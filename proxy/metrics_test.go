package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstrumentCountsRequestsErrorsAndLatency(t *testing.T) {
	before := PrimaryRequests.Value()
	beforeErrors := PrimaryErrors.Value()
	beforeMicros := PrimaryLatencyMicros.Value()

	srv := httptest.NewServer(Instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		io.WriteString(w, "ok")
	})))
	defer srv.Close()

	for _, path := range []string{"/", "/", "/boom"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if got := PrimaryRequests.Value() - before; got != 3 {
		t.Errorf("primary_requests_total moved by %d, want 3", got)
	}
	if got := PrimaryErrors.Value() - beforeErrors; got != 1 {
		t.Errorf("primary_errors_total moved by %d, want 1", got)
	}
	if PrimaryLatencyMicros.Value() < beforeMicros {
		t.Error("primary_latency_us_total went backwards")
	}
	if got := PrimaryInFlight.Value(); got != 0 {
		t.Errorf("primary_in_flight = %d after all requests finished, want 0", got)
	}
}

// ReverseProxy needs Flush to stream. The recorder must not hide it.
func TestStatusRecorderKeepsResponseStreamable(t *testing.T) {
	flushed := make(chan error, 1)

	srv := httptest.NewServer(Instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "chunk")
		flushed <- http.NewResponseController(w).Flush()
	})))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if err := <-flushed; err != nil {
		t.Errorf("Flush through statusRecorder: %v", err)
	}
}

func TestMetricsHandlerServesJSON(t *testing.T) {
	srv := httptest.NewServer(MetricsHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var vars map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&vars); err != nil {
		t.Fatalf("decoding /metrics: %v", err)
	}
	for _, key := range []string{
		"primary_requests_total", "primary_errors_total", "primary_latency_us_total",
		"primary_in_flight", "shadow_dispatched_total", "shadow_dropped_total",
		"shadow_errors_total", "shadow_unsampled_total", "shadow_too_large_total",
	} {
		if _, ok := vars[key]; !ok {
			t.Errorf("/metrics is missing %s", key)
		}
	}
}
