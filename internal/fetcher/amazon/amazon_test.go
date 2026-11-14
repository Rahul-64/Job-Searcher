package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"jobscraper/internal/fetcher"
)

func fixtureServer(t *testing.T, handler http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL, "test-agent", srv.Client()), srv
}

func TestFetchParsesRealJobs(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search.json")
	if err != nil {
		t.Fatal(err)
	}
	var seenQuery url.Values
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/en/search.json" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		seenQuery = r.URL.Query()
		w.Write(fixture)
	})

	jobs, err := a.Fetch(context.Background(), "IND")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}

	// the request asks for India and only the technical categories, newest first
	if got := seenQuery["normalized_country_code[]"]; !reflect.DeepEqual(got, []string{"IND"}) {
		t.Errorf("country filter = %v", got)
	}
	if got := seenQuery["category[]"]; !reflect.DeepEqual(got, technicalCategories) {
		t.Errorf("category filter = %v", got)
	}
	if seenQuery.Get("result_limit") != "100" || seenQuery.Get("sort") != "recent" {
		t.Errorf("paging/sort = %v", seenQuery)
	}

	j := jobs[0]
	if j.ExternalID == "" || j.Title == "" || !strings.HasPrefix(j.URL, "https://www.amazon.jobs/en/jobs/") {
		t.Errorf("identity fields: %+v", j)
	}
	if j.Country != "IND" || !strings.HasSuffix(j.Location, "IND") {
		t.Errorf("location/country = %q / %q", j.Location, j.Country)
	}
	if j.Department != "Software Development" {
		t.Errorf("department = %q", j.Department)
	}
	if j.PostedAt == nil || j.PostedAt.Year() != 2026 {
		t.Errorf("posted = %v", j.PostedAt)
	}
	if !strings.Contains(j.DescriptionHTML, "BASIC QUALIFICATIONS") || !strings.Contains(j.DescriptionHTML, "years") {
		t.Error("qualifications (which state the years required) must be part of the description")
	}
	if len(j.Payload) == 0 || !json.Valid(j.Payload) {
		t.Error("the original JSON must be kept")
	}
	if j.Employment != "full-time" {
		t.Errorf("employment = %q", j.Employment)
	}
}

func TestParseRebuildsTheSameJobAsFetch(t *testing.T) {
	fixture, _ := os.ReadFile("testdata/search.json")
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write(fixture) })
	jobs, err := a.Fetch(context.Background(), "IND")
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		got, err := a.Parse(j.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, j) {
			t.Errorf("Parse differs from Fetch for %s", j.ExternalID)
		}
	}
}

// jobsJSON builds n synthetic jobs with ids first..first+n-1.
func jobsJSON(first, n int) []json.RawMessage {
	var out []json.RawMessage
	for i := first; i < first+n; i++ {
		out = append(out, json.RawMessage(fmt.Sprintf(
			`{"id_icims":"%d","title":"Software Development Engineer %d","normalized_location":"Pune, Maharashtra, IND","country_code":"IND","job_category":"Software Development","job_path":"/en/jobs/%d/x","posted_date":"November 1, 2026"}`, i, i, i)))
	}
	return out
}

func TestFetchPagesThroughAllResults(t *testing.T) {
	var offsets []string
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		offsets = append(offsets, off)
		var jobs []json.RawMessage
		switch off {
		case "0":
			jobs = jobsJSON(1, 100)
		case "100":
			jobs = jobsJSON(101, 50)
		}
		json.NewEncoder(w).Encode(map[string]any{"hits": 150, "jobs": jobs})
	})
	jobs, err := a.Fetch(context.Background(), "IND")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 150 || !reflect.DeepEqual(offsets, []string{"0", "100"}) {
		t.Errorf("got %d jobs from offsets %v, want 150 from [0 100]", len(jobs), offsets)
	}
}

func TestFetchDropsJobsThatShiftBetweenPages(t *testing.T) {
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		var jobs []json.RawMessage
		if r.URL.Query().Get("offset") == "0" {
			jobs = jobsJSON(1, 100)
		} else {
			jobs = jobsJSON(96, 10) // a new posting pushed 96..100 onto the second page
		}
		json.NewEncoder(w).Encode(map[string]any{"hits": 105, "jobs": jobs})
	})
	jobs, err := a.Fetch(context.Background(), "IND")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 105 {
		t.Errorf("got %d jobs, want 105 distinct ones", len(jobs))
	}
}

func TestInternAndMissingFields(t *testing.T) {
	a := NewWithBaseURL("http://unused", "x", http.DefaultClient)
	j, err := a.Parse([]byte(`{"id_icims":"9","title":"SDE Intern","is_intern":true,"job_schedule_type":"full-time","normalized_location":"Bengaluru, Karnataka, IND","country_code":"IND"}`))
	if err != nil || j.Employment != "Intern" {
		t.Errorf("intern: %+v, %v", j, err)
	}
	if j.URL != "" || j.PostedAt != nil {
		t.Errorf("missing path/date must stay empty: %+v", j)
	}
	if _, err := a.Parse([]byte(`{"title":"No id"}`)); err == nil {
		t.Error("a job without an id must be rejected")
	}
	if _, err := a.Parse([]byte(`{"id_icims":"1","title":"  "}`)); err == nil {
		t.Error("a job without a title must be rejected")
	}
	if _, err := a.Parse([]byte(`not json`)); err == nil {
		t.Error("garbage must be rejected")
	}
}

func TestCompanyAndTokens(t *testing.T) {
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits := 0
		if r.URL.Query().Get("normalized_country_code[]") == "IND" {
			hits = 574
		}
		json.NewEncoder(w).Encode(map[string]any{"hits": hits, "jobs": []any{}})
	})
	if name, err := a.Company(context.Background(), "IND"); err != nil || name != "Amazon" {
		t.Errorf("Company(IND) = %q, %v", name, err)
	}
	if _, err := a.Company(context.Background(), "ZZZ"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("a country with no jobs should be ErrBoardNotFound, got %v", err)
	}
	for _, bad := range []string{"", "IN", "india", "IND&x=1", "ind", "../x"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q must be rejected", bad)
		}
	}
	if got := a.BoardURL("IND"); !strings.HasSuffix(got, "normalized_country_code[]=IND") {
		t.Errorf("BoardURL = %q", got)
	}
}

func TestServerErrorIsReported(t *testing.T) {
	a, _ := fixtureServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
	if _, err := a.Fetch(context.Background(), "IND"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected a 500 error, got %v", err)
	}
}
