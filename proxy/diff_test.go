package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jsonResp(status int, body string) *capturedResponse {
	return &capturedResponse{
		Status: status,
		Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:   []byte(body),
	}
}

func TestCompareResponses(t *testing.T) {
	tests := []struct {
		name           string
		primary        *capturedResponse
		shadow         *capturedResponse
		ignore         string
		wantMatch      bool
		wantReasonLike string
	}{
		{
			name:      "identical",
			primary:   jsonResp(200, `{"id":1,"name":"ada"}`),
			shadow:    jsonResp(200, `{"id":1,"name":"ada"}`),
			wantMatch: true,
		},
		{
			name:      "key order and whitespace are not differences",
			primary:   jsonResp(200, `{"id":1,"name":"ada"}`),
			shadow:    jsonResp(200, "{\n  \"name\": \"ada\",\n  \"id\": 1\n}"),
			wantMatch: true,
		},
		{
			name:           "status regression",
			primary:        jsonResp(200, `{"ok":true}`),
			shadow:         jsonResp(500, `{"ok":true}`),
			wantReasonLike: "status: primary 200, shadow 500",
		},
		{
			name:           "changed scalar",
			primary:        jsonResp(200, `{"total":100}`),
			shadow:         jsonResp(200, `{"total":99}`),
			wantReasonLike: "total: primary 100, shadow 99",
		},
		{
			name:           "type change is not equality",
			primary:        jsonResp(200, `{"id":1}`),
			shadow:         jsonResp(200, `{"id":"1"}`),
			wantReasonLike: `id: primary 1, shadow "1"`,
		},
		{
			name:           "dropped field",
			primary:        jsonResp(200, `{"id":1,"email":"a@b.c"}`),
			shadow:         jsonResp(200, `{"id":1}`),
			wantReasonLike: "email: missing from shadow",
		},
		{
			name:           "added field",
			primary:        jsonResp(200, `{"id":1}`),
			shadow:         jsonResp(200, `{"id":1,"debug":true}`),
			wantReasonLike: "debug: only in shadow",
		},
		{
			name:           "nested path is reported in full",
			primary:        jsonResp(200, `{"user":{"plan":{"tier":"pro"}}}`),
			shadow:         jsonResp(200, `{"user":{"plan":{"tier":"free"}}}`),
			wantReasonLike: `user.plan.tier: primary "pro", shadow "free"`,
		},
		{
			name:           "array length",
			primary:        jsonResp(200, `{"items":[1,2,3]}`),
			shadow:         jsonResp(200, `{"items":[1,2]}`),
			wantReasonLike: "items: 3 items in primary, 2 in shadow",
		},
		{
			name:           "array element by index",
			primary:        jsonResp(200, `{"items":[{"qty":1}]}`),
			shadow:         jsonResp(200, `{"items":[{"qty":2}]}`),
			wantReasonLike: "items.0.qty: primary 1, shadow 2",
		},
		{
			name:      "ignored timestamp",
			primary:   jsonResp(200, `{"id":1,"createdAt":"2026-01-01T00:00:00Z"}`),
			shadow:    jsonResp(200, `{"id":1,"createdAt":"2026-07-30T12:00:00Z"}`),
			ignore:    "createdAt",
			wantMatch: true,
		},
		{
			name:      "wildcard ignores every element's id",
			primary:   jsonResp(200, `{"items":[{"id":"a","qty":1},{"id":"b","qty":2}]}`),
			shadow:    jsonResp(200, `{"items":[{"id":"x","qty":1},{"id":"y","qty":2}]}`),
			ignore:    "items.*.id",
			wantMatch: true,
		},
		{
			name:           "ignoring a nondeterministic field still catches a real one",
			primary:        jsonResp(200, `{"createdAt":"then","total":100}`),
			shadow:         jsonResp(200, `{"createdAt":"now","total":99}`),
			ignore:         "createdAt",
			wantReasonLike: "total: primary 100, shadow 99",
		},
		{
			name:           "content type change",
			primary:        jsonResp(200, `{"a":1}`),
			shadow:         &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: []byte(`{"a":1}`)},
			wantReasonLike: "content-type:",
		},
		{
			name:           "non-JSON bodies compare by size",
			primary:        &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: []byte("hello")},
			shadow:         &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: []byte("goodbye")},
			wantReasonLike: "body: 5 bytes vs 7 bytes",
		},
		{
			name:      "identical non-JSON bodies match",
			primary:   &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: []byte("hello")},
			shadow:    &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: []byte("hello")},
			wantMatch: true,
		},
		{
			name:           "truncation is admitted, not reported as a field diff",
			primary:        &capturedResponse{Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"a":1}`), Truncated: true},
			shadow:         jsonResp(200, `{"a":2}`),
			wantReasonLike: "truncated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareResponses(tt.primary, tt.shadow, parseIgnoreList(tt.ignore))

			if tt.wantMatch {
				if len(got) != 0 {
					t.Fatalf("expected a match, got %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("expected a mismatch containing %q, got a match", tt.wantReasonLike)
			}
			if !strings.Contains(strings.Join(got, "\n"), tt.wantReasonLike) {
				t.Errorf("reasons %v\ndo not contain %q", got, tt.wantReasonLike)
			}
		})
	}
}

// A backend returning a wholly different document must not produce one reason
// per field — the feed has to stay readable.
func TestDiffReasonsAreCapped(t *testing.T) {
	var primary, shadow strings.Builder
	primary.WriteString(`{`)
	shadow.WriteString(`{`)
	for i := 0; i < maxReasons*3; i++ {
		if i > 0 {
			primary.WriteString(",")
			shadow.WriteString(",")
		}
		primary.WriteString(`"f` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":1`)
		shadow.WriteString(`"f` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `":2`)
	}
	primary.WriteString(`}`)
	shadow.WriteString(`}`)

	got := compareResponses(jsonResp(200, primary.String()), jsonResp(200, shadow.String()), nil)
	if len(got) != maxReasons+1 {
		t.Fatalf("got %d reasons, want %d plus a summary line", len(got), maxReasons)
	}
	if !strings.Contains(got[len(got)-1], "more differences") {
		t.Errorf("last reason should summarise the remainder, got %q", got[len(got)-1])
	}
}

// TestComparisonEndToEnd is the one that proves the product works: two backends
// that disagree, driven through the real middleware, must produce a recorded
// mismatch naming the field — while the client still gets the primary's answer
// untouched.
func TestComparisonEndToEnd(t *testing.T) {
	json := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}
	}

	primaryBackend := httptest.NewServer(json(`{"total":100,"currency":"usd","servedAt":"early"}`))
	defer primaryBackend.Close()
	shadowBackend := httptest.NewServer(json(`{"total":99,"currency":"usd","servedAt":"late"}`))
	defer shadowBackend.Close()

	primary, err := NewPrimary(primaryBackend.URL)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewShadow(shadowBackend.URL, 100, 16, 4)
	if err != nil {
		t.Fatal(err)
	}
	shadow.EnableDiffs(10, "servedAt") // servedAt is expected to differ

	front := httptest.NewServer(shadow.Middleware(primary))
	defer front.Close()

	resp, err := http.Get(front.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// The client must see the primary's response, unmodified by the capture.
	if got := string(body); got != `{"total":100,"currency":"usd","servedAt":"early"}` {
		t.Errorf("client got %q — capturing the response altered what was served", got)
	}

	diff := awaitDiff(t, shadow)
	if diff.Path != "/checkout" || diff.Method != http.MethodGet {
		t.Errorf("diff recorded for %s %s, want GET /checkout", diff.Method, diff.Path)
	}
	joined := strings.Join(diff.Reasons, "\n")
	if !strings.Contains(joined, "total: primary 100, shadow 99") {
		t.Errorf("reasons %v do not name the changed field", diff.Reasons)
	}
	if strings.Contains(joined, "servedAt") {
		t.Errorf("ignored path leaked into reasons: %v", diff.Reasons)
	}
	if strings.Contains(joined, "currency") {
		t.Errorf("matching field reported as a difference: %v", diff.Reasons)
	}
}

// TestIdenticalBackendsProduceNoDiff is the other half: agreement must be
// counted as a match, not filed as a mismatch. Without this, a comparator that
// flags everything would still pass the test above.
func TestIdenticalBackendsProduceNoDiff(t *testing.T) {
	same := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"total":100}`))
	}
	primaryBackend := httptest.NewServer(http.HandlerFunc(same))
	defer primaryBackend.Close()
	shadowBackend := httptest.NewServer(http.HandlerFunc(same))
	defer shadowBackend.Close()

	primary, err := NewPrimary(primaryBackend.URL)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := NewShadow(shadowBackend.URL, 100, 16, 4)
	if err != nil {
		t.Fatal(err)
	}
	shadow.EnableDiffs(10, "")

	front := httptest.NewServer(shadow.Middleware(primary))
	defer front.Close()

	before := DiffMatches.Value()
	resp, err := http.Get(front.URL + "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for DiffMatches.Value() == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if DiffMatches.Value() == before {
		t.Fatal("identical responses were never counted as a match")
	}
	if got := shadow.Diffs().Recent(0); len(got) != 0 {
		t.Errorf("identical responses recorded %d mismatches: %v", len(got), got)
	}
}

// awaitDiff waits for the worker pool to record a mismatch. Comparison is
// asynchronous by design — it happens after the client has already been served.
func awaitDiff(t *testing.T, s *Shadow) Diff {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.Diffs().Recent(1); len(got) == 1 {
			return got[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no diff recorded within 2s")
	return Diff{}
}

func TestDiffStoreKeepsNewestWithinLimit(t *testing.T) {
	s := NewDiffStore(3)
	for i := 1; i <= 5; i++ {
		s.Add(Diff{At: time.Now(), Path: "/" + string(rune('0'+i))})
	}

	got := s.Recent(0)
	if len(got) != 3 {
		t.Fatalf("got %d diffs, want the 3 most recent", len(got))
	}
	// Newest first: /5, /4, /3.
	for i, want := range []string{"/5", "/4", "/3"} {
		if got[i].Path != want {
			t.Errorf("position %d is %q, want %q", i, got[i].Path, want)
		}
	}
	if n := len(s.Recent(2)); n != 2 {
		t.Errorf("Recent(2) returned %d", n)
	}
	if got := NewDiffStore(3).Recent(0); len(got) != 0 {
		t.Errorf("empty store returned %d diffs", len(got))
	}
}
