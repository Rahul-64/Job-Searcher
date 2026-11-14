// Package handler is the Vercel serverless entry point: one function that serves
// every /api/* route (see vercel.json for the rewrite). The real work lives in
// internal/api, shared with the local dev server (cmd/serve).
package handler

import (
	"context"
	"log"
	"net/http"
	"sync"

	"jobscraper/internal/api"
)

var (
	once    sync.Once
	server  *api.Server
	initErr error
)

// Handler serves API requests. The database pool is created once per warm
// instance and reused across invocations.
func Handler(w http.ResponseWriter, r *http.Request) {
	once.Do(func() {
		server, _, initErr = api.NewFromEnv(context.Background())
	})
	if initErr != nil {
		log.Printf("api init failed: %v", initErr) // details stay in the logs
		http.Error(w, `{"error":"service unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	server.ServeHTTP(w, r)
}
