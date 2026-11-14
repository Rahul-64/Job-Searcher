package atlassian

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/normalizer"
)

func server(t *testing.T, h http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL, "test-agent", srv.Client())
}

func fixtureAdapter(t *testing.T) *Adapter {
	fixture, err := os.ReadFile("testdata/listings.json")
	if err != nil {
		t.Fatal(err)
	}
	return server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/endpoint/careers/listings" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Write(fixture)
	})
}

func TestFetchParsesRealListings(t *testing.T) {
	jobs, err := fixtureAdapter(t).Fetch(context.Background(), Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 {
		t.Fatalf("got %d jobs, want 3", len(jobs))
	}
	j := jobs[0]
	if j.ExternalID == "" || j.Title == "" || !strings.HasPrefix(j.URL, "http") {
		t.Errorf("identity: %+v", j)
	}
	if j.Department == "" || j.Employment != "Full-Time" {
		t.Errorf("department/employment: %q / %q", j.Department, j.Employment)
	}
	if !strings.Contains(j.DescriptionHTML, "Responsibilities") || !strings.Contains(j.DescriptionHTML, "Qualifications") {
		t.Error("the description should include responsibilities and qualifications")
	}
	if j.UpdatedAt == nil {
		t.Error("the portal's updated date should parse")
	}
	if len(j.Payload) == 0 || !json.Valid(j.Payload) {
		t.Error("the original JSON must be kept")
	}
}

// The site's own location wording must resolve to India, or the job would be dropped as out of scope.
func TestLocationsResolveToIndia(t *testing.T) {
	jobs, err := fixtureAdapter(t).Fetch(context.Background(), Token)
	if err != nil {
		t.Fatal(err)
	}
	india, other := 0, 0
	for _, j := range jobs {
		res := normalizer.ParseLocation(j.Location)
		inIndia := false
		for _, p := range res.Places {
			if p.Country == "IN" {
				inIndia = true
			}
		}
		if inIndia {
			india++
		} else {
			other++
		}
	}
	if india != 2 || other != 1 {
		t.Errorf("%d resolved to India and %d elsewhere, want 2 and 1", india, other)
	}
}

func TestParseRebuildsTheSameJobAsFetch(t *testing.T) {
	a := fixtureAdapter(t)
	jobs, err := a.Fetch(context.Background(), Token)
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

func TestDuplicatesBadInputAndErrors(t *testing.T) {
	a := server(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"title":"A","locations":["Sydney"]},{"id":1,"title":"A again"},{"id":2,"title":"B"}]`))
	})
	jobs, err := a.Fetch(context.Background(), Token)
	if err != nil || len(jobs) != 2 {
		t.Errorf("duplicate ids should collapse: %d jobs, %v", len(jobs), err)
	}
	if _, err := a.Fetch(context.Background(), "other"); err == nil {
		t.Error("only the listings board exists")
	}
	if name, err := a.Company(context.Background(), Token); err != nil || name != "Atlassian" {
		t.Errorf("Company = %q, %v", name, err)
	}
	for _, bad := range []string{`[{"title":"no id"}]`, `[{"id":3,"title":" "}]`} {
		b := server(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(bad)) })
		if _, err := b.Fetch(context.Background(), Token); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
	notFound := server(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := notFound.Fetch(context.Background(), Token); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("404 should be ErrBoardNotFound, got %v", err)
	}
	broken := server(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", 500) })
	if _, err := broken.Fetch(context.Background(), Token); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected a 500 error, got %v", err)
	}
}
