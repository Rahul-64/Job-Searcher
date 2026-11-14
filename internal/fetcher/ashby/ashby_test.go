package ashby

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"jobscraper/internal/fetcher"
)

// lastQuery records the raw query of the most recent board request.
var lastQuery string

func fakeServer(t *testing.T) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile("testdata/board.json")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/posting-api/job-board/acme", func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.RawQuery
		w.Write(fixture)
	})
	mux.HandleFunc("/posting-api/job-board/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchParsesAndFiltersUnlisted(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())

	jobs, err := a.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if lastQuery != "includeCompensation=true" {
		t.Errorf("Fetch should request compensation data, query was %q", lastQuery)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2 (the unlisted one must be dropped)", len(jobs))
	}
	for _, j := range jobs {
		if j.ExternalID == "unlisted-1" {
			t.Fatal("unlisted posting leaked through")
		}
	}

	j := jobs[0]
	if j.Title != "Engagement Manager, Chanakya" {
		t.Errorf("Title = %q", j.Title)
	}
	if j.Location != "Delhi / Mumbai" {
		t.Errorf("Location should dedupe case-insensitively, got %q", j.Location)
	}
	if j.Department != "Business Development" {
		t.Errorf("Department = %q", j.Department)
	}
	if j.Country != "IN" {
		t.Errorf("Country from address = %q, want IN", j.Country)
	}
	if j.PostedAt == nil || j.PostedAt.Year() != 2026 {
		t.Errorf("PostedAt = %v", j.PostedAt)
	}
	if j.DescriptionHTML != "<p>About us</p>" || j.URL == "" {
		t.Errorf("description/url wrong: %+v", j)
	}

	if j.Remote != "Hybrid" || j.Employment != "FullTime" {
		t.Errorf("hints: remote %q employment %q", j.Remote, j.Employment)
	}
	if j.SalaryMin != 2400000 || j.SalaryMax != 3600000 || j.SalaryCurrency != "INR" || j.SalaryPeriod != "year" {
		t.Errorf("structured salary must come from the Salary component: %v-%v %s/%s", j.SalaryMin, j.SalaryMax, j.SalaryCurrency, j.SalaryPeriod)
	}

	us := jobs[1]
	if us.Remote != "remote" {
		t.Errorf("isRemote without a workplace type should read as remote, got %q", us.Remote)
	}
	if us.SalaryMax != 0 {
		t.Error("no compensation means no structured salary")
	}
	if us.Country != "US" {
		t.Errorf("country from address should resolve via the country table, got %q", us.Country)
	}
	if us.PostedAt != nil {
		t.Error("garbage timestamp should become nil")
	}
	if us.Department != "Platform" {
		t.Errorf("Department should fall back to team, got %q", us.Department)
	}
}

func TestCompanyChecksExistence(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())
	if name, err := a.Company(context.Background(), "acme"); err != nil || name != "" {
		t.Fatalf("name %q err %v", name, err)
	}
	if _, err := a.Company(context.Background(), "nope"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestErrors(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test", srv.Client())
	if _, err := a.Fetch(context.Background(), "broken"); err == nil || errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("500 should be a generic error: %v", err)
	}
	for _, bad := range []string{"", "a/b", "..", ".", "a b", "a?b"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q should be rejected", bad)
		}
	}
}

func TestParseMatchesFetchAndReportsUnlisted(t *testing.T) {
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
	if _, err := a.Parse([]byte(`{"id":"x","title":"T","isListed":false}`)); err == nil {
		t.Error("an unlisted posting must not parse into a job")
	}
}
