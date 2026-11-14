// Package api is the read-only HTTP API over the job database. The same handler
// serves the local dev server (cmd/serve) and the Vercel function (api/index.go).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"jobscraper/internal/config"
	"jobscraper/internal/store"
)

// Store is what the API needs from the database layer.
type Store interface {
	Ping(ctx context.Context) error
	SearchJobs(ctx context.Context, p store.SearchParams) (store.SearchResult, error)
	GetJob(ctx context.Context, id int64) (*store.JobDetail, error)
	Facets(ctx context.Context) (store.Facets, error)
	PublicStats(ctx context.Context) (store.PublicStats, error)
	SourceHealth(ctx context.Context) ([]store.SourceHealth, error)
}

const (
	// Data changes once a day. Browsers may reuse a response for a minute; shared
	// (CDN) caches for five, and may serve it stale for ten more while refreshing.
	// Stating max-age explicitly matters: without it a browser can show a response
	// up to the stale-while-revalidate window old on a repeat visit.
	cacheControl = "public, max-age=60, s-maxage=300, stale-while-revalidate=600"
	requestLimit = 8 * time.Second
	aggregateTTL = 5 * time.Minute
)

// Server routes API requests. It is safe for concurrent use.
type Server struct {
	st  Store
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	val     any
	expires time.Time
}

// New builds a Server around any Store.
func New(st Store, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Server{st: st, log: log, now: time.Now, cache: map[string]cacheEntry{}}
}

// NewFromEnv opens the database from DATABASE_URL (or .env) with a small pool
// suited to serverless use, and returns the server and a close function.
func NewFromEnv(ctx context.Context) (*Server, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	url, err := cfg.RequireDatabaseURL()
	if err != nil {
		return nil, nil, err
	}
	maxConns := int32(3) // each serverless instance holds few connections
	if v := os.Getenv("API_MAX_CONNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 20 {
			maxConns = int32(n)
		}
	}
	pool, err := store.OpenWithMaxConns(ctx, url, maxConns)
	if err != nil {
		return nil, nil, err
	}
	return New(store.New(pool), nil), pool.Close, nil
}

// routePath works out which API route a request is for. Behind Vercel's
// rewrite the function is reached as /api/index?_route=jobs&..., so the route
// travels in a query parameter; everywhere else it is the URL path.
func routePath(r *http.Request) string {
	if rt := r.URL.Query().Get("_route"); rt != "" {
		return "/" + strings.Trim(rt, "/")
	}
	p := strings.TrimPrefix(r.URL.Path, "/api")
	if p == "" || p == "/index" {
		return "/"
	}
	return "/" + strings.Trim(p, "/")
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("panic in handler", "panic", rec, "path", r.URL.Path)
			writeError(w, http.StatusInternalServerError, "internal error")
		}
	}()

	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "this API is read-only")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestLimit)
	defer cancel()
	r = r.WithContext(ctx)

	switch routePath(r) {
	case "/health":
		s.health(w, r)
	case "/jobs":
		s.jobs(w, r)
	case "/job":
		s.job(w, r)
	case "/facets":
		s.facets(w, r)
	case "/stats":
		s.stats(w, r)
	case "/sources":
		s.sources(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown endpoint")
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := s.st.Ping(r.Context()); err != nil {
		s.log.Error("health check failed", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded"}, "no-store")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"}, "no-store")
}

type searchResponse struct {
	store.SearchResult
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	p, err := parseSearch(r.URL.Query())
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.st.SearchJobs(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, searchResponse{SearchResult: res, Limit: p.Limit, Offset: p.Offset}, cacheControl)
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		s.fail(w, badParam("id must be a positive whole number"))
		return
	}
	d, err := s.st.GetJob(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d, cacheControl)
}

func (s *Server) facets(w http.ResponseWriter, r *http.Request) {
	v, err := s.cached("facets", func() (any, error) { return s.st.Facets(r.Context()) })
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v, cacheControl)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	v, err := s.cached("stats", func() (any, error) { return s.st.PublicStats(r.Context()) })
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v, cacheControl)
}

func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	v, err := s.cached("sources", func() (any, error) { return s.st.SourceHealth(r.Context()) })
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v, cacheControl)
}

// cached returns a fresh-enough value for key, computing it at most once per TTL.
func (s *Server) cached(key string, compute func() (any, error)) (any, error) {
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && s.now().Before(e.expires) {
		s.mu.Unlock()
		return e.val, nil
	}
	s.mu.Unlock()

	v, err := compute()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = cacheEntry{val: v, expires: s.now().Add(aggregateTTL)}
	s.mu.Unlock()
	return v, nil
}

// fail maps an error to a response. Client mistakes and missing rows are
// reported as such; anything else is logged and returned as a generic 500, so
// database details never reach the caller.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var pe *paramError
	switch {
	case errors.As(err, &pe):
		writeError(w, http.StatusBadRequest, pe.msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "job not found")
	case errors.Is(err, context.DeadlineExceeded):
		s.log.Warn("request timed out")
		writeError(w, http.StatusGatewayTimeout, "the database took too long to answer, try again")
	default:
		s.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg}, "no-store")
}

// writeJSON marshals first, so an encoding failure becomes a clean 500 instead
// of a half-written body.
func writeJSON(w http.ResponseWriter, status int, v any, cache string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	if cache != "" {
		h.Set("Cache-Control", cache)
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
