// Package validator provides the "polite HTTP client" every adapter uses.
//
// It is an http.RoundTripper that, before and around each request:
//   - checks robots.txt (RFC 9309 semantics, Crawl-delay honored),
//   - spaces requests per host so we never hammer a site,
//   - retries transient failures (network errors, 429, 5xx) with jittered
//     exponential backoff, honoring Retry-After.
//
// Adapters only need to use the returned *http.Client; they do not know any of
// this exists. The DB-backed parts of validation (circuit breaker, run lock,
// mass-drop guard) live in the store and pipeline packages.
package validator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// ErrDisallowedByRobots is returned when robots.txt forbids the request.
var ErrDisallowedByRobots = errors.New("disallowed by robots.txt")

// Options configures the polite client. Zero values get sensible defaults.
type Options struct {
	UserAgent     string
	MinInterval   time.Duration // minimum gap between requests to one host (default 1s)
	MaxRetries    int           // retries after the first attempt (default 3)
	BaseBackoff   time.Duration // first retry delay (default 1s)
	MaxBackoff    time.Duration // cap on a computed retry delay (default 30s)
	MaxRetryAfter time.Duration // longest server-requested wait we accept (default 2m)
	Timeout       time.Duration // whole-request timeout incl. retries (default 4m)
	RobotsTTL     time.Duration // how long a robots.txt is cached (default 24h)

	// Test hooks. Production code leaves these nil.
	Transport http.RoundTripper
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
	Rand      func() float64 // returns [0,1)

	// DisableRobots exists only so unit tests of the retry logic can run
	// without a robots.txt endpoint. It is not exposed through configuration.
	DisableRobots bool
}

func (o *Options) defaults() {
	if o.MinInterval <= 0 {
		o.MinInterval = time.Second
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	} else if o.MaxRetries == 0 {
		o.MaxRetries = 3
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.MaxRetryAfter <= 0 {
		o.MaxRetryAfter = 2 * time.Minute
	}
	if o.Timeout <= 0 {
		o.Timeout = 4 * time.Minute
	}
	if o.RobotsTTL <= 0 {
		o.RobotsTTL = 24 * time.Hour
	}
	if o.Transport == nil {
		o.Transport = http.DefaultTransport
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Rand == nil {
		o.Rand = defaultRand
	}
}

// NewClient returns an http.Client that applies robots.txt, rate limiting and retries.
func NewClient(o Options) *http.Client {
	o.defaults()
	t := &transport{
		o:    o,
		lim:  &limiter{next: map[string]time.Time{}, now: o.Now, sleep: o.Sleep},
		robo: map[string]*robotsEntry{},
	}
	return &http.Client{Transport: t, Timeout: o.Timeout}
}

type transport struct {
	o   Options
	lim *limiter

	roboMu sync.Mutex
	robo   map[string]*robotsEntry // keyed by scheme://host
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("validator: unsupported scheme %q", req.URL.Scheme)
	}
	if req.Header.Get("User-Agent") == "" && t.o.UserAgent != "" {
		req = req.Clone(req.Context()) // RoundTrippers must not mutate the caller's request
		req.Header.Set("User-Agent", t.o.UserAgent)
	}

	interval := t.o.MinInterval
	if !t.o.DisableRobots && req.URL.Path != "/robots.txt" {
		allowed, delay, err := t.checkRobots(req)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, fmt.Errorf("%w: %s%s", ErrDisallowedByRobots, req.URL.Host, req.URL.Path)
		}
		if delay > interval {
			interval = delay // Crawl-delay raises our spacing, never lowers it
		}
	}
	return t.fetch(req, interval)
}

