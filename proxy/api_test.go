package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testOrigin = "http://localhost:3000"

func apiServer(t *testing.T, shadow *Shadow) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(APIHandler(shadow, testOrigin))
	t.Cleanup(srv.Close)
	return srv
}

func TestMetricsEndpointShape(t *testing.T) {
	srv := apiServer(t, nil)

	PrimaryRequests.Add(10)
	PrimaryLatencyMicros.Add(10 * 14_200) // 14.2ms each

	resp, err := http.Get(srv.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if origin := resp.Header.Get("Access-Control-Allow-Origin"); origin != testOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", origin, testOrigin)
	}

	var m Metrics
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.PrimaryRequestsTotal < 10 {
		t.Errorf("primaryRequestsTotal = %d, want at least 10", m.PrimaryRequestsTotal)
	}
	if m.AvgPrimaryLatencyMs <= 0 {
		t.Errorf("avgPrimaryLatencyMs = %v, want a positive mean", m.AvgPrimaryLatencyMs)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal")
	shadow := newTestShadow(target, 50, 4)
	srv := apiServer(t, shadow)

	resp, err := http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()

	if got.SampleRate != 50 || !got.ShadowEnabled || got.MaxBodySizeMB != 10 {
		t.Errorf("GET /api/config = %+v, want {50 10 true}", got)
	}

	resp, err = http.Post(srv.URL+"/api/config", "application/json", strings.NewReader(`{"sampleRate":12.5}`))
	if err != nil {
		t.Fatal(err)
	}
	var updated Config
	json.NewDecoder(resp.Body).Decode(&updated)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d, want 200", resp.StatusCode)
	}
	if updated.SampleRate != 12.5 {
		t.Errorf("sampleRate = %v, want 12.5", updated.SampleRate)
	}
	if shadow.SampleRate() != 12.5 {
		t.Errorf("live proxy sample rate = %v, want 12.5", shadow.SampleRate())
	}
	// shadowEnabled was omitted, so it must be untouched.
	if !updated.ShadowEnabled {
		t.Error("omitted shadowEnabled was zeroed instead of left alone")
	}
}

func TestConfigRejectsBadInput(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal")
	shadow := newTestShadow(target, 50, 4)
	srv := apiServer(t, shadow)

	for _, body := range []string{
		`{"sampleRate":101}`,
		`{"sampleRate":-1}`,
		`{"sampleRate":"half"}`,
		`{"sampleRate":50,"dropTables":true}`,
		`not json`,
	} {
		resp, err := http.Post(srv.URL+"/api/config", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s => %d, want 400", body, resp.StatusCode)
		}
	}

	if shadow.SampleRate() != 50 {
		t.Errorf("a rejected request still mutated the proxy: sample rate = %v", shadow.SampleRate())
	}
}

func TestConfigWriteRefusedWithoutShadowBackend(t *testing.T) {
	srv := apiServer(t, nil)

	resp, err := http.Post(srv.URL+"/api/config", "application/json", strings.NewReader(`{"sampleRate":10}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
}

func TestDisablingShadowStopsSampling(t *testing.T) {
	target, _ := url.Parse("http://shadow.internal")
	shadow := newTestShadow(target, 100, 4)

	if !shadow.sampled() {
		t.Fatal("expected sampling at 100%")
	}
	shadow.SetEnabled(false)
	if shadow.sampled() {
		t.Error("sampling continued after the shadow was disabled")
	}
}

func TestPreflightAndMethodGuard(t *testing.T) {
	srv := apiServer(t, nil)

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/metrics", strings.NewReader("{}"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/metrics = %d, want 405", resp.StatusCode)
	}
}
