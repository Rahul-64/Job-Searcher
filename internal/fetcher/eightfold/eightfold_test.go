package eightfold

import (
	"bytes"
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
	"sync"
	"testing"
	"time"

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

// fake is a Microsoft-like Eightfold site. Every position's detail is the fixture with its own id and name.
type fake struct {
	mu       sync.Mutex
	searches []url.Values
	details  []string
	search   func(start int) []byte
}

func (f *fake) adapter(t *testing.T) *Adapter {
	detail := read(t, "detail.json")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/pcsx/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "test-agent" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		f.mu.Lock()
		f.searches = append(f.searches, r.URL.Query())
		f.mu.Unlock()
		var start int
		fmt.Sscan(r.URL.Query().Get("start"), &start)
		w.Write(f.search(start))
	})
	mux.HandleFunc("/api/pcsx/position_details", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("position_id")
		f.mu.Lock()
		f.details = append(f.details, id)
		f.mu.Unlock()
		dec := json.NewDecoder(bytes.NewReader(detail))
		dec.UseNumber()
		var env map[string]any
		if err := dec.Decode(&env); err != nil {
			t.Fatal(err)
		}
		data := env["data"].(map[string]any)
		data["id"] = json.Number(id)
		data["name"] = "Position " + id
		out, _ := json.Marshal(env)
		w.Write(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL, "test-agent", srv.Client())
}

func fixtureFake(t *testing.T) *fake {
	search := read(t, "search.json")
	return &fake{search: func(start int) []byte {
		if start == 0 {
			return search
		}
		return []byte(`{"status":200,"error":null,"data":{"positions":[],"count":5}}`)
	}}
}

const token = "apply.example.com/example.com/India"

func TestFetchListsThenFetchesEachDetail(t *testing.T) {
	f := fixtureFake(t)
	jobs, err := f.adapter(t).Fetch(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 5 || len(f.details) != 5 {
		t.Fatalf("%d jobs, %d detail requests, want 5 and 5", len(jobs), len(f.details))
	}
	q := f.searches[0]
	if q.Get("domain") != "example.com" || q.Get("location") != "India" || q.Get("start") != "0" || q.Get("sort_by") != "timestamp" {
		t.Errorf("search query = %v", q)
	}
	j := jobs[0]
	if j.ExternalID == "" || j.Title == "" || !strings.HasPrefix(j.URL, "https://apply.careers.microsoft.com/careers/job/") {
		t.Errorf("identity: %+v", j)
	}
	if j.Employment != "Full-Time" || j.Remote != "onsite" || j.Department != "Consulting Project Management" {
		t.Errorf("employment/remote/department: %q / %q / %q", j.Employment, j.Remote, j.Department)
	}
	if j.PostedAt == nil || j.PostedAt.Year() != 2026 {
		t.Errorf("posted: %v", j.PostedAt)
	}
	if !strings.Contains(j.DescriptionHTML, "Microsoft") || len(j.Payload) == 0 || !json.Valid(j.Payload) {
		t.Error("description and original payload must be kept")
	}
	// Eightfold's own wording resolves to three Indian cities
	var cities []string
	for _, p := range normalizer.ParseLocation(j.Location).Places {
		cities = append(cities, p.City+"/"+p.Country)
	}
	if !reflect.DeepEqual(cities, []string{"Noida/IN", "Mumbai/IN", "Bengaluru/IN"}) {
		t.Errorf("places = %v from %q", cities, j.Location)
	}
}

func TestKeepSkipsTheDetailRequest(t *testing.T) {
	f := fixtureFake(t)
	a := f.adapter(t)
	a.Keep = func(title, department string) bool { return normalizer.RoleFamily(title, department) != "" }
	jobs, err := a.Fetch(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	// "Software Engineer" and "Principal Software Engineer" stay; consulting and solution roles are not requested
	if len(jobs) != 2 || len(f.details) != 2 {
		t.Errorf("%d jobs, %d detail requests, want 2 and 2", len(jobs), len(f.details))
	}
}

func TestPagesUntilTheCountIsReached(t *testing.T) {
	mk := func(from, n int) []byte {
		var ps []map[string]any
		for i := from; i < from+n; i++ {
			ps = append(ps, map[string]any{"id": json.Number(fmt.Sprint(1000 + i)), "name": fmt.Sprintf("Engineer %d", i), "department": "Engineering"})
		}
		b, _ := json.Marshal(map[string]any{"status": 200, "error": nil, "data": map[string]any{"count": 25, "positions": ps}})
		return b
	}
	f := &fake{search: func(start int) []byte {
		switch start {
		case 0:
			return mk(0, 10)
		case 10:
			return mk(10, 10)
		case 20:
			return mk(20, 5)
		}
		return mk(0, 0)
	}}
	jobs, err := f.adapter(t).Fetch(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	var starts []string
	for _, q := range f.searches {
		starts = append(starts, q.Get("start"))
	}
	if len(jobs) != 25 || !reflect.DeepEqual(starts, []string{"0", "10", "20"}) {
		t.Errorf("%d jobs from starts %v, want 25 from [0 10 20]", len(jobs), starts)
	}
}

func TestParseRebuildsTheSameJobAsFetch(t *testing.T) {
	f := fixtureFake(t)
	a := f.adapter(t)
	jobs, err := a.Fetch(context.Background(), token)
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

func TestWaitsAndRetriesWhenThrottled(t *testing.T) {
	search := read(t, "search.json")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 3 { // Microsoft's "Please try again later"
			http.Error(w, "Please try again later", http.StatusTooManyRequests)
			return
		}
		w.Write(search)
	}))
	defer srv.Close()
	a := NewWithBaseURL(srv.URL, "test-agent", srv.Client())
	var waits []time.Duration
	a.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }

	if _, err := a.Company(context.Background(), token); err != nil {
		t.Fatalf("should succeed after the throttling stops: %v", err)
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
	if calls != 4 || !reflect.DeepEqual(waits, want) {
		t.Errorf("%d calls, waits %v; want 4 calls and waits %v (growing)", calls, waits, want)
	}

	// it does not wait forever: after the allowed retries the error is reported
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "later", 429) }))
	defer always.Close()
	b := NewWithBaseURL(always.URL, "test-agent", always.Client())
	n := 0
	b.Sleep = func(ctx context.Context, d time.Duration) error { n++; return nil }
	if _, err := b.Company(context.Background(), token); err == nil || !strings.Contains(err.Error(), "429") || n != maxThrottleRetries {
		t.Errorf("want a 429 error after %d waits, got %v after %d", maxThrottleRetries, err, n)
	}

	// a cancelled run stops waiting at once
	c := NewWithBaseURL(always.URL, "test-agent", always.Client())
	c.Sleep = func(ctx context.Context, d time.Duration) error { return context.Canceled }
	if _, err := c.Company(context.Background(), token); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must stop the wait, got %v", err)
	}
	if throttleWait(0) != 10*time.Second || throttleWait(3) != 60*time.Second || throttleWait(50) != throttleCap {
		t.Errorf("wait schedule: %v %v %v", throttleWait(0), throttleWait(3), throttleWait(50))
	}
}