// fetch performs the request with per-host spacing and retries.
func (t *transport) fetch(req *http.Request, interval time.Duration) (*http.Response, error) {
	ctx := req.Context()
	canRetry := req.Body == nil || req.Body == http.NoBody

	for attempt := 0; ; attempt++ {
		if err := t.lim.wait(ctx, req.URL.Host, interval); err != nil {
			return nil, err
		}
		resp, err := t.o.Transport.RoundTrip(req)

		var retryable bool
		var retryAfter time.Duration
		switch {
		case err != nil:
			retryable = ctx.Err() == nil // a cancelled context is never retried
		case retryableStatus(resp.StatusCode):
			retryable = true
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), t.o.Now())
		}
		if !retryable || !canRetry || attempt >= t.o.MaxRetries {
			return resp, err
		}
		if retryAfter > t.o.MaxRetryAfter {
			return resp, err // the server wants longer than we are willing to wait
		}

		wait := backoff(attempt, t.o.BaseBackoff, t.o.MaxBackoff, t.o.Rand())
		if retryAfter > wait {
			wait = retryAfter
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) //nolint:errcheck
			resp.Body.Close()
		}
		if err := t.o.Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff returns the delay before retry number attempt (0-based): exponential
// from base, capped at max, with jitter in [50%, 100%] of the computed value.
// r must be in [0,1).
func backoff(attempt int, base, max time.Duration, r float64) time.Duration {
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d/2 + time.Duration(float64(d/2)*r)
}

// parseRetryAfter understands both "120" (seconds) and an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// ---- rate limiting ---------------------------------------------------------

// limiter hands out request slots per host, at least `interval` apart.
// Reservations are made under the lock and slept on outside it, so concurrent
// workers queue up fairly instead of stampeding.
type limiter struct {
	mu    sync.Mutex
	next  map[string]time.Time
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func (l *limiter) wait(ctx context.Context, host string, interval time.Duration) error {
	l.mu.Lock()
	now := l.now()
	at := l.next[host]
	if at.Before(now) {
		at = now
	}
	l.next[host] = at.Add(interval)
	l.mu.Unlock()

	if d := at.Sub(now); d > 0 {
		return l.sleep(ctx, d)
	}
	return ctx.Err()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---- robots.txt ------------------------------------------------------------

type robotsEntry struct {
	data    *robotstxt.RobotsData
	expires time.Time
}

// productToken extracts the crawler name used in robots.txt User-agent lines
// from a full User-Agent header: "jobscraper/0.1 (contact)" -> "jobscraper".
func productToken(ua string) string {
	ua = strings.TrimSpace(ua)
	if i := strings.IndexAny(ua, "/ ("); i > 0 {
		ua = ua[:i]
	}
	return ua
}

// checkRobots reports whether req is allowed and any Crawl-delay for our agent.
func (t *transport) checkRobots(req *http.Request) (allowed bool, delay time.Duration, err error) {
	data, err := t.robotsFor(req)
	if err != nil {
		return false, 0, err
	}
	agent := productToken(t.o.UserAgent)
	// TestAgent (not FindGroup().Test) is required: only it honours the
	// allow-all / disallow-all states produced by robots.txt 4xx / 5xx responses.
	allowed = data.TestAgent(req.URL.RequestURI(), agent)
	return allowed, data.FindGroup(agent).CrawlDelay, nil
}

func (t *transport) robotsFor(req *http.Request) (*robotstxt.RobotsData, error) {
	key := req.URL.Scheme + "://" + req.URL.Host

	t.roboMu.Lock()
	defer t.roboMu.Unlock()
	if e, ok := t.robo[key]; ok && t.o.Now().Before(e.expires) {
		return e.data, nil
	}

	rreq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, key+"/robots.txt", nil)
	if err != nil {
		return nil, err
	}
	rreq.Header.Set("User-Agent", t.o.UserAgent)
	resp, err := t.fetch(rreq, t.o.MinInterval)
	if err != nil {
		// Unreachable robots.txt: refuse rather than assume permission (RFC 9309).
		return nil, fmt.Errorf("robots.txt for %s unavailable: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, fmt.Errorf("read robots.txt for %s: %w", req.URL.Host, err)
	}

	// RFC 9309: 2xx -> parse; 4xx -> everything allowed; 5xx -> everything disallowed.
	data, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		return nil, fmt.Errorf("parse robots.txt for %s: %w", req.URL.Host, err)
	}
	ttl := t.o.RobotsTTL
	if resp.StatusCode >= 500 {
		ttl = 5 * time.Minute // a broken robots.txt should be re-checked soon
	}
	t.robo[key] = &robotsEntry{data: data, expires: t.o.Now().Add(ttl)}
	return data, nil
}
