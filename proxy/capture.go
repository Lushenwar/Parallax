package proxy

import (
	"bytes"
	"io"
	"net/http"
)

// MaxCaptureSize caps how much of a response body is held in memory for
// comparison. Responses larger than this are compared up to the cap and marked
// truncated — the alternative is letting a streamed download decide the proxy's
// memory ceiling.
const MaxCaptureSize = 1 << 20 // 1MB

// capturedResponse is one side of a comparison: what a backend answered.
type capturedResponse struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
}

// captureWriter tees the primary response into a capped buffer on its way to
// the client. It changes nothing the client receives: every byte is still
// passed straight through, and Unwrap keeps http.ResponseController able to
// reach the real writer's Flush and Hijack, so streaming and upgrades survive.
type captureWriter struct {
	http.ResponseWriter
	status    int
	buf       bytes.Buffer
	truncated bool
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	switch room := MaxCaptureSize - c.buf.Len(); {
	case room >= len(b):
		c.buf.Write(b)
	case room > 0:
		c.buf.Write(b[:room])
		c.truncated = true
	case len(b) > 0:
		c.truncated = true
	}
	return c.ResponseWriter.Write(b)
}

func (c *captureWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// captured snapshots what the primary answered. Call it after the handler has
// returned; the header map is only final by then.
func (c *captureWriter) captured() *capturedResponse {
	status := c.status
	if status == 0 {
		status = http.StatusOK // handler returned without writing anything
	}
	return &capturedResponse{
		Status:    status,
		Header:    c.Header().Clone(),
		Body:      c.buf.Bytes(),
		Truncated: c.truncated,
	}
}

// captureResponse reads a shadow response for comparison. It always drains the
// body past the cap, because a body left unread is a connection that never
// returns to the pool.
func captureResponse(resp *http.Response) *capturedResponse {
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxCaptureSize))
	if err != nil {
		body = nil
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	return &capturedResponse{
		Status:    resp.StatusCode,
		Header:    resp.Header.Clone(),
		Body:      body,
		Truncated: n > 0,
	}
}
