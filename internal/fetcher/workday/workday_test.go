package workday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/normalizer"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const indiaID = "c4f78be1a8f14da0ab49ce1162348a5e"

func TestFindCountryFacetInBothShapes(t *testing.T) {
	for file, wantParam := range map[string]string{
		"facets_adobe.json":      "locationCountry", // nested inside locationMainGroup
		"facets_salesforce.json": "CF_-_REC_-_LRV_-_Job_Posting_Anchor_-_Country_from_Job_Posting_Location_Extended",
	} {
		var r listResponse
		if err := json.Unmarshal(read(t, file), &r); err != nil {
			t.Fatal(err)
		}
		param, id, ok := findCountryFacet(r.Facets, "India")
		if !ok || param != wantParam || id != indiaID {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, true)", file, param, id, ok, wantParam, indiaID)
		}
		if _, _, ok := findCountryFacet(r.Facets, "Narnia"); ok {
			t.Errorf("%s: a country that is not listed must not be found", file)
		}
	}
}

// site is a fake Workday tenant. It records what was asked of it.
type site struct {
	mu          sync.Mutex
	listBodies  []listRequest
	detailPaths []string
	listFor     func(listRequest) []byte
}

func (s *site) handler(t *testing.T) http.Handler {
	detail := read(t, "detail.json")
	mux := http.NewServeMux()
	mux.HandleFunc("/wday/cxs/acme/Careers/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("list request: %s %q %q", r.Method, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(r.Body)
		var req listRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad list body %s", body)
		}
		s.mu.Lock()
		s.listBodies = append(s.listBodies, req)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(s.listFor(req))
	})
	mux.HandleFunc("/wday/cxs/acme/Careers/job/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.detailPaths = append(s.detailPaths, r.URL.Path)
		s.mu.Unlock()
		// every job gets its own requisition id, taken from the path
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "_")+1:]
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.ReplaceAll(string(detail), "JR338174", id)))
	})
	return mux
}

func (s *site) adapter(t *testing.T) *Adapter {
	srv := httptest.NewServer(s.handler(t))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL, "test-agent", srv.Client())
}

func countryAware(t *testing.T) func(listRequest) []byte {
	facets, india := read(t, "facets_salesforce.json"), read(t, "list_india.json")
	return func(req listRequest) []byte {
		if len(req.AppliedFacets) == 0 {
			return facets // the unfiltered call that reveals the filter's name
		}
		return india
	}
}

func TestFetchListsOnlyIndiaAndParsesDetails(t *testing.T) {
	s := &site{listFor: countryAware(t)}
	a := s.adapter(t)

	jobs, err := a.Fetch(context.Background(), "acme/wd1/Careers")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 8 {
		t.Fatalf("got %d jobs, want 8", len(jobs))
	}

	// 1st call discovers the filter, later calls apply it, 20 at a time
	if len(s.listBodies) != 2 || len(s.listBodies[0].AppliedFacets) != 0 {
		t.Fatalf("list calls: %+v", s.listBodies)
	}
	applied := s.listBodies[1]
	wantParam := "CF_-_REC_-_LRV_-_Job_Posting_Anchor_-_Country_from_Job_Posting_Location_Extended"
	if !reflect.DeepEqual(applied.AppliedFacets, map[string][]string{wantParam: {indiaID}}) || applied.Limit != 20 || applied.Offset != 0 {
		t.Errorf("filtered call: %+v", applied)
	}

	j := jobs[0]
	if j.Title != "Software Engineering LMTS - Backend Distributed Systems" || j.ExternalID == "" || !strings.HasPrefix(j.URL, "https://") {
		t.Errorf("identity: %+v", j)
	}
	if j.Location != "India - Bangalore; India - Hyderabad" || j.Country != "India" {
		t.Errorf("location/country: %q / %q", j.Location, j.Country)
	}
	if j.Employment != "Full time" || j.Remote != "Office - Flexible" {
		t.Errorf("employment/remote: %q / %q", j.Employment, j.Remote)
	}
	if j.PostedAt == nil || j.PostedAt.Format("2006-01-02") != "2026-11-13" {
		t.Errorf("posted: %v", j.PostedAt)
	}
	if !strings.Contains(j.DescriptionHTML, "candidate experience") || len(j.Payload) == 0 || !json.Valid(j.Payload) {
		t.Error("description and original payload must be kept")
	}
	// Workday's own wording resolves to two Indian cities
	res := normalizer.ParseLocation(j.Location)
	var cities []string
	for _, p := range res.Places {
		cities = append(cities, p.City+"/"+p.Country)
	}
	if !reflect.DeepEqual(cities, []string{"Bengaluru/IN", "Hyderabad/IN"}) {
		t.Errorf("places = %v", cities)
	}
	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ExternalID] = true
	}
	if len(ids) != 8 {
		t.Errorf("each job needs its own id, got %d distinct", len(ids))
	}
}

func TestKeepSkipsTheDetailRequest(t *testing.T) {
	s := &site{listFor: countryAware(t)}
	a := s.adapter(t)
	a.Keep = func(title, department string) bool {
		return normalizer.RoleFamily(title, department) != ""
	}
	jobs, err := a.Fetch(context.Background(), "acme/wd1/Careers")
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, j := range jobs {
		titles = append(titles, j.Title)
	}
	// sales, HR and success roles are never requested
	for _, bad := range []string{"Business Development", "Territory Account", "People Consultant"} {
		for _, title := range titles {
			if strings.Contains(title, bad) {
				t.Errorf("%q should have been skipped", title)
			}
		}
	}
	if len(jobs) == 0 || len(jobs) >= 8 || len(s.detailPaths) != len(jobs) {
		t.Errorf("%d jobs, %d detail requests: only kept jobs may be requested (%v)", len(jobs), len(s.detailPaths), titles)
	}
}

