// Package handler is the Vercel serverless entry point: one function that serves
// every /api/* route (see vercel.json for the rewrite). The real work lives in
// internal/api, reached through the serverless package because Vercel compiles
// this file outside the jobscraper module path and Go's internal/ rule would
// otherwise reject the import.
package handler

import (
	"net/http"

	"jobscraper/serverless"
)

// Handler serves API requests.
func Handler(w http.ResponseWriter, r *http.Request) {
	serverless.Handler(w, r)
}
