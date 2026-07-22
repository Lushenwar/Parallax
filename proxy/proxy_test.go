package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrimaryForwardsRequestAndResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Backend", "primary")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, r.Method+" "+r.URL.Path+" "+string(b))
	}))
	defer backend.Close()

	h, err := NewPrimary(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Post(front.URL+"/hello", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Backend"); got != "primary" {
		t.Errorf("X-Backend = %q, want %q", got, "primary")
	}
	if got, want := string(body), "POST /hello payload"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPrimaryDownReturns504(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := backend.URL
	backend.Close() // nothing is listening on dead anymore

	h, err := NewPrimary(dead)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := http.Get(front.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
}

func TestNewPrimaryRejectsRelativeURL(t *testing.T) {
	if _, err := NewPrimary("/not-absolute"); err == nil {
		t.Error("expected error for non-absolute primary URL")
	}
}
