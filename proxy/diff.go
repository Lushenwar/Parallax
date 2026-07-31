package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxReasons caps how many differences one comparison reports. A shadow backend
// that returns a wholly different document would otherwise produce a reason per
// field, and nobody reads the four-hundredth line.
const maxReasons = 20

// Diff is one recorded disagreement between the two backends.
type Diff struct {
	At            time.Time `json:"at"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	PrimaryStatus int       `json:"primaryStatus"`
	ShadowStatus  int       `json:"shadowStatus"`
	Reasons       []string  `json:"reasons"`
}

// compareResponses reports how the shadow response differs from the primary's.
// An empty result means they agree, to the precision this compares at.
//
// ponytail: status, Content-Type and body only. The rest of the headers are a
// noise farm — Date, Content-Length, Set-Cookie and every hop-by-hop artifact
// differ between two backends for reasons that have nothing to do with a
// regression. Add a configurable header allowlist if a real case needs one.
func compareResponses(primary, shadow *capturedResponse, ignore ignoreList) []string {
	var reasons []string

	if primary.Status != shadow.Status {
		reasons = append(reasons, fmt.Sprintf("status: primary %d, shadow %d", primary.Status, shadow.Status))
	}
	if p, s := mediaType(primary.Header), mediaType(shadow.Header); p != s {
		reasons = append(reasons, fmt.Sprintf("content-type: primary %q, shadow %q", p, s))
	}

	// A truncated side cannot be compared honestly — say so rather than
	// reporting a difference that is really the 1MB cap.
	if primary.Truncated || shadow.Truncated {
		if !bytes.Equal(primary.Body, shadow.Body) {
			reasons = append(reasons, "body: differs within the first 1MB (response truncated for comparison)")
		}
		return reasons
	}

	return append(reasons, compareBodies(primary, shadow, ignore)...)
}

func compareBodies(primary, shadow *capturedResponse, ignore ignoreList) []string {
	if bytes.Equal(primary.Body, shadow.Body) {
		return nil
	}

	// Structural comparison only when both sides really are JSON; otherwise a
	// byte difference is all that can be said.
	var pv, sv any
	if isJSON(primary.Header) && isJSON(shadow.Header) &&
		json.Unmarshal(primary.Body, &pv) == nil && json.Unmarshal(shadow.Body, &sv) == nil {
		reasons := diffJSON("", pv, sv, ignore, nil)
		if len(reasons) == 0 {
			return nil // differed only in whitespace, key order, or ignored paths
		}
		if len(reasons) > maxReasons {
			return append(reasons[:maxReasons], fmt.Sprintf("... and %d more differences", len(reasons)-maxReasons))
		}
		return reasons
	}

	return []string{fmt.Sprintf("body: %d bytes vs %d bytes", len(primary.Body), len(shadow.Body))}
}

// diffJSON walks two decoded JSON values in step, collecting a reason per
// disagreeing leaf. path is dotted, with array indices as segments: "items.0.id".
func diffJSON(path string, primary, shadow any, ignore ignoreList, reasons []string) []string {
	if ignore.matches(path) {
		return reasons
	}

	switch p := primary.(type) {
	case map[string]any:
		s, ok := shadow.(map[string]any)
		if !ok {
			return append(reasons, mismatch(path, primary, shadow))
		}
		for _, k := range sortedKeys(p, s) {
			child := joinPath(path, k)
			if ignore.matches(child) {
				continue
			}
			pv, inPrimary := p[k]
			sv, inShadow := s[k]
			switch {
			case !inShadow:
				reasons = append(reasons, fmt.Sprintf("%s: missing from shadow", child))
			case !inPrimary:
				reasons = append(reasons, fmt.Sprintf("%s: only in shadow", child))
			default:
				reasons = diffJSON(child, pv, sv, ignore, reasons)
			}
		}
		return reasons

	case []any:
		s, ok := shadow.([]any)
		if !ok {
			return append(reasons, mismatch(path, primary, shadow))
		}
		if len(p) != len(s) {
			return append(reasons, fmt.Sprintf("%s: %d items in primary, %d in shadow", label(path), len(p), len(s)))
		}
		for i := range p {
			reasons = diffJSON(joinPath(path, strconv.Itoa(i)), p[i], s[i], ignore, reasons)
		}
		return reasons

	default:
		if primary != shadow {
			return append(reasons, mismatch(path, primary, shadow))
		}
		return reasons
	}
}

// sortedKeys is the union of both objects' keys in a stable order, so the same
// mismatch reads the same way twice.
func sortedKeys(a, b map[string]any) []string {
	seen := make(map[string]bool, len(a)+len(b))
	keys := make([]string, 0, len(a)+len(b))
	for _, m := range []map[string]any{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sortStrings(keys)
	return keys
}

// ponytail: insertion sort. These are the keys of one JSON object, not a data set.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func mismatch(path string, primary, shadow any) string {
	return fmt.Sprintf("%s: primary %s, shadow %s", label(path), render(primary), render(shadow))
}

// render prints a JSON scalar the way it appeared, so 1 and "1" do not look alike.
func render(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func label(path string) string {
	if path == "" {
		return "body"
	}
	return path
}

func joinPath(path, seg string) string {
	if path == "" {
		return seg
	}
	return path + "." + seg
}

func mediaType(h http.Header) string {
	ct, _, _ := strings.Cut(h.Get("Content-Type"), ";")
	return strings.ToLower(strings.TrimSpace(ct))
}

func isJSON(h http.Header) bool {
	// Covers application/json and the +json suffix family (e.g. application/vnd.api+json).
	return strings.HasSuffix(mediaType(h), "json")
}

// ignoreList holds JSON paths whose differences are expected rather than
// interesting: timestamps, generated IDs, anything the two backends are
// entitled to disagree about. A "*" segment matches exactly one path segment,
// so "items.*.id" covers every element's id.
type ignoreList []string

// parseIgnoreList builds an ignore list from comma-separated JSON paths.
func parseIgnoreList(csv string) ignoreList {
	var out ignoreList
	for _, p := range strings.Split(csv, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (l ignoreList) matches(path string) bool {
	if path == "" {
		return false
	}
	for _, pattern := range l {
		if matchPath(pattern, path) {
			return true
		}
	}
	return false
}

func matchPath(pattern, path string) bool {
	p, want := strings.Split(pattern, "."), strings.Split(path, ".")
	if len(p) != len(want) {
		return false
	}
	for i := range p {
		if p[i] != "*" && p[i] != want[i] {
			return false
		}
	}
	return true
}

// DiffStore keeps the most recent mismatches for the dashboard to read.
//
// ponytail: a slice trimmed to a limit, not a true ring buffer. It reallocates
// on trim, which is irrelevant at the rate mismatches actually occur — a proxy
// producing enough diffs for this to matter has a bigger problem than the
// allocator. Swap in a fixed ring if that stops being true.
type DiffStore struct {
	mu    sync.Mutex
	diffs []Diff
	limit int
}

// NewDiffStore returns a store holding at most limit diffs.
func NewDiffStore(limit int) *DiffStore {
	if limit < 1 {
		limit = 1
	}
	return &DiffStore{limit: limit}
}

// Add records a mismatch, evicting the oldest once the store is full.
func (s *DiffStore) Add(d Diff) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diffs = append(s.diffs, d)
	if len(s.diffs) > s.limit {
		s.diffs = s.diffs[len(s.diffs)-s.limit:]
	}
}

// Recent returns up to n diffs, newest first.
func (s *DiffStore) Recent(n int) []Diff {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n <= 0 || n > len(s.diffs) {
		n = len(s.diffs)
	}
	out := make([]Diff, 0, n)
	for i := len(s.diffs) - 1; i >= len(s.diffs)-n; i-- {
		out = append(out, s.diffs[i])
	}
	return out
}
