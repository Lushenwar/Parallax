package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type captured struct {
	method string
	path   string
	query  string
	header http.Header
	body   string
}

// newStack wires a primary echo backend behind the shadow middleware and
// returns the front-door server. shadowHandler receives the mirrored traffic.
func newStack(t *testing.T, shadowHandler http.HandlerFunc) *httptest.Server {
	t.Helper()

	primaryBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		io.WriteString(w, "primary:"+string(b))
	}))
	t.Cleanup(primaryBackend.Close)

	shadowBackend := httptest.NewServer(shadowHandler)
	t.Cleanup(shadowBackend.Close)

	primary, err := NewPrimary(primaryBackend.URL)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewShadow(shadowBackend.URL, 100, 16, 4)
	if err != nil {
		t.Fatal(err)
	}

	front := httptest.NewServer(shadow.Middleware(primary))
	t.Cleanup(front.Close)
	return front
}

func TestShadowDispatchDoesNotBlockPrimary(t *testing.T) {
	release := make(chan struct{})
	got := make(chan captured, 1)

	front := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)}
		<-release // shadow backend hangs until the test is done
	})
	// Deferred before t.Cleanup runs, so httptest's Close does not wait forever
	// on the hung shadow handler.
	defer close(release)

	start := time.Now()
	resp, err := http.Post(front.URL+"/orders?id=7", "application/json", strings.NewReader(`{"qty":2}`))
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if elapsed > 2*time.Second {
		t.Errorf("primary took %v — it waited on the hanging shadow", elapsed)
	}
	if string(body) != `primary:{"qty":2}` {
		t.Errorf("primary body = %q, want %q", body, `primary:{"qty":2}`)
	}

	select {
	case c := <-got:
		if c.method != http.MethodPost || c.path != "/orders" || c.query != "id=7" {
			t.Errorf("shadow got %s %s?%s, want POST /orders?id=7", c.method, c.path, c.query)
		}
		if c.body != `{"qty":2}` {
			t.Errorf("shadow body = %q, want %q", c.body, `{"qty":2}`)
		}
	case <-time.After(3 * time.Second):
		t.Error("shadow backend never received the mirrored request")
	}
}

func TestPrimaryUnaffectedWhenShadowIsDown(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	primaryBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer primaryBackend.Close()

	primary, err := NewPrimary(primaryBackend.URL)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewShadow(deadURL, 100, 16, 4)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(shadow.Middleware(primary))
	defer front.Close()

	resp, err := http.Get(front.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("got %d %q, want 200 \"ok\" — an offline shadow leaked into the primary path", resp.StatusCode, body)
	}
}

func TestShadowRequestsAreMarked(t *testing.T) {
	got := make(chan captured, 1)
	front := newStack(t, func(w http.ResponseWriter, r *http.Request) {
		got <- captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), ""}
	})

	resp, err := http.Get(front.URL + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case c := <-got:
		if v := c.header.Get(ShadowHeader); v != "true" {
			t.Errorf("%s = %q, want \"true\"", ShadowHeader, v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shadow backend never received the mirrored request")
	}
}

func TestLoopGuardDropsAlreadyMirroredTraffic(t *testing.T) {
	for _, header := range loopHeaders {
		t.Run(header, func(t *testing.T) {
			mirrored := make(chan struct{}, 1)
			front := newStack(t, func(w http.ResponseWriter, r *http.Request) {
				mirrored <- struct{}{}
			})

			req, _ := http.NewRequest(http.MethodGet, front.URL+"/loop", nil)
			req.Header.Set(header, "true")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusLoopDetected {
				t.Errorf("status = %d, want 508", resp.StatusCode)
			}
			select {
			case <-mirrored:
				t.Error("already-mirrored request was amplified to the shadow backend")
			case <-time.After(250 * time.Millisecond):
			}
		})
	}
}

func TestNewShadowRejectsRelativeURL(t *testing.T) {
	if _, err := NewShadow("shadow.internal", 100, 16, 4); err == nil {
		t.Error("expected error for non-absolute shadow URL")
	}
}

func TestNewShadowRejectsOutOfRangeSampleRate(t *testing.T) {
	for _, rate := range []float64{-1, 100.5} {
		if _, err := NewShadow("http://shadow.internal", rate, 16, 4); err == nil {
			t.Errorf("expected error for sample rate %v", rate)
		}
	}
}

func TestDispatchDropsInsteadOfBlockingWhenQueueIsFull(t *testing.T) {
	const capacity = 2
	target, _ := url.Parse("http://shadow.internal")
	// No workers: nothing ever drains the queue, so it fills on the third send.
	s := newTestShadow(target, 100, capacity)

	r := httptest.NewRequest(http.MethodPost, "/burst", nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			s.Dispatch(r, []byte("payload"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatch blocked on a full queue — this would stall the primary path")
	}

	if got := len(s.queue); got != capacity {
		t.Errorf("queued %d requests, want %d (the rest must be dropped)", got, capacity)
	}
}

// newTestShadow builds a Shadow with no workers, so the queue only drains when
// a test wants it to.
func newTestShadow(target *url.URL, rate float64, capacity int) *Shadow {
	s := &Shadow{Target: target, Client: ShadowClient, queue: make(chan *http.Request, capacity)}
	s.SetSampleRate(rate)
	s.SetEnabled(true)
	return s
}

func TestSampleRateBoundsAreAbsolute(t *testing.T) {
	off := newTestShadow(nil, 0, 1)
	on := newTestShadow(nil, 100, 1)
	for i := 0; i < 1000; i++ {
		if off.sampled() {
			t.Fatal("sample rate 0 mirrored a request")
		}
		if !on.sampled() {
			t.Fatal("sample rate 100 skipped a request")
		}
	}
}

func TestSamplingApproximatesTheConfiguredRate(t *testing.T) {
	const (
		n         = 20000
		rate      = 25.0
		tolerance = 3.0 // percentage points; ~8 sigma at n=20000
	)
	s := newTestShadow(nil, rate, 1)

	hits := 0
	for i := 0; i < n; i++ {
		if s.sampled() {
			hits++
		}
	}

	got := float64(hits) / n * 100
	if got < rate-tolerance || got > rate+tolerance {
		t.Errorf("sampled %.2f%% of %d requests, want %.0f%% +/- %.0f", got, n, rate, tolerance)
	}
}
