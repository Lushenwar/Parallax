package proxy

import (
	"bytes"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// Hop-by-hop headers (RFC 7230 §6.1) belong to a single connection and must
// never be forwarded by a proxy.
var hopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// CloneForShadow builds a standalone request aimed at target, carrying the
// method, path, query, end-to-end headers and buffered body of r.
//
// The clone deliberately does not inherit r's context: that context is
// cancelled once the primary response is written, and shadow dispatch must
// outlive the primary exchange.
func CloneForShadow(r *http.Request, target *url.URL, body []byte) (*http.Request, error) {
	u := target.JoinPath(r.URL.EscapedPath())
	u.RawQuery = r.URL.RawQuery

	req, err := http.NewRequest(r.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = cloneHeaders(r.Header)
	req.Host = target.Host // rewrite Host to the shadow backend
	req.ContentLength = int64(len(body))
	return req, nil
}

func cloneHeaders(src http.Header) http.Header {
	dst := src.Clone()
	if dst == nil {
		dst = http.Header{}
	}
	// Whatever the Connection header nominates is hop-by-hop too.
	for _, field := range src.Values("Connection") {
		for _, name := range strings.Split(field, ",") {
			if name = textproto.TrimString(name); name != "" {
				dst.Del(name)
			}
		}
	}
	for _, h := range hopHeaders {
		dst.Del(h)
	}
	// ContentLength on the clone is authoritative; a stale header would conflict.
	dst.Del("Content-Length")
	return dst
}
