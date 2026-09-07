package proxy

import (
	"errors"
	"io"
	"log"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

// ShadowHeader marks mirrored traffic so a shadow backend can tell it apart.
const ShadowHeader = "X-Shadow-Traffic"

// loopHeaders are the markers that mean "this request is already a mirror".
// If the shadow target is misconfigured to point back at the proxy, seeing one
// of these is the only thing standing between us and infinite amplification
// (danger zone #4). CLAUDE.md names the header both ways, so honour both.
var loopHeaders = []string{ShadowHeader, "X-Shadow-Request"}

// sampleScale is how many atomic units make up one percent. Storing the rate as
// a scaled integer keeps it atomically mutable from the control plane without
// float bit-twiddling; the cost is a floor of 0.01% granularity.
const sampleScale = 100

// safeMethods is the mirror allowlist a zero-value Shadow uses: the methods
// that are not supposed to change state.
//
// A mirrored write is a real write. If the shadow environment shares anything
// at all with production — a payment sandbox, a mailer, an SMS gateway, a
// third-party key, a queue — then mirroring POST /charge charges twice and
// mirroring DELETE /users/42 deletes twice. Writes are therefore opt-in via
// SHADOW_METHODS, to be turned on only once the shadow stack is known to be
// isolated.
var safeMethods = map[string]bool{http.MethodGet: true, http.MethodHead: true}

// Shadow mirrors requests to a secondary backend. Every dispatch is
// fire-and-forget: nothing here may ever block the primary request path.
//
// Must be used by pointer: it carries atomics that the control plane writes
// while requests are being served.
type Shadow struct {
	Target *url.URL
	Client *http.Client

	// sampleUnits is the mirror rate in hundredths of a percent, 0..10000.
	sampleUnits atomic.Int64
	enabled     atomic.Bool

	// methods is the mirror allowlist, keyed by upper-case HTTP method, with
	// "*" meaning every method. Written once at startup and read by every
	// request goroutine after that — unlike sampleUnits and enabled, this is
	// not safe to retune while serving. nil means safeMethods.
	methods map[string]bool

	// ignorePaths are path.Match globs that are never mirrored, and traceCookie
	// is the cookie to fall back on when a request carries no trace header.
	// Written once at startup, like methods.
	ignorePaths []string
	traceCookie string

	// queue is the bounded handoff to the worker pool. Full queue means drop.
	queue chan *mirror

	// diffs is where comparison results land. nil disables comparison entirely,
	// in which case the shadow response is drained and discarded as before.
	// Written once at startup, like methods.
	diffs  *DiffStore
	ignore ignoreList
}

// mirror is one queued clone, optionally carrying what the primary answered so
// the worker can compare the two.
type mirror struct {
	req     *http.Request
	primary *capturedResponse
}

// NewShadow returns a mirror aimed at shadowURL, sampling sampleRate percent of
// traffic through a queue of queueSize served by workers goroutines. The
// workers start immediately and run for the life of the process.
func NewShadow(shadowURL string, sampleRate float64, queueSize, workers int) (*Shadow, error) {
	target, err := url.Parse(shadowURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, errors.New("shadow URL must be absolute, e.g. http://127.0.0.1:9001")
	}
	if err := validSampleRate(sampleRate); err != nil {
		return nil, err
	}
	if queueSize < 1 || workers < 1 {
		return nil, errors.New("shadow queue size and worker count must be at least 1")
	}

	s := &Shadow{
		Target: target,
		Client: ShadowClient,
		queue:  make(chan *mirror, queueSize),
	}
	s.SetSampleRate(sampleRate)
	s.SetEnabled(true)

	for i := 0; i < workers; i++ {
		go s.worker()
	}
	return s, nil
}

func validSampleRate(rate float64) error {
	if math.IsNaN(rate) || rate < 0 || rate > 100 {
		return errors.New("shadow sample rate must be between 0 and 100")
	}
	return nil
}

// SampleRate is the percentage of traffic currently being mirrored.
func (s *Shadow) SampleRate() float64 {
	return float64(s.sampleUnits.Load()) / sampleScale
}

// SetSampleRate retunes mirroring live. Out-of-range values are clamped rather
// than rejected; callers that need rejection validate first.
func (s *Shadow) SetSampleRate(rate float64) {
	s.sampleUnits.Store(int64(math.Round(math.Min(math.Max(rate, 0), 100) * sampleScale)))
}

// SetMethods replaces the mirror allowlist. A single "*" mirrors every method,
// which is only safe when the shadow environment shares no downstream with
// production. An empty list is rejected rather than silently mirroring nothing.
//
// Call it before the proxy starts serving; the list is read without a lock.
func (s *Shadow) SetMethods(methods []string) error {
	set := make(map[string]bool, len(methods))
	for _, m := range methods {
		if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
			set[m] = true
		}
	}
	if len(set) == 0 {
		return errors.New("shadow method list is empty; use \"*\" to mirror every method")
	}
	s.methods = set
	return nil
}

