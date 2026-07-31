// Command backends runs a throwaway primary and shadow backend so you can
// drive the proxy locally without pointing it at anything real.
//
//	go run ./loadtest/backends
//
// Primary listens on :9000, shadow on :9001. Both answer JSON, and the shadow
// deliberately disagrees: it returns a different `total`, the way a regression
// would. Both also return a `requestId` and `servedAt` that legitimately differ
// on every call — run the proxy with
//
//	DIFF_IGNORE=requestId,servedAt
//
// and the comparator reports the seeded regression while staying quiet about
// the nondeterministic fields. Drop DIFF_IGNORE to see why it exists.
package main

import (
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"time"
)

func main() {
	// The shadow's total is wrong on purpose — this is the regression the
	// comparator is supposed to catch.
	go serve(":9000", "PRIMARY", 100)
	serve(":9001", "SHADOW", 99)
}

func serve(addr, name string, total int) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] %s %s shadow=%q", name, r.Method, r.URL.Path, r.Header.Get("X-Shadow-Traffic"))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"path":     r.URL.Path,
			"total":    total,
			"currency": "usd",
			// Nondeterministic on both sides. Without DIFF_IGNORE these bury the
			// real difference — which is the point the ignore list exists to make.
			"requestId": rand.Int64(),
			"servedAt":  time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	log.Printf("[%s] listening on %s (total=%d)", name, addr, total)
	log.Fatal(http.ListenAndServe(addr, http.HandlerFunc(handler)))
}
