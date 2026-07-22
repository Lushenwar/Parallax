package proxy

import (
	"net"
	"net/http"
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
