// Package serverless is the one public entry into the API for Vercel.
//
// Vercel compiles api/index.go under its own module name, and Go's internal/
// rule then forbids that file from importing jobscraper/internal/... directly.
// This package lives inside the jobscraper module, so it may import internal/api,
// and api/index.go only has to import this package.
package serverless

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
