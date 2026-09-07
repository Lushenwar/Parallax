package proxy

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"log"
	"net/http"
	"strings"
)

// Metrics is the dashboard-facing view of the counters. The expvar names stay
// as they are for scrapers; this is the shape the control plane asked for.
type Metrics struct {
	PrimaryRequestsTotal     int64   `json:"primaryRequestsTotal"`
	ShadowRequestsDispatched int64   `json:"shadowRequestsDispatched"`
	ShadowRequestsDropped    int64   `json:"shadowRequestsDropped"`
	ActiveConnections        int64   `json:"activeConnections"`
	AvgPrimaryLatencyMs      float64 `json:"avgPrimaryLatencyMs"`
	AvgShadowLatencyMs       float64 `json:"avgShadowLatencyMs"`

	// Windowed percentiles over recent traffic. The averages above are lifetime
	// means kept for compatibility; these are the numbers to judge the proxy on.
	// ProxyOverhead is time spent in this process rather than waiting on the
	// primary backend — what Parallax costs to have in the request path.
	PrimaryLatency Latency `json:"primaryLatency"`
	ShadowLatency  Latency `json:"shadowLatency"`
	ProxyOverhead  Latency `json:"proxyOverhead"`
}

// DiffReport is the comparison view: how many responses agreed, how many did
// not, and the most recent disagreements.
//
// Enabled is what lets the dashboard distinguish "comparison is off" from
// "comparison is on and has found nothing" — a distinction worth being loud
// about, since the second is the whole point of the tool and the first looks
// identical from the outside.
type DiffReport struct {
	Enabled    bool   `json:"enabled"`
	Matches    int64  `json:"matches"`
	Mismatches int64  `json:"mismatches"`
	Diffs      []Diff `json:"diffs"`
}

// maxDiffsReturned caps one /api/diffs response. The store holds more; the
// dashboard shows a feed, not an archive.
const maxDiffsReturned = 50

// Config is the live, mutable slice of proxy settings.
type Config struct {
	SampleRate    float64 `json:"sampleRate"`
	MaxBodySizeMB int     `json:"maxBodySizeMB"`
	ShadowEnabled bool    `json:"shadowEnabled"`
}

// configPatch uses pointers so an omitted field means "leave it alone" rather
// than "set it to zero" — the difference between pausing mirroring and
// accidentally setting the sample rate to 0.
type configPatch struct {
	SampleRate    *float64 `json:"sampleRate"`
	ShadowEnabled *bool    `json:"shadowEnabled"`
}

// maxConfigBody caps the control-plane request body. It is a trust boundary,
// however internal the port is meant to be.
const maxConfigBody = 4 << 10

// APIHandler serves the dashboard endpoints: GET /api/metrics and
// GET|POST /api/config. shadow may be nil when the proxy runs without a shadow
// backend; config then reports mirroring off and refuses writes.
//
// allowedOrigin is echoed as Access-Control-Allow-Origin. It is deliberately
// never "*": these endpoints retune a proxy in the live request path, and a
// wildcard would let any page the operator happens to visit do it.
//
// token, when non-empty, is required as "Authorization: Bearer <token>" on
// every endpoint. CORS is not access control — it is a rule browsers agree to
// follow, and curl has never agreed to anything — so without a token anything
// that can open the port can flip the kill switch on live traffic.
func APIHandler(shadow *Shadow, allowedOrigin, token string) http.Handler {
	guard := func(methods ...string) func(http.ResponseWriter, *http.Request) bool {
		return func(w http.ResponseWriter, r *http.Request) bool {
			if !cors(w, r, allowedOrigin, methods...) {
				return false
			}
			return authorized(w, r, token)
		}
	}

	mux := http.NewServeMux()
	readOnly := guard(http.MethodGet)
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !readOnly(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, currentMetrics())
	})
	mux.HandleFunc("/api/diffs", func(w http.ResponseWriter, r *http.Request) {
		if !readOnly(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, currentDiffs(shadow))
	})
	config := guard(http.MethodGet, http.MethodPost)
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if !config(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			patchConfig(w, r, shadow)
			return
		}
		writeJSON(w, http.StatusOK, currentConfig(shadow))
	})
	return mux
}

