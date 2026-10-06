// hello is a tiny API for testing a SynCloud cluster end to end: it reports
// which task answered, counts requests, uses S3 when bound and can burn CPU
// to trigger autoscaling.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

var hits atomic.Int64

func main() {
	host, _ := os.Hostname()
	version := envOr("VERSION", "v1")
	start := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		writeJSON(w, map[string]any{"message": envOr("GREETING", "Hello, world!"), "version": version, "task": host, "hits": n,
			"uptime": time.Since(start).Round(time.Second).String(), "client": r.Header.Get("X-Forwarded-For")})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	// /burn?ms=200 spins the CPU, for autoscaling tests.
	mux.HandleFunc("GET /burn", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		end := time.Now().Add(time.Duration(min(max(ms, 1), 2000)) * time.Millisecond)
		x := 0
		for time.Now().Before(end) {
			x++
		}
		writeJSON(w, map[string]any{"task": host, "spins": x})
	})
	mux.HandleFunc("GET /fail", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "on purpose", http.StatusInternalServerError) })
	addr := ":" + envOr("PORT", "8080")
	log.Printf("hello %s listening on %s", version, addr)
	log.Fatal(http.ListenAndServe(addr, logged(mux)))
}

func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		h.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(t).Round(time.Microsecond))
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
