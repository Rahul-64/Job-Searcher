package lever

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"jobscraper/internal/fetcher"
)

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/postings.json")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/postings/acme", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") != "json" {
			t.Errorf("expected mode=json, got %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("limit") == "1" { // existence check
			w.Write([]byte(`[]`))
			return
		}
		w.Write(fixture)
	})
	mux.HandleFunc("/v0/postings/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("/v0/postings/garbage", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"not":"an array"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchParsesPostings(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())

	jobs, err := a.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	j := jobs[0]
	if j.ExternalID != "7d9af9b5-c1c7-48ec-bbb5-9b25e49f6596" {
		t.Errorf("ExternalID = %q", j.ExternalID)
	}
	if j.Title != "AM/ Manager - Risk & Decision Science" {
		t.Errorf("Title = %q", j.Title)
	}
	if j.Location != "Bangalore, Karnataka / Remote - India" {
		t.Errorf("Location should merge distinct locations, got %q", j.Location)
	}
	if j.Department != "Risk" {
		t.Errorf("Department should prefer team, got %q", j.Department)
	}
	if j.Country != "IN" {
		t.Errorf("Country should be upper-cased ISO code, got %q", j.Country)
	}
	if j.PostedAt == nil || j.PostedAt.Year() != 2025 {
		t.Errorf("PostedAt from createdAt ms: %v", j.PostedAt)
	}
	for _, want := range []string{
		"<strong>About the Team</strong>",
		"<h3>What you will do</h3><ul><li>Build models</li><li>Ship them</li></ul>",
		"equal opportunity employer",
	} {
		if !strings.Contains(j.DescriptionHTML, want) {
			t.Errorf("description missing %q: %q", want, j.DescriptionHTML)
		}
	}
	if strings.Contains(j.DescriptionHTML, "Empty section") {
		t.Error("empty list sections should be skipped")
	}
	if j.Remote != "onsite" || j.Employment != "Full Time Employee" {
		t.Errorf("hints: remote %q employment %q", j.Remote, j.Employment)
	}
	if j.SalaryMin != 1200000 || j.SalaryMax != 1800000 || j.SalaryCurrency != "INR" || j.SalaryPeriod != "year" {
		t.Errorf("structured salary: %v-%v %s/%s", j.SalaryMin, j.SalaryMax, j.SalaryCurrency, j.SalaryPeriod)
	}
	if !strings.Contains(string(j.Payload), `"workplaceType": "onsite"`) {
		t.Error("payload should keep the original JSON")
	}

	// Sparse posting: fallbacks, no timestamp, no country.
	s := jobs[1]
	if s.DescriptionHTML != "<p>Fallback body</p>" || s.Department != "Design" ||
		s.PostedAt != nil || s.Country != "" || s.Location != "" || s.SalaryMax != 0 {
		t.Errorf("sparse posting handled wrongly: %+v", s)
	}
}

func TestCompanyChecksExistence(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())

	name, err := a.Company(context.Background(), "acme")
	if err != nil || name != "" {
		t.Fatalf("existing board: name %q err %v (Lever has no name, want \"\")", name, err)
	}
	if _, err := a.Company(context.Background(), "nope"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Fatalf("missing board should be ErrBoardNotFound, got %v", err)
	}
}

func TestErrors(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())

	if _, err := a.Fetch(context.Background(), "nope"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("404: %v", err)
	}
	if _, err := a.Fetch(context.Background(), "broken"); err == nil || errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("500 should be a generic error: %v", err)
	}
	if _, err := a.Fetch(context.Background(), "garbage"); err == nil {
		t.Error("non-array body should fail to decode")
	}
	for _, bad := range []string{"", "a/b", "../x", "a b", "a?b"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q should be rejected", bad)
		}
	}
}

func TestParseMatchesFetch(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())
	jobs, err := a.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range jobs {
		got, err := a.Parse(want.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Parse differs from Fetch for %s:\n got  %+v\n want %+v", want.ExternalID, got, want)
		}
	}
}
