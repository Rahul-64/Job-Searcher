package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jobscraper/internal/store"
)

// fakeStore records calls and returns canned data.
type fakeStore struct {
	lastSearch store.SearchParams
	searchErr  error
	jobErr     error
	pingErr    error
	facetCalls int32
	panicOn    string
}

func (f *fakeStore) Ping(ctx context.Context) error { return f.pingErr }
func (f *fakeStore) SearchJobs(ctx context.Context, p store.SearchParams) (store.SearchResult, error) {
	f.lastSearch = p
	if f.panicOn == "search" {
		panic("boom")
	}
	if f.searchErr != nil {
		return store.SearchResult{}, f.searchErr
	}
	city := "Bengaluru"
	return store.SearchResult{Total: 1, Jobs: []store.JobSummary{{ID: 7, Title: "Go <script>alert(1)</script> Engineer",
		Company: store.CompanyRef{Name: "Okta", Slug: "okta"}, City: &city}}}, nil
}
func (f *fakeStore) GetJob(ctx context.Context, id int64) (*store.JobDetail, error) {
	if f.jobErr != nil {
		return nil, f.jobErr
	}
	return &store.JobDetail{JobSummary: store.JobSummary{ID: id, Title: "T"}, Description: "d"}, nil
}
func (f *fakeStore) Facets(ctx context.Context) (store.Facets, error) {
	atomic.AddInt32(&f.facetCalls, 1)
	return store.Facets{Cities: []store.FacetValue{{Value: "Pune", Count: 3}}}, nil
}
func (f *fakeStore) PublicStats(ctx context.Context) (store.PublicStats, error) {
	return store.PublicStats{OpenJobs: 10}, nil
}
func (f *fakeStore) SourceHealth(ctx context.Context) ([]store.SourceHealth, error) {
	return []store.SourceHealth{{Company: "Okta", Kind: "greenhouse", Enabled: true}}, nil
}

func newTestServer(f *fakeStore) *Server {
	return New(f, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(s *Server, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func errMsg(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}
	return e["error"]
}

func TestJobsHappyPath(t *testing.T) {
	f := &fakeStore{}
	rec := do(newTestServer(f), "GET", "/api/jobs?q=go+backend&city=blr&remote=Hybrid&seniority=senior&exp=5&limit=10&offset=20&has_salary=1&posted_within=7&company=okta&country=in")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	p := f.lastSearch
	if p.Q != "go backend" || p.City != "blr" || p.Remote != "hybrid" || p.Seniority != "senior" ||
		p.Exp == nil || *p.Exp != 5 || p.Limit != 10 || p.Offset != 20 || !p.HasSalary ||
		p.PostedWithinDays != 7 || p.Company != "okta" || p.Country != "IN" {
		t.Errorf("params not parsed correctly: %+v", p)
	}
	if p.Sort != store.SortRelevance {
		t.Errorf("a text search should rank by relevance by default, got %q", p.Sort)
	}
	var body struct {
		Total, Limit, Offset int
		Jobs                 []store.JobSummary
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || body.Limit != 10 || body.Offset != 20 || len(body.Jobs) != 1 {
		t.Errorf("body: %+v", body)
	}
	h := rec.Header()
	if !strings.Contains(h.Get("Cache-Control"), "s-maxage=300") ||
		h.Get("Content-Type") != "application/json; charset=utf-8" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", h)
	}
	// Titles containing markup are data, escaped in the JSON, never interpreted.
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Error("angle brackets in data must be JSON-escaped")
	}
}

func TestJobsEntryLevelParameter(t *testing.T) {
	for query, want := range map[string]bool{"": false, "entry_level=1": true, "entry_level=true": true, "entry_level=0": false} {
		f := &fakeStore{}
		if rec := do(newTestServer(f), "GET", "/api/jobs?"+query); rec.Code != 200 {
			t.Fatalf("%q: status %d", query, rec.Code)
		}
		if f.lastSearch.EntryLevel != want {
			t.Errorf("%q: EntryLevel = %v, want %v", query, f.lastSearch.EntryLevel, want)
		}
	}
}

func TestJobsDefaults(t *testing.T) {
	f := &fakeStore{}
	do(newTestServer(f), "GET", "/api/jobs")
	p := f.lastSearch
	if p.Limit != 20 || p.Offset != 0 || p.Sort != store.SortNewest || p.Q != "" {
		t.Errorf("defaults: %+v", p)
	}
}

func TestJobsRejectsBadParameters(t *testing.T) {
	cases := map[string]string{
		"remote=sometimes":                "remote",
		"seniority=wizard":                "seniority",
		"employment=forever":              "employment",
		"limit=0":                         "limit",
		"limit=101":                       "limit",
		"limit=abc":                       "limit",
		"offset=-1":                       "offset",
		"offset=999999":                   "offset",
		"exp=-1":                          "exp",
		"exp=99":                          "exp",
		"exp=lots":                        "exp",
		"posted_within=0":                 "posted_within",
		"posted_within=9999":              "posted_within",
		"country=India":                   "country",
		"company=Not+A+Slug!":             "company",
		"sort=random":                     "sort",
		"has_salary=maybe":                "has_salary",
		"hide_unknown_exp=maybe":          "hide_unknown_exp",
		"entry_level=maybe":               "entry_level",
		"q=" + strings.Repeat("a", 201):   "q",
		"city=" + strings.Repeat("a", 61): "city",
	}
	for query, want := range cases {
		f := &fakeStore{}
		rec := do(newTestServer(f), "GET", "/api/jobs?"+query)
		if rec.Code != 400 {
			t.Errorf("%.40s: status %d, want 400", query, rec.Code)
			continue
		}
		if !strings.Contains(errMsg(t, rec), want) {
			t.Errorf("%.40s: error %q should mention %q", query, errMsg(t, rec), want)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%.40s: errors must not be cached", query)
		}
	}
}

func TestOnlyReadMethods(t *testing.T) {
	s := newTestServer(&fakeStore{})
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		rec := do(s, m, "/api/jobs")
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s: status %d allow %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if rec := do(s, "HEAD", "/api/jobs"); rec.Code != 200 {
		t.Errorf("HEAD: %d", rec.Code)
	}
}

func TestUnknownRoute(t *testing.T) {
	rec := do(newTestServer(&fakeStore{}), "GET", "/api/nope")
	if rec.Code != 404 {
		t.Errorf("status %d", rec.Code)
	}
}

func TestJobDetail(t *testing.T) {
	f := &fakeStore{}
	s := newTestServer(f)
	for _, bad := range []string{"/api/job", "/api/job?id=abc", "/api/job?id=0", "/api/job?id=-3", "/api/job?id=1;DROP"} {
		if rec := do(s, "GET", bad); rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", bad, rec.Code)
		}
	}
	if rec := do(s, "GET", "/api/job?id=42"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":42`) {
		t.Errorf("ok case: %d %s", rec.Code, rec.Body.String())
	}
	f.jobErr = store.ErrNotFound
	if rec := do(s, "GET", "/api/job?id=42"); rec.Code != 404 {
		t.Errorf("missing job: status %d", rec.Code)
	}
}

