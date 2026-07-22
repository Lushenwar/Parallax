package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCloneForShadowRewritesTargetAndKeepsRequest(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal:9999/base")
	r := httptest.NewRequest(http.MethodPut, "/v1/items?page=2", nil)
	r.Header.Set("X-Request-Id", "abc123")
	r.Header.Set("Content-Type", "application/json")

	clone, err := CloneForShadow(r, target, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}

	if clone.Method != http.MethodPut {
		t.Errorf("method = %q, want PUT", clone.Method)
	}
	if got, want := clone.URL.String(), "http://shadow.internal:9999/base/v1/items?page=2"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
	if clone.Host != "shadow.internal:9999" {
		t.Errorf("Host = %q, want shadow.internal:9999", clone.Host)
	}
	if got := clone.Header.Get("X-Request-Id"); got != "abc123" {
		t.Errorf("end-to-end header lost: X-Request-Id = %q", got)
	}

	body, _ := io.ReadAll(clone.Body)
	if string(body) != `{"a":1}` {
		t.Errorf("body = %q, want %q", body, `{"a":1}`)
	}
	if clone.ContentLength != 7 {
		t.Errorf("ContentLength = %d, want 7", clone.ContentLength)
	}
}

func TestCloneForShadowStripsHopByHopHeaders(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Connection", "Keep-Alive, X-Custom-Hop")
	r.Header.Set("Keep-Alive", "timeout=5")
	r.Header.Set("X-Custom-Hop", "should-not-survive")
	r.Header.Set("Transfer-Encoding", "chunked")
	r.Header.Set("Proxy-Authorization", "Basic xyz")
	r.Header.Set("X-Keep-Me", "yes")

	clone, err := CloneForShadow(r, target, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, h := range []string{"Connection", "Keep-Alive", "X-Custom-Hop", "Transfer-Encoding", "Proxy-Authorization"} {
		if got := clone.Header.Get(h); got != "" {
			t.Errorf("hop-by-hop header %s survived with %q", h, got)
		}
	}
	if got := clone.Header.Get("X-Keep-Me"); got != "yes" {
		t.Errorf("end-to-end header X-Keep-Me = %q, want yes", got)
	}
}

func TestCloneForShadowDoesNotInheritRequestContext(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx, cancel := context.WithCancel(context.Background())
	r = r.WithContext(ctx)
	cancel() // primary exchange finished

	clone, err := CloneForShadow(r, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Context().Err() != nil {
		t.Errorf("clone inherited the cancelled primary context: %v", clone.Context().Err())
	}
}
