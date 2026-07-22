package proxy

import (
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
)

// ShadowHeader marks mirrored traffic so a shadow backend can tell it apart.
const ShadowHeader = "X-Shadow-Traffic"

// loopHeaders are the markers that mean "this request is already a mirror".
// If the shadow target is misconfigured to point back at the proxy, seeing one
// of these is the only thing standing between us and infinite amplification
// (danger zone #4). CLAUDE.md names the header both ways, so honour both.
var loopHeaders = []string{ShadowHeader, "X-Shadow-Request"}

// Shadow mirrors requests to a secondary backend. Every dispatch is
// fire-and-forget: nothing here may ever block the primary request path.
type Shadow struct {
	Target *url.URL
	Client *http.Client

	// SampleRate is the percentage of traffic to mirror, 0 to 100.
	SampleRate float64

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
	if sampleRate < 0 || sampleRate > 100 {
		return nil, errors.New("shadow sample rate must be between 0 and 100")
	}
	if queueSize < 1 || workers < 1 {
		return nil, errors.New("shadow queue size and worker count must be at least 1")
	}

	s := &Shadow{
		Target:     target,
		Client:     ShadowClient,
		SampleRate: sampleRate,
		queue:      make(chan *http.Request, queueSize),
	}
	for i := 0; i < workers; i++ {
		go s.worker()
	}
	return s, nil
}

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

// QueueDepth is the number of clones waiting for a worker. Publish it as a
// gauge if you want it in /metrics; it is not registered here because a process
// may hold more than one Shadow and expvar names must be unique.
func (s *Shadow) QueueDepth() int { return len(s.queue) }

func (s *Shadow) worker() {
	for req := range s.queue {
		s.send(req)
	}
}

func (s *Shadow) send(req *http.Request) {
	resp, err := s.Client.Do(req)
	if err != nil {
		ShadowErrors.Add(1)
		return // Fail silently. Shadow problems must never surface to the client.
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // Drain so the connection returns to the pool.
	ShadowDispatched.Add(1)
}

// sampled reports whether this request is one of the mirrored ones.
//
// ponytail: independent per-request coin flip, not a counter-based every-Nth
// scheme. Statistically equivalent at volume and immune to traffic that arrives
// in a repeating pattern. Swap in a deterministic hash of a request ID if
// mirroring needs to be reproducible.
func (s *Shadow) sampled() bool {
	switch {
	case s.SampleRate <= 0:
		return false
	case s.SampleRate >= 100:
		return true
	default:
		return rand.Float64()*100 < s.SampleRate
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
