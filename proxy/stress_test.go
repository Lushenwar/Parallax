package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPrimarySurvivesPausedShadow is the Phase 4 exit criterion as a test: with
// the shadow backend artificially paused, the primary path must stay fast and
// error-free while the excess mirrors are dropped.
func TestPrimarySurvivesPausedShadow(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	const (
		requests    = 2000
		concurrency = 50
		p99Budget   = 250 * time.Millisecond
	)

	primaryBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, "ok")
	}))
	defer primaryBackend.Close()

	release := make(chan struct{})
	shadowBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // paused: never answers
	}))
	defer shadowBackend.Close()
	defer close(release) // LIFO: unblock handlers before Close waits on them

	primary, err := NewPrimary(primaryBackend.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately tiny queue so backpressure shows up immediately.
	shadow, err := NewShadow(shadowBackend.URL, 100, 8, 4)
	if err != nil {
		t.Fatal(err)
	}

	front := httptest.NewServer(Instrument(shadow.Middleware(primary)))
	defer front.Close()

	client := &http.Client{Timeout: 10 * time.Second}
	latencies := make([]time.Duration, requests)
	statuses := make([]int, requests)

	droppedBefore := ShadowDropped.Value()

	var wg sync.WaitGroup
	work := make(chan int)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range work {
				start := time.Now()
				resp, err := client.Post(front.URL+"/orders", "application/json", strings.NewReader(`{"qty":1}`))
				latencies[n] = time.Since(start)
				if err != nil {
					statuses[n] = -1
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				statuses[n] = resp.StatusCode
			}
		}()
	}
	for n := 0; n < requests; n++ {
		work <- n
	}
	close(work)
	wg.Wait()

	for n, code := range statuses {
		if code != http.StatusOK {
			t.Fatalf("request %d returned %d — the paused shadow leaked into the primary path", n, code)
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[requests*99/100]
	if p99 > p99Budget {
		t.Errorf("primary p99 = %v, budget %v (max %v)", p99, p99Budget, latencies[requests-1])
	}

	if dropped := ShadowDropped.Value() - droppedBefore; dropped == 0 {
		t.Error("expected the bounded queue to drop mirrors while the shadow was paused, got none")
	}
	t.Logf("p50=%v p99=%v max=%v shadow_dropped=%d",
		latencies[requests/2], p99, latencies[requests-1], ShadowDropped.Value()-droppedBefore)
}
