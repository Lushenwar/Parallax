package main

import (
	"log"
	"net/http"
	"time"
)

func main() {
	addr := env("LISTEN_ADDR", ":8080")

	srv := &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(handle),
		// ponytail: no WriteTimeout — it would cap slow primary responses mid-stream.
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("parallax listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

func handle(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "no backend configured", http.StatusServiceUnavailable)
}
