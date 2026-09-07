package proxy

import (
	"math"
	"slices"
	"sync"
)

// latencyWindowSize is how many recent samples each window keeps. 2048 covers
// a couple of minutes at a few hundred req/s, which is the timescale an
// operator watching a dashboard cares about.
const latencyWindowSize = 2048

// Latency is a windowed latency summary in milliseconds.
//
// A lifetime mean cannot answer the only question that matters for a proxy in
// the request path — how bad is the tail — because one slow request in ten
// thousand disappears into it. Samples is included so the dashboard can say
// "not enough data yet" instead of showing a confident 0.
type Latency struct {
	P50     float64 `json:"p50"`
	P95     float64 `json:"p95"`
	P99     float64 `json:"p99"`
	Max     float64 `json:"max"`
	Samples int     `json:"samples"`
}

// latencyWindow is a fixed ring of recent samples in microseconds.
//
// ponytail: ring buffer plus sort-on-read, not a t-digest or HDR histogram.
// Exact over the window, allocates nothing on the hot path, and being windowed
// rather than lifetime is what makes a spike visible at all. The ceiling is the
// O(n log n) sort per read — fine at 2048 samples and one read every 2s, wrong
// if the window ever needs to be big enough to reason about hours. Swap in a
// bucketed histogram at that point; Add/Latency do not change shape.
type latencyWindow struct {
	mu  sync.Mutex
	buf []int64
	n   int // total ever recorded; also the write cursor
}

func newLatencyWindow(size int) *latencyWindow {
	return &latencyWindow{buf: make([]int64, size)}
}

// Add records one observation in microseconds, overwriting the oldest.
func (w *latencyWindow) Add(micros int64) {
	w.mu.Lock()
	w.buf[w.n%len(w.buf)] = micros
	w.n++
	w.mu.Unlock()
}

// Latency summarises the window. Reading is O(n log n); it happens per
// dashboard poll, not per request.
func (w *latencyWindow) Latency() Latency {
	s := w.sorted()
	return Latency{
		P50:     quantileMillis(s, 50),
		P95:     quantileMillis(s, 95),
		P99:     quantileMillis(s, 99),
		Max:     quantileMillis(s, 100),
		Samples: len(s),
	}
}

// sorted copies the live samples out from under the lock, then sorts the copy —
// holding the lock across the sort would put it on the request path.
func (w *latencyWindow) sorted() []int64 {
	w.mu.Lock()
	n := min(w.n, len(w.buf))
	out := make([]int64, n)
	copy(out, w.buf[:n])
	w.mu.Unlock()

	slices.Sort(out)
	return out
}

// quantileMillis returns the nearest-rank q-th percentile of a sorted
// microsecond slice, in milliseconds rounded to two decimals.
//
// Nearest rank rather than interpolation: it always returns a latency that was
// actually observed, which is the honest thing to put next to "p99".
func quantileMillis(sorted []int64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(q/100*float64(len(sorted)))) - 1
	i = min(max(i, 0), len(sorted)-1)
	return math.Round(float64(sorted[i])/10) / 100
}
