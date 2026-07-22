package main

import (
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

	var handler http.Handler = primary
	shadowURL := env("SHADOW_URL", "")
	if shadowURL != "" {
		sampleRate := envFloat("SHADOW_SAMPLE_RATE", 100)
		queueSize := envInt("SHADOW_QUEUE_SIZE", 1024)
		workers := envInt("SHADOW_WORKERS", 64)

		shadow, err := proxy.NewShadow(shadowURL, sampleRate, queueSize, workers)
		if err != nil {
			log.Fatalf("shadow config: %v", err)
		}
		handler = shadow.Middleware(primary)
		log.Printf("mirroring %.1f%% of traffic to shadow %s (queue %d, workers %d)",
			sampleRate, shadowURL, queueSize, workers)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// ponytail: no WriteTimeout — it would cap slow primary responses mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("parallax listening on %s -> primary %s", addr, primaryURL)
	log.Fatal(srv.ListenAndServe())
}
