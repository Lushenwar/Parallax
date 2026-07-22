// Command backends runs a throwaway primary and shadow backend so you can
// drive the proxy locally without pointing it at anything real.
//
//	go run ./loadtest/backends
//
// Primary listens on :9000, shadow on :9001. Both echo the request and log it,
// so you can watch a mirrored request arrive with X-Shadow-Traffic set.
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
)

func main() {
	go serve(":9000", "PRIMARY")
	serve(":9001", "SHADOW")
}

func serve(addr, name string) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.Printf("[%s] %s %s shadow=%q body=%q", name, r.Method, r.URL.Path, r.Header.Get("X-Shadow-Traffic"), body)
		fmt.Fprintf(w, "%s handled %s %s body=%s", name, r.Method, r.URL.Path, body)
	}
	log.Printf("[%s] listening on %s", name, addr)
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(handler)))
}
