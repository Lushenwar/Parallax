package proxy

import (
	"context"
	"expvar"
	"net/http"
	"sync/atomic"
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

	ShadowDispatched    = expvar.NewInt("shadow_dispatched_total")
	ShadowDropped       = expvar.NewInt("shadow_dropped_total") // queue was full
	ShadowErrors        = expvar.NewInt("shadow_errors_total")
	ShadowUnsampled     = expvar.NewInt("shadow_unsampled_total")
	ShadowSkippedMethod = expvar.NewInt("shadow_skipped_method_total") // not in the method allowlist
	ShadowSkippedPath   = expvar.NewInt("shadow_skipped_path_total")   // matched SHADOW_IGNORE_PATHS
	ShadowTooLarge      = expvar.NewInt("shadow_too_large_total")
	ShadowLoops         = expvar.NewInt("shadow_loops_blocked_total")
	ShadowLatencyMicros = expvar.NewInt("shadow_latency_us_total")

	DiffMatches    = expvar.NewInt("diff_matches_total")
	DiffMismatches = expvar.NewInt("diff_mismatches_total")

	// ProxyOverhead* is wall time spent inside this process rather than waiting
	// on the primary backend: total handler time minus the backend round trip
	// and body read. It is the answer to "what does putting Parallax in the
	// request path cost?", measured on live traffic instead of inferred from a
	// benchmark run against a different machine on a different day.
	ProxyOverheadMicros = expvar.NewInt("proxy_overhead_us_total")
	ProxyOverheadCount  = expvar.NewInt("proxy_overhead_samples_total")
)

// Windowed samples behind the percentiles. Counters above give lifetime totals;
// these give the shape of the recent tail, which is the number that decides
// whether a proxy in the request path is acceptable.
var (
	primaryLatency = newLatencyWindow(latencyWindowSize)
	shadowLatency  = newLatencyWindow(latencyWindowSize)
	proxyOverhead  = newLatencyWindow(latencyWindowSize)
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

		// The transport writes the backend's own time into this slot on the way
		// past, so overhead falls out as a subtraction rather than needing a
		// separate control run against the backend.
		upstream := new(atomic.Int64)
		r = r.WithContext(context.WithValue(r.Context(), upstreamKey{}, upstream))

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		total := time.Since(start).Microseconds()
		PrimaryRequests.Add(1)
		PrimaryLatencyMicros.Add(total)
		primaryLatency.Add(total)
		if rec.status >= 500 {
			PrimaryErrors.Add(1)
		}

		// up == 0 means the request never reached the backend (a loop-guard
		// rejection, say); there is no overhead to attribute. total == up is a
		// real reading, not a bad one — on a warm loopback the proxy's own work
		// lands under the clock's resolution, and discarding those samples
		// would bias the reported overhead upward.
		if up := upstream.Load(); up > 0 && total >= up {
			ProxyOverheadMicros.Add(total - up)
			ProxyOverheadCount.Add(1)
			proxyOverhead.Add(total - up)
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