func TestInternalErrorsDoNotLeak(t *testing.T) {
	f := &fakeStore{searchErr: errors.New(`pq: password authentication failed for user "neondb_owner" at ep-secret.neon.tech`)}
	rec := do(newTestServer(f), "GET", "/api/jobs")
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "neon") || strings.Contains(body, "password") || errMsg(t, rec) != "internal error" {
		t.Errorf("internal details leaked: %s", body)
	}
}

func TestTimeoutIsReportedPlainly(t *testing.T) {
	f := &fakeStore{searchErr: context.DeadlineExceeded}
	if rec := do(newTestServer(f), "GET", "/api/jobs"); rec.Code != 504 {
		t.Errorf("status %d, want 504", rec.Code)
	}
}

func TestPanicIsContained(t *testing.T) {
	f := &fakeStore{panicOn: "search"}
	rec := do(newTestServer(f), "GET", "/api/jobs")
	if rec.Code != 500 || errMsg(t, rec) != "internal error" {
		t.Errorf("status %d %s", rec.Code, rec.Body.String())
	}
}

func TestAggregatesAreCached(t *testing.T) {
	f := &fakeStore{}
	s := newTestServer(f)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		if rec := do(s, "GET", "/api/facets"); rec.Code != 200 {
			t.Fatalf("facets: %d", rec.Code)
		}
	}
	if n := atomic.LoadInt32(&f.facetCalls); n != 1 {
		t.Errorf("facets computed %d times within the TTL, want 1", n)
	}
	clock = clock.Add(6 * time.Minute)
	do(s, "GET", "/api/facets")
	if n := atomic.LoadInt32(&f.facetCalls); n != 2 {
		t.Errorf("facets should refresh after the TTL, computed %d times", n)
	}
}

func TestStatsSourcesAndHealth(t *testing.T) {
	f := &fakeStore{}
	s := newTestServer(f)
	if rec := do(s, "GET", "/api/stats"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"open_jobs":10`) {
		t.Errorf("stats: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(s, "GET", "/api/sources"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "greenhouse") {
		t.Errorf("sources: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(s, "GET", "/api/health")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("health: %d %v", rec.Code, rec.Header())
	}
	f.pingErr = errors.New("connection refused to ep-secret.neon.tech")
	rec = do(s, "GET", "/api/health")
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "neon") {
		t.Errorf("degraded health must be 503 without details: %d %s", rec.Code, rec.Body.String())
	}
}

// Behind Vercel's rewrite the route arrives as ?_route=...
func TestRouteViaQueryParameter(t *testing.T) {
	f := &fakeStore{}
	rec := do(newTestServer(f), "GET", "/api/index?_route=jobs&q=python&limit=5")
	if rec.Code != 200 || f.lastSearch.Q != "python" || f.lastSearch.Limit != 5 {
		t.Errorf("rewritten request not routed: %d %+v", rec.Code, f.lastSearch)
	}
	rec = do(newTestServer(f), "GET", "/api/index?_route=job&id=5")
	if rec.Code != 200 {
		t.Errorf("job via _route: %d", rec.Code)
	}
}

func TestRoutePath(t *testing.T) {
	cases := map[string]string{
		"/api/jobs":               "/jobs",
		"/api/jobs/":              "/jobs",
		"/api":                    "/",
		"/api/index":              "/",
		"/api/index?_route=jobs":  "/jobs",
		"/api/index?_route=/job/": "/job",
		"/jobs":                   "/jobs", // dev server mounts without the prefix too
	}
	for in, want := range cases {
		req := httptest.NewRequest("GET", in, nil)
		if got := routePath(req); got != want {
			t.Errorf("routePath(%q) = %q, want %q", in, got, want)
		}
	}
}

var _ http.Handler = (*Server)(nil)
