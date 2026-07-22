package proxy

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
)

// Shadow mirrors requests to a secondary backend. Every dispatch is
// fire-and-forget: nothing here may ever block the primary request path.
type Shadow struct {
	Target *url.URL
	Client *http.Client
}

// NewShadow returns a mirror aimed at shadowURL.
func NewShadow(shadowURL string) (*Shadow, error) {
	target, err := url.Parse(shadowURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, errors.New("shadow URL must be absolute, e.g. http://127.0.0.1:9001")
	}
	return &Shadow{Target: target, Client: ShadowClient}, nil
}

// Middleware buffers the request body, mirrors a clone of it, then hands the
// original to next. The mirror is dispatched before next runs because next may
// mutate the request, and it is dispatched asynchronously so the primary
// response never waits on the shadow backend (danger zone #1).
func (s *Shadow) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := BufferBody(r)
		if err == nil {
			s.Dispatch(r, body)
		}
		// Over-limit and unreadable bodies still go to the primary, unmirrored.
		next.ServeHTTP(w, r)
	})
}

// Dispatch clones r and sends it to the shadow backend on its own goroutine.
// It returns immediately.
func (s *Shadow) Dispatch(r *http.Request, body []byte) {
	req, err := CloneForShadow(r, s.Target, body)
	if err != nil {
		log.Printf("shadow clone failed: %s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	go s.send(req)
}

func (s *Shadow) send(req *http.Request) {
	resp, err := s.Client.Do(req)
	if err != nil {
		return // Fail silently. Shadow problems must never surface to the client.
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // Drain so the connection returns to the pool.
}
