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

	srv := &http.Server{
		Addr:    addr,
		Handler: primary,
		// ponytail: no WriteTimeout — it would cap slow primary responses mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("parallax listening on %s -> primary %s", addr, primaryURL)
	log.Fatal(srv.ListenAndServe())
}