// authorized checks the bearer token and reports whether handling continues.
// An empty configured token disables the check, which is the local-development
// default; PROXY_API_TOKEN turns it on.
func authorized(w http.ResponseWriter, r *http.Request, token string) bool {
	if token == "" {
		return true
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Constant time so the response time cannot be used to guess the token a
	// character at a time. The length check leaks only the length.
	if ok && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="parallax control plane"`)
	writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
	return false
}

func currentMetrics() Metrics {
	requests := PrimaryRequests.Value()
	dispatched := ShadowDispatched.Value()
	return Metrics{
		PrimaryRequestsTotal:     requests,
		ShadowRequestsDispatched: dispatched,
		ShadowRequestsDropped:    ShadowDropped.Value(),
		ActiveConnections:        PrimaryInFlight.Value(),
		AvgPrimaryLatencyMs:      avgMillis(PrimaryLatencyMicros.Value(), requests),
		AvgShadowLatencyMs:       avgMillis(ShadowLatencyMicros.Value(), dispatched),
		PrimaryLatency:           primaryLatency.Latency(),
		ShadowLatency:            shadowLatency.Latency(),
		ProxyOverhead:            proxyOverhead.Latency(),
	}
}

// avgMillis converts a cumulative microsecond counter into a mean in
// milliseconds, rounded to two decimals.
//
// It is a running mean over process lifetime: the steady state, not the last
// 30 seconds, and blind to the tail by construction. Metrics.PrimaryLatency is
// the windowed percentile view; this stays for callers already reading it.
func avgMillis(totalMicros, count int64) float64 {
	if count <= 0 {
		return 0
	}
	// Convert before dividing — the other order truncates every sub-millisecond
	// mean to a flat 0, which is most of them for a proxy.
	return math.Round(float64(totalMicros)/float64(count)/10) / 100
}

func currentDiffs(shadow *Shadow) DiffReport {
	if shadow == nil || shadow.Diffs() == nil {
		return DiffReport{Diffs: []Diff{}}
	}
	return DiffReport{
		Enabled:    true,
		Matches:    DiffMatches.Value(),
		Mismatches: DiffMismatches.Value(),
		Diffs:      shadow.Diffs().Recent(maxDiffsReturned),
	}
}

func currentConfig(shadow *Shadow) Config {
	c := Config{MaxBodySizeMB: MaxBodySize / (1 << 20)}
	if shadow != nil {
		c.SampleRate = shadow.SampleRate()
		c.ShadowEnabled = shadow.Enabled()
	}
	return c
}

func patchConfig(w http.ResponseWriter, r *http.Request, shadow *Shadow) {
	if shadow == nil {
		writeError(w, http.StatusConflict, "no shadow backend configured; set SHADOW_URL and restart")
		return
	}

	var patch configPatch
	dec := json.NewDecoder(io.LimitReader(r.Body, maxConfigBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if patch.SampleRate != nil {
		if err := validSampleRate(*patch.SampleRate); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Validate everything before mutating anything, so a bad field cannot leave
	// the proxy half-reconfigured.
	if patch.SampleRate != nil {
		shadow.SetSampleRate(*patch.SampleRate)
	}
	if patch.ShadowEnabled != nil {
		shadow.SetEnabled(*patch.ShadowEnabled)
	}
	log.Printf("config updated: sampleRate=%.2f%% shadowEnabled=%t", shadow.SampleRate(), shadow.Enabled())

	writeJSON(w, http.StatusOK, currentConfig(shadow))
}

// cors applies the CORS headers, answers preflight, and enforces the method
// allowlist. It reports whether the caller should continue handling.
func cors(w http.ResponseWriter, r *http.Request, allowedOrigin string, methods ...string) bool {
	if allowedOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Methods", join(methods)+", OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Add("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", join(methods))
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func join(methods []string) string {
	out := ""
	for i, m := range methods {
		if i > 0 {
			out += ", "
		}
		out += m
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, http.ErrHandlerTimeout) {
		log.Printf("api: writing response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
