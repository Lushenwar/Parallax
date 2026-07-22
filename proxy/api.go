package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
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
}

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
func APIHandler(shadow *Shadow, allowedOrigin string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if !cors(w, r, allowedOrigin, http.MethodGet) {
			return
		}
		writeJSON(w, http.StatusOK, currentMetrics())
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		if !cors(w, r, allowedOrigin, http.MethodGet, http.MethodPost) {
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
	}
}

// avgMillis converts a cumulative microsecond counter into a mean in
// milliseconds, rounded to two decimals.
//
// ponytail: a running mean over process lifetime, not a windowed one. It tells
// you the steady state, not the last 30 seconds. Add a ring buffer if the
// dashboard ever needs to show a latency spike as it happens.
func avgMillis(totalMicros, count int64) float64 {
	if count <= 0 {
		return 0
	}
	return float64(totalMicros/count) / 1000
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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