// SetIgnorePaths excludes matching request paths from mirroring. Patterns are
// path.Match globs: "/health", "/internal/*". Empty entries are ignored, so an
// unset env var is a no-op.
//
// Health checks and readiness probes are the motivating case — they are high
// volume, they tell you nothing when mirrored, and they crowd real traffic out
// of the diff feed.
//
// Call it before the proxy starts serving; the list is read without a lock.
func (s *Shadow) SetIgnorePaths(patterns []string) {
	s.ignorePaths = s.ignorePaths[:0]
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			s.ignorePaths = append(s.ignorePaths, p)
		}
	}
}

// SetTraceCookie names the cookie that identifies a session, for browser
// traffic that carries no trace header. Call it before serving.
func (s *Shadow) SetTraceCookie(name string) { s.traceCookie = strings.TrimSpace(name) }

// ignoredPath reports whether p is excluded from mirroring.
//
// ponytail: path.Match, so "*" stops at a "/" — "/internal/*" does not cover
// "/internal/a/b". Add "/internal/*/*" or move to a prefix check if that bites.
func (s *Shadow) ignoredPath(p string) bool {
	for _, pat := range s.ignorePaths {
		if ok, err := path.Match(pat, p); ok && err == nil {
			return true
		}
	}
	return false
}

// mirrors reports whether requests with this method are eligible for mirroring.
func (s *Shadow) mirrors(method string) bool {
	if s.methods == nil {
		return safeMethods[strings.ToUpper(method)]
	}
	return s.methods["*"] || s.methods[strings.ToUpper(method)]
}

// EnableDiffs turns on response comparison, keeping the most recent limit
// mismatches. ignorePaths is a comma-separated list of JSON paths whose
// differences are expected rather than interesting — "data.createdAt",
// "items.*.id" — where a "*" segment matches any one path segment.
//
// Comparison makes the proxy hold the primary response body in memory (capped
// at MaxCaptureSize) for the life of the mirror, which is why it is opt-in.
// Call it before the proxy starts serving.
func (s *Shadow) EnableDiffs(limit int, ignorePaths string) {
	s.diffs = NewDiffStore(limit)
	s.ignore = parseIgnoreList(ignorePaths)
}

// Diffs is the recorded-mismatch store, or nil when comparison is off.
func (s *Shadow) Diffs() *DiffStore { return s.diffs }

// Enabled reports whether mirroring is currently on.
func (s *Shadow) Enabled() bool { return s.enabled.Load() }

// SetEnabled turns mirroring on or off without restarting the proxy.
func (s *Shadow) SetEnabled(on bool) { s.enabled.Store(on) }

// QueueDepth is the number of clones waiting for a worker.
func (s *Shadow) QueueDepth() int { return len(s.queue) }

// Middleware mirrors qualifying requests and hands the original to next. The
// mirror is prepared before next runs because next may mutate the request, and
// handed off asynchronously so the primary response never waits on the shadow
// backend (danger zone #1).
func (s *Shadow) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMirrored(r) {
			ShadowLoops.Add(1)
			log.Printf("loop guard: dropping already-mirrored request %s %s", r.Method, r.URL.Path)
			http.Error(w, "shadow traffic loop detected", http.StatusLoopDetected)
			return
		}

		// Method filter before sampling: a write must never be mirrored, at any
		// sample rate.
		if !s.mirrors(r.Method) {
			ShadowSkippedMethod.Add(1)
			next.ServeHTTP(w, r)
			return
		}

		if s.ignoredPath(r.URL.Path) {
			ShadowSkippedPath.Add(1)
			next.ServeHTTP(w, r)
			return
		}

		// Sample before buffering: unsampled requests never pay the copy.
		if !s.sampled(r) {
			ShadowUnsampled.Add(1)
			next.ServeHTTP(w, r)
			return
		}

		// Over-limit and unreadable bodies still go to the primary, unmirrored.
		var m *mirror
		body, err := BufferBody(r)
		switch {
		case errors.Is(err, ErrPayloadTooLarge):
			ShadowTooLarge.Add(1)
		case err != nil:
			log.Printf("shadow buffering failed: %s %s: %v", r.Method, r.URL.Path, err)
		default:
			// Clone before next runs, because next may mutate the request.
			m = s.prepare(r, body)
		}

		// Capture the primary response only when there is a mirror to compare it
		// against — an unmirrored request should not pay for a buffer nobody reads.
		if m == nil || s.diffs == nil {
			next.ServeHTTP(w, r)
		} else {
			cw := &captureWriter{ResponseWriter: w}
			next.ServeHTTP(cw, r)
			m.primary = cw.captured()
		}

		// Enqueue after the primary is served: the queue handoff is instant, but
		// the primary path owes the client nothing behind it.
		if m != nil {
			s.enqueue(m)
		}
	})
}

