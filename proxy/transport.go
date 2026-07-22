package proxy

import (
	"net/http"
	"time"
)

// Primary and shadow traffic use completely separate Transports so a slow shadow
// backend can never starve primary client connections (CLAUDE.md danger zone #3).

var PrimaryTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          1000,
	MaxIdleConnsPerHost:   100,
	IdleConnTimeout:       90 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second, // Primary Timeout: strict, client gets 504.
}

var ShadowTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          200,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       10 * time.Second, // Drop idle connections faster.
	ResponseHeaderTimeout: 5 * time.Second,  // Shadow Timeout: aggressive.
}

// ShadowClient is the only client that may talk to the shadow backend.
var ShadowClient = &http.Client{
	Transport: ShadowTransport,
	Timeout:   5 * time.Second,
	// Never follow redirects on mirrored traffic — the response is discarded anyway.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
