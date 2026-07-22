package proxy

import (
	"expvar"
	"net/http"
	"time"
)

// ponytail: expvar is the stdlib metrics registry — atomic counters plus a JSON
// handler, no dependency. Swap for Prometheus when something needs to scrape it
// in Prometheus' own format; the counter call sites do not change.
var (
	PrimaryRequests      = expvar.NewInt("primary_requests_total")
	PrimaryErrors        = expvar.NewInt("primary_errors_total") // 5xx, including our own 504
	PrimaryLatencyMicros = expvar.NewInt("primary_latency_us_total")
	PrimaryInFlight      = expvar.NewInt("primary_in_flight")

	ShadowDispatched = expvar.NewInt("shadow_dispatched_total")
	ShadowDropped    = expvar.NewInt("shadow_dropped_total") // queue was full
	ShadowErrors     = expvar.NewInt("shadow_errors_total")
	ShadowUnsampled  = expvar.NewInt("shadow_unsampled_total")
	ShadowTooLarge   = expvar.NewInt("shadow_too_large_total")
	ShadowLoops      = expvar.NewInt("shadow_loops_blocked_total")
)

// MetricsHandler serves the expvar registry as JSON.
func MetricsHandler() http.Handler { return expvar.Handler() }

// Instrument records request count, in-flight depth, latency and 5xx rate for
// the primary path.
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		PrimaryInFlight.Add(1)
		defer PrimaryInFlight.Add(-1)

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		PrimaryRequests.Add(1)
		PrimaryLatencyMicros.Add(time.Since(start).Microseconds())
		if rec.status >= 500 {
			PrimaryErrors.Add(1)
		}
	})
}

// statusRecorder remembers the status code on its way through.
//
// Unwrap is what keeps streaming intact: http.ResponseController walks it to
// reach the real writer's Flush and Hijack, which ReverseProxy needs for
// streamed responses and connection upgrades.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
