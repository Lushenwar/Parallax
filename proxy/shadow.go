package proxy

import (
	"errors"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
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

	// queue is the bounded handoff to the worker pool. Full queue means drop.
	queue chan *http.Request
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
		queue:  make(chan *http.Request, queueSize),
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

// mirrors reports whether requests with this method are eligible for mirroring.
func (s *Shadow) mirrors(method string) bool {
	if s.methods == nil {
		return safeMethods[strings.ToUpper(method)]
	}
	return s.methods["*"] || s.methods[strings.ToUpper(method)]
}

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

		// Sample before buffering: unsampled requests never pay the copy.
		if !s.sampled() {
			ShadowUnsampled.Add(1)
			next.ServeHTTP(w, r)
			return
		}

		// Over-limit and unreadable bodies still go to the primary, unmirrored.
		body, err := BufferBody(r)
		switch {
		case errors.Is(err, ErrPayloadTooLarge):
			ShadowTooLarge.Add(1)
		case err != nil:
			log.Printf("shadow buffering failed: %s %s: %v", r.Method, r.URL.Path, err)
		default:
			s.Dispatch(r, body)
		}
		next.ServeHTTP(w, r)
	})
}

// Dispatch clones r and hands it to the worker pool. It never blocks: if the
// queue is full the mirror is dropped, because waiting for shadow capacity
// would put the shadow backend's latency on the primary path (backpressure rule).
func (s *Shadow) Dispatch(r *http.Request, body []byte) {
	req, err := CloneForShadow(r, s.Target, body)
	if err != nil {
		log.Printf("shadow clone failed: %s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	req.Header.Set(ShadowHeader, "true")

	select {
	case s.queue <- req:
	default:
		ShadowDropped.Add(1) // Queue full — drop silently, counted only.
	}
}

func (s *Shadow) worker() {
	for req := range s.queue {
		s.send(req)
	}
}

func (s *Shadow) send(req *http.Request) {
	start := time.Now()
	resp, err := s.Client.Do(req)
	if err != nil {
		ShadowErrors.Add(1)
		return // Fail silently. Shadow problems must never surface to the client.
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // Drain so the connection returns to the pool.

	ShadowLatencyMicros.Add(time.Since(start).Microseconds())
	ShadowDispatched.Add(1)
}

// sampled reports whether this request is one of the mirrored ones.
//
// ponytail: independent per-request coin flip, not a counter-based every-Nth
// scheme. Statistically equivalent at volume and immune to traffic that arrives
// in a repeating pattern. Swap in a deterministic hash of a request ID if
// mirroring needs to be reproducible.
func (s *Shadow) sampled() bool {
	if !s.enabled.Load() {
		return false
	}
	units := s.sampleUnits.Load()
	switch {
	case units <= 0:
		return false
	case units >= 100*sampleScale:
		return true
	default:
		return rand.Int64N(100*sampleScale) < units
	}
}

func isMirrored(r *http.Request) bool {
	for _, h := range loopHeaders {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}
