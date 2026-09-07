package proxy

import (
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Primary and shadow traffic use completely separate Transports so a slow shadow
// backend can never starve primary client connections (CLAUDE.md danger zone #3).

var PrimaryTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ForceAttemptHTTP2:     true,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConns:          1000,
	MaxIdleConnsPerHost:   100,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second, // Primary Timeout: strict, client gets 504.
}

// ShadowTimeout bounds a mirrored request end to end, body drain included.
const ShadowTimeout = 5 * time.Second

var ShadowTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	ForceAttemptHTTP2: true,
	DialContext:       (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 10 * time.Second}).DialContext,
	// Hard ceiling on sockets held against the shadow backend. Without it a
	// hanging shadow exhausts the proxy's ephemeral ports and takes primary
	// traffic down with it (danger zone #3).
	MaxConnsPerHost:       50,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       10 * time.Second, // Drop idle connections faster.
	ResponseHeaderTimeout: ShadowTimeout,    // Shadow Timeout: aggressive.
}

// ShadowClient is the only client that may talk to the shadow backend.
var ShadowClient = &http.Client{
	Transport: ShadowTransport,
	// ponytail: Client.Timeout already covers dial + headers + body drain, so
	// no per-request context deadline is needed on top of it.
	Timeout: ShadowTimeout,
	// Never follow redirects on mirrored traffic — the response is discarded anyway.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// upstreamKey addresses a per-request slot that measuredTransport adds the
// primary backend's own elapsed time to. Instrument owns the slot; subtracting
// it from total handler time is what makes proxy overhead a measured number
// instead of a claim.
type upstreamKey struct{}

// measuredTransport times the backend round trip and the read of its body,
// attributing both to the backend rather than to us.
//
// Counting the body read matters: RoundTrip returns at the first response
// header, so without it every byte the backend streams afterwards would be
// billed to the proxy and the overhead figure would track response size.
type measuredTransport struct{ http.RoundTripper }

func (m measuredTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	slot, _ := r.Context().Value(upstreamKey{}).(*atomic.Int64)

	start := time.Now()
	resp, err := m.RoundTripper.RoundTrip(r)
	if slot == nil || resp == nil {
		return resp, err
	}
	slot.Add(time.Since(start).Microseconds())

	resp.Body = &timedBody{ReadCloser: resp.Body, slot: slot}
	return resp, err
}

// timedBody bills the time spent waiting on backend bytes to the backend.
//
// ponytail: two time.Now() calls per Read. At ReverseProxy's 32KB copy buffer
// that is a few hundred nanoseconds against a network read, i.e. below the
// noise floor of the thing being measured. If it ever shows up, sample it.
type timedBody struct {
	io.ReadCloser
	slot *atomic.Int64
}

func (t *timedBody) Read(p []byte) (int, error) {
	start := time.Now()
	n, err := t.ReadCloser.Read(p)
	t.slot.Add(time.Since(start).Microseconds())
	return n, err
}
