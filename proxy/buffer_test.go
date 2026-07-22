package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

// closeTracker lets a test assert the original body's Closer survives splicing.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return nil }

func newRequest(body io.ReadCloser) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.Body = body
	return r
}

func TestBufferBodySmallBodyIsClonedAndStillReadable(t *testing.T) {
	r := newRequest(io.NopCloser(strings.NewReader("hello world")))

	buf, err := BufferBody(r)
	if err != nil {
		t.Fatalf("BufferBody: %v", err)
	}
	if string(buf) != "hello world" {
		t.Errorf("buffered = %q, want %q", buf, "hello world")
	}

	rest, _ := io.ReadAll(r.Body)
	if string(rest) != "hello world" {
		t.Errorf("primary would read %q, want %q", rest, "hello world")
	}
}

func TestBufferBodyReassemblesChunkedReads(t *testing.T) {
	const payload = "chunked payload arriving one byte at a time"
	r := newRequest(io.NopCloser(iotest.OneByteReader(strings.NewReader(payload))))

	buf, err := BufferBody(r)
	if err != nil {
		t.Fatalf("BufferBody: %v", err)
	}
	if string(buf) != payload {
		t.Errorf("buffered = %q, want %q", buf, payload)
	}
}

func TestBufferBodyExactlyAtLimitIsCloneable(t *testing.T) {
	body := bytes.Repeat([]byte("a"), MaxBodySize)
	r := newRequest(io.NopCloser(bytes.NewReader(body)))

	buf, err := BufferBody(r)
	if err != nil {
		t.Fatalf("BufferBody at exactly MaxBodySize: %v", err)
	}
	if len(buf) != MaxBodySize {
		t.Errorf("buffered %d bytes, want %d", len(buf), MaxBodySize)
	}
}

func TestBufferBodyOverLimitAbortsCloneButPreservesStream(t *testing.T) {
	body := bytes.Repeat([]byte("b"), MaxBodySize+1024)
	tracker := &closeTracker{Reader: bytes.NewReader(body)}
	r := newRequest(tracker)

	buf, err := BufferBody(r)
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
	if buf != nil {
		t.Errorf("buffered %d bytes, want nil (no clone above the limit)", len(buf))
	}

	// The primary must still receive every byte, prefix included.
	forwarded, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading spliced body: %v", err)
	}
	if !bytes.Equal(forwarded, body) {
		t.Errorf("primary would read %d bytes, want %d identical bytes", len(forwarded), len(body))
	}

	if err := r.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !tracker.closed {
		t.Error("spliced body dropped the original Closer")
	}
}

func TestBufferBodyNoBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	buf, err := BufferBody(r)
	if err != nil || buf != nil {
		t.Errorf("BufferBody(no body) = %v, %v; want nil, nil", buf, err)
	}
}
