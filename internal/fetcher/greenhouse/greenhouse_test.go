package greenhouse

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
	fixture, err := os.ReadFile("testdata/jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/boards/acme/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("content") != "true" {
			t.Errorf("expected content=true, got %q", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		w.Write(fixture)
	})
	mux.HandleFunc("/v1/boards/acme", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"  Acme Corp ","content":""}`))
	})
	mux.HandleFunc("/v1/boards/noname", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"","content":""}`))
	})
	mux.HandleFunc("/v1/boards/broken/jobs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	// everything else -> 404
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchParsesJobs(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())

	jobs, err := a.Fetch(context.Background(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	j := jobs[0]
	if j.ExternalID != "4970739101" {
		t.Errorf("ExternalID = %q", j.ExternalID)
	}
	if j.Title != "Assistant Manager - Internal Audit" {
		t.Errorf("Title not trimmed: %q", j.Title)
	}
	if j.Location != "Bengaluru-VTP, India" {
		t.Errorf("Location = %q", j.Location)
	}
	if j.Department != "Internal Controls, Finance" {
		t.Errorf("Department = %q", j.Department)
	}
	wantDesc := "<div><strong>About us:</strong></div><p>We build &amp; ship.</p>"
	if j.DescriptionHTML != wantDesc {
		t.Errorf("DescriptionHTML = %q, want %q", j.DescriptionHTML, wantDesc)
	}
	if j.PostedAt == nil || j.UpdatedAt == nil {
		t.Error("timestamps should parse")
	}
	if !strings.Contains(string(j.Payload), `"id": 4970739101`) {
		t.Errorf("Payload should hold the original job JSON, got %q", j.Payload)
	}

	// Company-specific metadata: only the names we understand are used.
	if j.Employment != "Full-time" || j.Remote != "Hybrid" {
		t.Errorf("metadata hints: employment %q remote %q", j.Employment, j.Remote)
	}
	if jobs[1].Employment != "" || jobs[1].Remote != "" {
		t.Errorf("a posting without metadata must have no hints: %+v", jobs[1])
	}

	// Bad/missing timestamps become nil rather than failing the run.
	if jobs[1].UpdatedAt != nil || jobs[1].PostedAt != nil {
		t.Error("unparseable timestamps should be nil")
	}
}

func TestContentHashStableAndSensitive(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())
	jobs, _ := a.Fetch(context.Background(), "acme")

	h1 := jobs[0].ContentHash()
	if h1 != jobs[0].ContentHash() {
		t.Fatal("hash must be deterministic")
	}
	changed := jobs[0]
	changed.Title += "!"
	if changed.ContentHash() == h1 {
		t.Fatal("hash must change when the title changes")
	}
}

func TestCompany(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())

	name, err := a.Company(context.Background(), "acme")
	if err != nil || name != "Acme Corp" {
		t.Fatalf("got %q, %v", name, err)
	}
	name, err = a.Company(context.Background(), "noname")
	if err != nil || name != "noname" {
		t.Fatalf("empty name should fall back to token, got %q, %v", name, err)
	}
}

func TestErrors(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())

	if _, err := a.Fetch(context.Background(), "missing"); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("404 should be ErrBoardNotFound, got %v", err)
	}
	if _, err := a.Fetch(context.Background(), "broken"); err == nil || errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("500 should be a generic error, got %v", err)
	}
	for _, bad := range []string{"", "a/b", "../etc", "x y", "a?b=c"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q should be rejected", bad)
		}
	}
}

// Parse must rebuild exactly what Fetch produced from the stored payload; that is
// what makes re-deriving jobs without re-scraping trustworthy.
func TestParseMatchesFetch(t *testing.T) {
	srv := fakeServer(t)
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())
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
	if _, err := a.Parse([]byte(`{"id":0}`)); err == nil {
		t.Error("a payload without id/title must be rejected")
	}
}
