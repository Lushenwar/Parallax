package proxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

// ErrPayloadTooLarge means the body exceeded MaxBodySize and must not be cloned.
// The request is still fully forwardable to the primary backend.
var ErrPayloadTooLarge = errors.New("request payload exceeds maximum shadow size limit")

// MaxBodySize caps how much of a request body is held in memory for cloning.
// Anything larger streams to the primary and is never mirrored (danger zone #2).
const MaxBodySize = 10 * 1024 * 1024 // 10MB

// BufferBody reads up to MaxBodySize of r.Body for shadow cloning and always
// leaves r.Body fully readable by the primary backend — you cannot read a TCP
// socket twice (danger zone #5), so consumed bytes are spliced back in front.
//
// Returns (nil, ErrPayloadTooLarge) when the body is too big to mirror; the
// caller should forward to the primary and skip shadowing.
func BufferBody(r *http.Request) ([]byte, error) {
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	orig := r.Body

	// Read one byte past the limit so "exactly MaxBodySize" is still cloneable.
	buf, err := io.ReadAll(io.LimitReader(orig, MaxBodySize+1))
	if err != nil {
		r.Body = splice(buf, orig)
		return nil, err
	}
	if int64(len(buf)) > MaxBodySize {
		r.Body = splice(buf, orig)
		return nil, ErrPayloadTooLarge
	}

	// Whole body is in memory; the original is drained.
	r.Body = io.NopCloser(bytes.NewReader(buf))
	return buf, nil
}

// splice re-fronts the already-read bytes onto the unread remainder, keeping the
// original Closer so the transport can release the connection.
func splice(read []byte, rest io.ReadCloser) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(read), rest), rest}
}
