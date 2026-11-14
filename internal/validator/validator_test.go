package validator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock; Sleep advances it and records waits.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.sleeps = append(c.sleeps, d)
	return nil
}

func newTestClient(t *testing.T, clk *fakeClock, robots bool, mutate func(*Options)) *http.Client {
	t.Helper()
	o := Options{
		UserAgent:     "jobscraper/0.1 (test)",
		MinInterval:   time.Second,
		BaseBackoff:   time.Second,
		MaxBackoff:    30 * time.Second,
		MaxRetries:    3,
		Now:           clk.Now,
		Sleep:         clk.Sleep,
		Rand:          func() float64 { return 1 }, // deterministic: full backoff, no jitter reduction
		DisableRobots: !robots,
	}
	if mutate != nil {
		mutate(&o)
	}
	return NewClient(o)
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	base, max := time.Second, 10*time.Second
	// With jitter at its upper bound the delay doubles until it hits the cap.
	want := []time.Duration{1, 2, 4, 8, 10, 10}
	for i, w := range want {
		got := backoff(i, base, max, 0.999999)
		if diff := w*time.Second - got; diff < 0 || diff > 10*time.Millisecond {
			t.Errorf("attempt %d: got %v, want ~%v", i, got, w*time.Second)
		}
	}
	// With jitter at its lower bound the delay is half of that.
	if got := backoff(2, base, max, 0); got != 2*time.Second {
		t.Errorf("jitter floor: got %v, want 2s", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("7", now); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	date := now.Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(date, now); d < 89*time.Second || d > 91*time.Second {
		t.Errorf("http-date: %v", d)
	}
	for _, bad := range []string{"", "soon", "-5"} {
		if d := parseRetryAfter(bad, now); d != 0 {
			t.Errorf("%q should be 0, got %v", bad, d)
		}
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	clk := newClock()
	resp, err := newTestClient(t, clk, false, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("status %d after %d calls, want 200 after 3", resp.StatusCode, calls)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()

	resp, err := newTestClient(t, newClock(), false, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 502 || atomic.LoadInt32(&calls) != 4 { // 1 try + 3 retries
		t.Fatalf("status %d, %d calls; want 502 after 4 calls", resp.StatusCode, calls)
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		var calls int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(code)
		}))
		resp, err := newTestClient(t, newClock(), false, nil).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if calls != 1 {
			t.Errorf("status %d was attempted %d times, want 1", code, calls)
		}
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	clk := newClock()
	resp, err := newTestClient(t, clk, false, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var longest time.Duration
	for _, s := range clk.sleeps {
		if s > longest {
			longest = s
		}
	}
	if longest < 20*time.Second {
		t.Fatalf("expected a wait of at least 20s (Retry-After), longest was %v (%v)", longest, clk.sleeps)
	}
}

func TestRetryAfterTooLongFailsFast(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "86400") // a day
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	resp, err := newTestClient(t, newClock(), false, nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 || calls != 1 {
		t.Fatalf("status %d after %d calls; want 429 after 1 (do not wait a day)", resp.StatusCode, calls)
	}
}

func TestRateLimitSpacesRequestsPerHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	clk := newClock()
	c := newTestClient(t, clk, false, func(o *Options) { o.MinInterval = 2 * time.Second })
	start := clk.Now()
	for i := 0; i < 3; i++ {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// Request 1 goes immediately; 2 and 3 each wait 2s.
	if elapsed := clk.Now().Sub(start); elapsed != 4*time.Second {
		t.Fatalf("3 requests took %v of simulated time, want 4s (sleeps %v)", elapsed, clk.sleeps)
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if _, err := newTestClient(t, newClock(), false, nil).Do(req); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

// ---- robots.txt ------------------------------------------------------------

func robotsServer(t *testing.T, robotsStatus int, robotsBody string, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			atomic.AddInt32(hits, 1)
			w.WriteHeader(robotsStatus)
			w.Write([]byte(robotsBody))
			return
		}
		w.Write([]byte("page"))
	}))
}

func TestRobotsDisallowAndCache(t *testing.T) {
	var hits int32
	srv := robotsServer(t, 200, "User-agent: *\nDisallow: /private\n", &hits)
	defer srv.Close()
	c := newTestClient(t, newClock(), true, nil)

	resp, err := c.Get(srv.URL + "/public")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	_, err = c.Get(srv.URL + "/private/data")
	if !errors.Is(err, ErrDisallowedByRobots) {
		t.Fatalf("expected ErrDisallowedByRobots, got %v", err)
	}
	if hits != 1 {
		t.Errorf("robots.txt fetched %d times, want 1 (cached)", hits)
	}
}

func TestRobotsAgentSpecificRule(t *testing.T) {
	var hits int32
	srv := robotsServer(t, 200, "User-agent: jobscraper\nDisallow: /\n\nUser-agent: *\nAllow: /\n", &hits)
	defer srv.Close()

	_, err := newTestClient(t, newClock(), true, nil).Get(srv.URL + "/anything")
	if !errors.Is(err, ErrDisallowedByRobots) {
		t.Fatalf("a rule naming our product token must apply, got %v", err)
	}
}

func TestRobots4xxAllowsAll(t *testing.T) {
	var hits int32
	srv := robotsServer(t, 404, "", &hits)
	defer srv.Close()
	resp, err := newTestClient(t, newClock(), true, nil).Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("a missing robots.txt means allowed, got %v", err)
	}
	resp.Body.Close()
}

func TestRobots5xxDisallowsAll(t *testing.T) {
	var hits int32
	srv := robotsServer(t, 500, "", &hits)
	defer srv.Close()
	_, err := newTestClient(t, newClock(), true, nil).Get(srv.URL + "/x")
	if !errors.Is(err, ErrDisallowedByRobots) {
		t.Fatalf("an erroring robots.txt must block (RFC 9309), got %v", err)
	}
}

func TestCrawlDelayRaisesInterval(t *testing.T) {
	var hits int32
	srv := robotsServer(t, 200, "User-agent: *\nCrawl-delay: 5\n", &hits)
	defer srv.Close()

	clk := newClock()
	c := newTestClient(t, clk, true, nil) // MinInterval is 1s; Crawl-delay is 5s
	// Warm the robots cache first so we measure only page spacing.
	r, err := c.Get(srv.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()

	before := clk.Now()
	r, err = c.Get(srv.URL + "/b")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if gap := clk.Now().Sub(before); gap < 4*time.Second {
		t.Fatalf("second page request waited only %v; Crawl-delay is 5s", gap)
	}
}

func TestProductToken(t *testing.T) {
	cases := map[string]string{
		"jobscraper/0.1 (personal; contact: a@b.c)": "jobscraper",
		"jobscraper":  "jobscraper",
		"  MyBot 1.0": "MyBot",
	}
	for in, want := range cases {
		if got := productToken(in); got != want {
			t.Errorf("productToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRejectsNonHTTPScheme(t *testing.T) {
	if _, err := newTestClient(t, newClock(), false, nil).Get("ftp://example.com/x"); err == nil {
		t.Fatal("expected an error for ftp")
	}
}
