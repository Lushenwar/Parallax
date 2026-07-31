package main

import (
	"context"
	"errors"
	"expvar"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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

		// Safe methods only unless the operator says otherwise — a mirrored
		// write hits the shadow backend for real. "*" mirrors everything.
		methods := env("SHADOW_METHODS", "GET,HEAD")
		if err := shadow.SetMethods(strings.Split(methods, ",")); err != nil {
			log.Fatalf("SHADOW_METHODS: %v", err)
		}

		// Comparison is the reason the mirror exists, so it is on unless the
		// operator turns it off with DIFF_BUFFER=0.
		if diffBuffer := envInt("DIFF_BUFFER", 100); diffBuffer > 0 {
			ignore := env("DIFF_IGNORE", "")
			shadow.EnableDiffs(diffBuffer, ignore)
			log.Printf("comparing responses (keeping %d mismatches, ignoring %q)", diffBuffer, ignore)
		} else {
			log.Print("response comparison disabled (DIFF_BUFFER=0); shadow responses are discarded")
		}

		handler = shadow.Middleware(primary)
		log.Printf("mirroring %.1f%% of [%s] traffic to shadow %s (queue %d, workers %d)",
			sampleRate, methods, shadowURL, queueSize, workers)
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

	// This process sits in front of a live backend, so exiting the moment the
	// signal lands would drop client requests mid-flight every time the proxy
	// itself is redeployed. Drain instead.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("parallax listening on %s -> primary %s", addr, primaryURL)

	<-ctx.Done()
	stop() // A second signal now kills the process rather than being swallowed.
	log.Printf("shutdown: draining in-flight requests (%s)", shutdownGrace)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	// ponytail: queued mirrors are still dropped here, deliberately. They are
	// fire-and-forget by design, and draining them would make shutdown of the
	// primary path wait on the shadow backend.
	log.Print("shutdown: complete")
}

// shutdownGrace bounds the drain so a hung backend cannot block the exit.
const shutdownGrace = 15 * time.Second
