package proxy

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// NewPrimary returns a handler that forwards every request to primaryURL and
// streams the response back to the client.
//
// ponytail: httputil.ReverseProxy already streams bodies, strips hop-by-hop
// headers, rewrites Host and sets X-Forwarded-*. Hand-rolling that is a
// correctness liability, not a simplification.
func NewPrimary(primaryURL string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(primaryURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, errors.New("primary URL must be absolute, e.g. http://127.0.0.1:9000")
	}

	return &httputil.ReverseProxy{
		Transport: PrimaryTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("primary error: %s %s: %v", r.Method, r.URL.Path, err)
			http.Error(w, "primary backend unavailable", http.StatusGatewayTimeout)
		},
	}, nil
}
