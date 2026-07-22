package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
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
	shadow, err := NewShadow(shadowBackend.URL)
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
	shadow, err := NewShadow(deadURL)
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

func TestNewShadowRejectsRelativeURL(t *testing.T) {
	if _, err := NewShadow("shadow.internal"); err == nil {
		t.Error("expected error for non-absolute shadow URL")
	}
}