// Dispatch clones r and hands it to the worker pool without comparing
// responses. It never blocks: if the queue is full the mirror is dropped,
// because waiting for shadow capacity would put the shadow backend's latency on
// the primary path (backpressure rule).
func (s *Shadow) Dispatch(r *http.Request, body []byte) {
	if m := s.prepare(r, body); m != nil {
		s.enqueue(m)
	}
}

// prepare builds the clone. It returns nil if the request cannot be cloned,
// which is logged and otherwise ignored — the primary is unaffected either way.
func (s *Shadow) prepare(r *http.Request, body []byte) *mirror {
	req, err := CloneForShadow(r, s.Target, body)
	if err != nil {
		log.Printf("shadow clone failed: %s %s: %v", r.Method, r.URL.Path, err)
		return nil
	}
	req.Header.Set(ShadowHeader, "true")
	return &mirror{req: req}
}

func (s *Shadow) enqueue(m *mirror) {
	select {
	case s.queue <- m:
	default:
		ShadowDropped.Add(1) // Queue full — drop silently, counted only.
	}
}

func (s *Shadow) worker() {
	for m := range s.queue {
		s.send(m)
	}
}

func (s *Shadow) send(m *mirror) {
	start := time.Now()
	resp, err := s.Client.Do(m.req)
	if err != nil {
		ShadowErrors.Add(1)
		return // Fail silently. Shadow problems must never surface to the client.
	}
	defer resp.Body.Close()

	var shadow *capturedResponse
	if m.primary != nil {
		shadow = captureResponse(resp) // drains the body too
	} else {
		io.Copy(io.Discard, resp.Body) // Drain so the connection returns to the pool.
	}

	elapsed := time.Since(start).Microseconds()
	ShadowLatencyMicros.Add(elapsed)
	shadowLatency.Add(elapsed)
	ShadowDispatched.Add(1)

	if shadow != nil {
		s.record(m, shadow)
	}
}

// record compares the two responses and files any disagreement.
func (s *Shadow) record(m *mirror, shadow *capturedResponse) {
	reasons := compareResponses(m.primary, shadow, s.ignore)
	if len(reasons) == 0 {
		DiffMatches.Add(1)
		return
	}
	DiffMismatches.Add(1)
	s.diffs.Add(Diff{
		At:            time.Now().UTC(),
		Method:        m.req.Method,
		Path:          m.req.URL.Path,
		PrimaryStatus: m.primary.Status,
		ShadowStatus:  shadow.Status,
		Reasons:       reasons,
	})
}

// traceHeaders are the identifiers a request may already carry that are stable
// across every hop of one logical flow, most specific first.
var traceHeaders = []string{"X-Trace-Id", "Traceparent", "X-Request-Id", "X-Correlation-Id"}

// sampled reports whether this request is one of the mirrored ones.
//
// It hashes a trace or session ID when the request carries one, so mirroring is
// coherent per flow rather than per request: every hop of a sampled trace is
// mirrored, and none of an unsampled one. A per-request coin flip mirrors
// POST /cart/add while dropping the POST /login before it, and the shadow
// backend answers 401 — a mismatch manufactured by the sampler, in a tool whose
// entire output is mismatches.
//
// With no ID to key on it falls back to the coin flip, which is the honest
// answer: there is nothing to be coherent about.
func (s *Shadow) sampled(r *http.Request) bool {
	if !s.enabled.Load() {
		return false
	}
	units := s.sampleUnits.Load()
	switch {
	case units <= 0:
		return false
	case units >= 100*sampleScale:
		return true
	}

	if id := s.traceID(r); id != "" {
		h := fnv.New64a()
		h.Write([]byte(id))
		return int64(h.Sum64()%(100*sampleScale)) < units
	}
	return rand.Int64N(100*sampleScale) < units
}

// traceID returns the flow identifier for r, or "" if it carries none.
func (s *Shadow) traceID(r *http.Request) string {
	for _, h := range traceHeaders {
		v := strings.TrimSpace(r.Header.Get(h))
		if v == "" {
			continue
		}
		// W3C traceparent is "00-<trace-id>-<span-id>-<flags>". Only the
		// trace-id field is stable across the flow; the span changes per hop,
		// so hashing the whole header would be the coin flip with extra steps.
		if strings.EqualFold(h, "Traceparent") {
			if parts := strings.Split(v, "-"); len(parts) >= 2 {
				return parts[1]
			}
			continue
		}
		return v
	}
	if s.traceCookie != "" {
		if c, err := r.Cookie(s.traceCookie); err == nil {
			return c.Value
		}
	}
	return ""
}

func isMirrored(r *http.Request) bool {
	for _, h := range loopHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}
