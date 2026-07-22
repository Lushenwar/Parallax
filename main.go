package main

import (
	"expvar"
	"log"
	"net/http"
	"time"

	"github.com/Lushenwar/Parallax/proxy"
)

func main() {
	addr := env("LISTEN_ADDR", ":8080")

	primaryURL := env("PRIMARY_URL", "")
	if primaryURL == "" {
		log.Fatal("PRIMARY_URL is required (e.g. http://127.0.0.1:9000)")
	}
	primary, err := proxy.NewPrimary(primaryURL)
	if err != nil {
		log.Fatalf("PRIMARY_URL: %v", err)
	}

	var shadow *proxy.Shadow
	var handler http.Handler = primary
	if shadowURL := env("SHADOW_URL", ""); shadowURL != "" {
		sampleRate := envFloat("SHADOW_SAMPLE_RATE", 100)
		queueSize := envInt("SHADOW_QUEUE_SIZE", 1024)
		workers := envInt("SHADOW_WORKERS", 64)

		shadow, err = proxy.NewShadow(shadowURL, sampleRate, queueSize, workers)
		if err != nil {
			log.Fatalf("shadow config: %v", err)
		}
		expvar.Publish("shadow_queue_depth", expvar.Func(func() any { return shadow.QueueDepth() }))

		handler = shadow.Middleware(primary)
		log.Printf("mirroring %.1f%% of traffic to shadow %s (queue %d, workers %d)",
			sampleRate, shadowURL, queueSize, workers)
	}
	handler = proxy.Instrument(handler)

	// ponytail: ServeMux is enough routing for a couple of carve-outs. Set
	// METRICS_PATH to "" if a real route collides with it.
	mux := http.NewServeMux()
	dashboardOrigin := env("DASHBOARD_ORIGIN", "http://localhost:3000")
	mux.Handle("/api/", proxy.APIHandler(shadow, dashboardOrigin))
	log.Printf("control plane on /api/ (CORS origin %s)", dashboardOrigin)

	if metricsPath := env("METRICS_PATH", "/metrics"); metricsPath != "" {
		mux.Handle(metricsPath, proxy.MetricsHandler())
		log.Printf("metrics on %s", metricsPath)
	}
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// ponytail: no WriteTimeout — it would cap slow primary responses mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("parallax listening on %s -> primary %s", addr, primaryURL)
	log.Fatal(srv.ListenAndServe())
}
