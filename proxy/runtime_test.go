package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLatencyWindowPercentiles pins the number the whole feature exists for:
// one slow request in a hundred must be visible at p99 and invisible at p50.
// A mean would hide it in both.
func TestLatencyWindowPercentiles(t *testing.T) {
	w := newLatencyWindow(latencyWindowSize)
	for i := 0; i < 99; i++ {
		w.Add(1000) // 1ms
	}
	w.Add(500_000) // one 500ms outlier

	got := w.Latency()
	if got.Samples != 100 {
		t.Fatalf("samples = %d, want 100", got.Samples)
	}
	if got.P50 != 1 {
		t.Errorf("p50 = %v ms, want 1 — the outlier leaked into the median", got.P50)
	}
	if got.P99 != 1 {
		t.Errorf("p99 = %v ms, want 1 (nearest rank of 100 samples is the 99th)", got.P99)
	}
	if got.Max != 500 {
		t.Errorf("max = %v ms, want 500 — the outlier vanished", got.Max)
	}

	// The window is fixed: older samples must fall off rather than accumulate.
	small := newLatencyWindow(4)
	for _, v := range []int64{9000, 9000, 1000, 1000, 1000, 1000} {
		small.Add(v)
	}
	if l := small.Latency(); l.Samples != 4 || l.Max != 1 {
		t.Errorf("ring did not evict: %+v, want 4 samples with max 1ms", l)
	}
}

// TestSamplingIsTraceCoherent is the guard on false positives in the diff feed:
// every hop of one trace must land on the same side of the sampling threshold,
// or a mirrored POST /cart/add arrives without its POST /login and the shadow
// backend's 401 gets filed as a code difference.
func TestSamplingIsTraceCoherent(t *testing.T) {
	s := newTestShadow(nil, 50, 1)

	req := func(header, value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/anything", nil)
		r.Header.Set(header, value)
		return r
	}

	// Same trace, different paths: one decision, repeated.
	first := s.sampled(req("X-Trace-Id", "trace-abc"))
	for i := 0; i < 50; i++ {
		if s.sampled(req("X-Trace-Id", "trace-abc")) != first {
			t.Fatal("same trace ID sampled inconsistently; flows will be mirrored in pieces")
		}
	}

	// Different traces must not all get the same answer, or "sampling" at 50%
	// is really 0% or 100%.
	var mirrored int
	for i := 0; i < 200; i++ {
		if s.sampled(req("X-Trace-Id", string(rune('a'+i%26))+time.Duration(i).String())) {
			mirrored++
		}
	}
	if mirrored == 0 || mirrored == 200 {
		t.Errorf("hash sampling degenerate: %d/200 mirrored at a 50%% rate", mirrored)
	}

	// W3C traceparent changes its span field per hop; only the trace-id field
	// is stable, so that is the field that must be hashed.
	parent := s.sampled(req("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"))
	child := s.sampled(req("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-b7ad6b7169203331-01"))
	if parent != child {
		t.Error("traceparent hashed whole; the span field broke flow coherence")
	}

	// Browser traffic carries no trace header, so the session cookie stands in.
	s.SetTraceCookie("sid")
	withCookie := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: "sid", Value: "session-42"})
		return r
	}
	seen := s.sampled(withCookie())
	for i := 0; i < 20; i++ {
		if s.sampled(withCookie()) != seen {
			t.Fatal("session cookie sampled inconsistently")
		}
	}
}

func TestIgnoredPathsAreNotMirrored(t *testing.T) {
	s := newTestShadow(nil, 100, 1)
	s.SetIgnorePaths([]string{"/health", "/internal/*", "", "  "})

	for _, p := range []string{"/health", "/internal/debug"} {
		if !s.ignoredPath(p) {
			t.Errorf("%s should be excluded from mirroring", p)
		}
	}
	for _, p := range []string{"/healthy", "/api/health", "/internal/a/b"} {
		if s.ignoredPath(p) {
			t.Errorf("%s was excluded but should be mirrored", p)
		}
	}
}

// TestControlPlaneRequiresToken covers the trust boundary: /api/config retunes
// a proxy carrying live traffic, and CORS does not stop anything that is not a
// browser.
func TestControlPlaneRequiresToken(t *testing.T) {
	srv := httptest.NewServer(APIHandler(nil, testOrigin, "s3cret"))
	defer srv.Close()

	get := func(auth string) int {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/config", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if code := get(""); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}
	if code := get("Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", code)
	}
	if code := get("s3cret"); code != http.StatusUnauthorized {
		t.Errorf("bare token without the Bearer scheme: got %d, want 401", code)
	}
	if code := get("Bearer s3cret"); code != http.StatusOK {
		t.Errorf("correct token: got %d, want 200", code)
	}

	// Preflight cannot carry an Authorization header, so it must answer before
	// the check or the browser never sends the real request.
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/config", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("preflight got %d, want 204", res.StatusCode)
	}
}

// TestOverheadExcludesBackendTime is what makes the overhead number defensible:
// a backend that takes 200ms must not be reported as 200ms of proxy overhead.
func TestOverheadExcludesBackendTime(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte("slow"))
	}))
	defer backend.Close()

	primary, err := NewPrimary(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(Instrument(primary))

	before := ProxyOverheadCount.Value()
	res, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// Close waits for the handler to return; the client is served before
	// Instrument does its bookkeeping, so reading the counter without this
	// races the handler.
	front.Close()

	if ProxyOverheadCount.Value() == before {
		t.Fatal("no overhead sample recorded")
	}
	// Generous ceiling: this asserts the backend's 150ms was subtracted out,
	// not a specific performance target on a shared CI runner.
	if got := proxyOverhead.Latency().Max; got > 50 {
		t.Errorf("overhead max = %v ms; backend sleep was billed to the proxy", got)
	}
}