func TestPagesThroughManyResults(t *testing.T) {
	var postings []map[string]string
	for i := 0; i < 45; i++ {
		postings = append(postings, map[string]string{"title": fmt.Sprintf("Engineer %d", i), "externalPath": fmt.Sprintf("/job/India---Pune/Engineer-%d_JR%d", i, 1000+i)})
	}
	facets := read(t, "facets_adobe.json")
	s := &site{listFor: func(req listRequest) []byte {
		if len(req.AppliedFacets) == 0 {
			return facets
		}
		end := req.Offset + req.Limit
		if end > len(postings) {
			end = len(postings)
		}
		// Like the real Workday: the total is sent with the first page only.
		total := 0
		if req.Offset == 0 {
			total = len(postings)
		}
		b, _ := json.Marshal(map[string]any{"total": total, "jobPostings": postings[req.Offset:end]})
		return b
	}}
	a := s.adapter(t)
	jobs, err := a.Fetch(context.Background(), "acme/wd1/Careers")
	if err != nil {
		t.Fatal(err)
	}
	var offsets []int
	for _, b := range s.listBodies[1:] {
		offsets = append(offsets, b.Offset)
	}
	if len(jobs) != 45 || !reflect.DeepEqual(offsets, []int{0, 20, 40}) {
		t.Errorf("%d jobs from offsets %v, want 45 from [0 20 40]", len(jobs), offsets)
	}
	if param := (func() string {
		for k := range s.listBodies[1].AppliedFacets {
			return k
		}
		return ""
	})(); param != "locationCountry" {
		t.Errorf("the Adobe-style filter name should be used, got %q", param)
	}
}

func TestFallsBackToTextSearchWithoutACountryFilter(t *testing.T) {
	s := &site{listFor: func(req listRequest) []byte {
		return []byte(`{"total":0,"jobPostings":[],"facets":[]}`)
	}}
	a := s.adapter(t)
	if _, err := a.Fetch(context.Background(), "acme/wd1/Careers"); err != nil {
		t.Fatal(err)
	}
	if last := s.listBodies[len(s.listBodies)-1]; last.SearchText != "India" || len(last.AppliedFacets) != 0 {
		t.Errorf("expected a text search for India, got %+v", last)
	}
}

func TestParseRebuildsTheSameJobAsFetch(t *testing.T) {
	s := &site{listFor: countryAware(t)}
	a := s.adapter(t)
	jobs, err := a.Fetch(context.Background(), "acme/wd1/Careers")
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		got, err := a.Parse(j.Payload)
		if err != nil || !reflect.DeepEqual(got, j) {
			t.Errorf("Parse differs from Fetch for %s (%v)", j.ExternalID, err)
		}
	}
}

func TestErrorsAndTokens(t *testing.T) {
	// maintenance page: HTML with status 200
	maint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>maintenance</html>"))
	}))
	defer maint.Close()
	a := NewWithBaseURL(maint.URL, "test-agent", maint.Client())
	if _, err := a.Company(context.Background(), "acme/wd1/Careers"); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Errorf("a maintenance page should be reported as such, got %v", err)
	}

	// unknown site: 422
	unknown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "{}", http.StatusUnprocessableEntity) }))
	defer unknown.Close()
	b := NewWithBaseURL(unknown.URL, "test-agent", unknown.Client())
	if _, err := b.Company(context.Background(), "acme/wd1/Careers"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("422 should mean board not found, got %v", err)
	}

	// no jobs at all
	empty := &site{listFor: func(listRequest) []byte { return []byte(`{"total":0,"jobPostings":[]}`) }}
	if _, err := empty.adapter(t).Company(context.Background(), "acme/wd1/Careers"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("a site with no jobs should be ErrBoardNotFound, got %v", err)
	}

	for _, bad := range []string{"", "acme", "acme/wd1", "ACME/wd1/Careers", "acme/pod1/Careers", "acme/wd1/Care ers", "../x/wd1/y", "acme/wd1/Careers/extra"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q must be rejected", bad)
		}
	}
	if got := New("x", nil).BoardURL("salesforce/wd12/External_Career_Site"); got != "https://salesforce.wd12.myworkdayjobs.com/wday/cxs/salesforce/External_Career_Site/jobs" {
		t.Errorf("BoardURL = %q", got)
	}
}

func TestStrangeJobPathsAreNeverRequested(t *testing.T) {
	facets := read(t, "facets_adobe.json")
	s := &site{listFor: func(req listRequest) []byte {
		if len(req.AppliedFacets) == 0 {
			return facets
		}
		return []byte(`{"total":4,"jobPostings":[
			{"title":"Engineer","externalPath":"/job/India---Pune/ok_JR1"},
			{"title":"Engineer","externalPath":"/job/../../admin"},
			{"title":"Engineer","externalPath":"https://evil.example/x"},
			{"title":"Engineer","externalPath":"/other/path"}]}`)
	}}
	a := s.adapter(t)
	jobs, err := a.Fetch(context.Background(), "acme/wd1/Careers")
	if err != nil || len(jobs) != 1 || len(s.detailPaths) != 1 {
		t.Errorf("%d jobs, %d requests (%v), want exactly one: %v", len(jobs), len(s.detailPaths), s.detailPaths, err)
	}
}