func TestErrorsAndTokens(t *testing.T) {
	respond := func(code int, body string) *Adapter {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code); w.Write([]byte(body)) }))
		t.Cleanup(srv.Close)
		return NewWithBaseURL(srv.URL, "test-agent", srv.Client())
	}
	// Netflix-style: the API exists but is switched off
	if _, err := respond(403, `{"message": "PCSX is not enabled for this user."}`).Company(context.Background(), token); !errors.Is(err, fetcher.ErrBoardNotFound) || !strings.Contains(err.Error(), "not enabled") {
		t.Errorf("a disabled API should say so, got %v", err)
	}
	if _, err := respond(500, "boom").Fetch(context.Background(), token); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected a 500 error, got %v", err)
	}
	if _, err := respond(200, `{"status":500,"error":"bad query","data":null}`).Fetch(context.Background(), token); err == nil {
		t.Error("an error inside a 200 response must be reported")
	}
	if _, err := respond(200, `{"status":200,"error":{"message":"rate limited","body":""},"data":{"count":1,"positions":[]}}`).Company(context.Background(), token); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("a non-empty error object must be reported, got %v", err)
	}
	// the real API sends an EMPTY error object with every successful response
	if _, err := respond(200, `{"status":200,"error":{"message":"","body":""},"data":{"count":1,"positions":[]}}`).Company(context.Background(), token); err != nil {
		t.Errorf("an empty error object is not an error: %v", err)
	}
	if _, err := respond(200, `{"error":null,"data":{"count":0,"positions":[]}}`).Company(context.Background(), token); !errors.Is(err, fetcher.ErrBoardNotFound) {
		t.Errorf("no jobs should be ErrBoardNotFound, got %v", err)
	}
	a := respond(200, "{}")
	for _, bad := range []string{"", "host", "host/domain", "Host/domain/India", "host/domain/In dia?x=1", "a/b/c/d", "../x/y/India"} {
		if _, err := a.Fetch(context.Background(), bad); err == nil {
			t.Errorf("token %q must be rejected", bad)
		}
	}
	if got := New("x", nil).BoardURL("apply.careers.microsoft.com/microsoft.com/India"); !strings.HasPrefix(got, "https://apply.careers.microsoft.com/api/pcsx/search?") {
		t.Errorf("BoardURL = %q", got)
	}
}
