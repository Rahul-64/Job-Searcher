// Package eightfold implements the fetcher.Adapter for career sites built on Eightfold's
// "PCSX" API, which Microsoft uses:
//
//	GET https://{host}/api/pcsx/search?domain={domain}&location={location}&start={n}
//	GET https://{host}/api/pcsx/position_details?position_id={id}&domain={domain}
//
// These sites' robots.txt blocks everything except a short allow-list that includes
// /api/pcsx, so the operator permits this API explicitly. The board token is
// "host/domain/location", for example "apply.careers.microsoft.com/microsoft.com/India".
//
// Not every Eightfold customer enables PCSX (Netflix answers "PCSX is not enabled").
// Microsoft turns away more than half of all requests with HTTP 429 ("try again later") and
// sends no Retry-After, whatever the pace. The polite client retries a few times; on top of that
// this adapter waits longer, with growing pauses, before giving up on a request.
package eightfold

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
)

const (
	Kind         = "eightfold"
	maxPositions = 2000
	maxBodyBytes = 16 << 20

	maxThrottleRetries = 6                // extra attempts after the polite client's own
	throttleBase       = 10 * time.Second // first wait; doubles each time, capped below
	throttleCap        = 60 * time.Second
)

var tokenRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*)/([a-z0-9][a-z0-9.-]*)/([A-Za-z][A-Za-z -]{0,40})$`)

type Adapter struct {
	baseURL   string // set by tests; empty means https://{host}
	client    *http.Client
	userAgent string

	// Keep, when set, decides from a job's title and department whether it is worth the
	// request for its description. Jobs it rejects are not returned at all.
	Keep func(title, department string) bool

	// Sleep waits between throttled attempts; tests replace it so they do not really wait.
	Sleep func(ctx context.Context, d time.Duration) error
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// throttleWait is how long to wait after the n-th (0-based) throttled attempt: 10s, 20s, 40s, 60s, ...
func throttleWait(n int) time.Duration {
	d := throttleBase << uint(n)
	if d > throttleCap || d <= 0 {
		return throttleCap
	}
	return d
}

// New returns an adapter for real Eightfold sites. Pass the polite client from the
// validator package so robots.txt, rate limits and retries apply.
func New(userAgent string, client *http.Client) *Adapter {
	return &Adapter{client: client, userAgent: userAgent}
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	a := New(userAgent, client)
	a.baseURL = strings.TrimRight(baseURL, "/")
	return a
}

func (a *Adapter) Kind() string { return Kind }

type board struct{ host, domain, location string }

func parseToken(token string) (board, error) {
	m := tokenRe.FindStringSubmatch(token)
	if m == nil {
		return board{}, fmt.Errorf("invalid eightfold token %q (want host/domain/location, e.g. apply.careers.microsoft.com/microsoft.com/India)", token)
	}
	return board{host: m[1], domain: m[2], location: m[3]}, nil
}

func (a *Adapter) origin(b board) string {
	if a.baseURL != "" {
		return a.baseURL
	}
	return "https://" + b.host
}

func (a *Adapter) searchURL(b board, start int) string {
	q := url.Values{}
	q.Set("domain", b.domain)
	q.Set("query", "")
	q.Set("location", b.location)
	q.Set("start", strconv.Itoa(start))
	q.Set("sort_by", "timestamp")
	return a.origin(b) + "/api/pcsx/search?" + q.Encode()
}

func (a *Adapter) detailURL(b board, id string) string {
	q := url.Values{}
	q.Set("position_id", id)
	q.Set("domain", b.domain)
	q.Set("hl", "en")
	return a.origin(b) + "/api/pcsx/position_details?" + q.Encode()
}

func (a *Adapter) BoardURL(token string) string {
	b, err := parseToken(token)
	if err != nil {
		return ""
	}
	return a.searchURL(b, 0)
}

// request makes one GET and returns the status and body.
func (a *Adapter) request(ctx context.Context, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("eightfold request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("eightfold read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return 0, nil, fmt.Errorf("eightfold: response exceeds %d bytes", maxBodyBytes)
	}
	return resp.StatusCode, body, nil
}

func (a *Adapter) get(ctx context.Context, rawURL string) (json.RawMessage, error) {
	sleep := a.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	var (
		code int
		body []byte
	)
	for attempt := 0; ; attempt++ {
		var err error
		if code, body, err = a.request(ctx, rawURL); err != nil {
			return nil, err
		}
		if code != http.StatusTooManyRequests || attempt >= maxThrottleRetries {
			break
		}
		if err := sleep(ctx, throttleWait(attempt)); err != nil {
			return nil, err
		}
	}
	switch {
	case code == http.StatusNotFound:
		return nil, fetcher.ErrBoardNotFound
	case code == http.StatusForbidden && strings.Contains(string(body), "not enabled"):
		return nil, fmt.Errorf("eightfold: the PCSX API is not enabled on this site: %w", fetcher.ErrBoardNotFound)
	case code != http.StatusOK:
		return nil, fmt.Errorf("eightfold: unexpected status %d %s", code, http.StatusText(code))
	}
	var env struct {
		Status int             `json:"status"`
		Error  json.RawMessage `json:"error"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode eightfold response: %w", err)
	}
	if msg := apiError(env.Error); msg != "" || (env.Status != 0 && env.Status != http.StatusOK) || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, fmt.Errorf("eightfold returned an error (status %d): %s", env.Status, msg)
	}
	return env.Data, nil
}

// apiError reads the "error" field. A successful response still carries one, as an object with
// empty message and body, so only a non-empty value counts.
func apiError(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var obj struct {
		Message string `json:"message"`
		Body    string `json:"body"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return strings.TrimSpace(obj.Message + " " + obj.Body)
	}
	return s
}

type position struct {
	ID         json.Number `json:"id"`
	Name       string      `json:"name"`
	Department string      `json:"department"`
}

type searchData struct {
	Count     int        `json:"count"`
	Positions []position `json:"positions"`
}

func (a *Adapter) search(ctx context.Context, b board, start int) (searchData, error) {
	raw, err := a.get(ctx, a.searchURL(b, start))
	if err != nil {
		return searchData{}, err
	}
	var d searchData
	if err := json.Unmarshal(raw, &d); err != nil {
		return searchData{}, fmt.Errorf("decode eightfold search: %w", err)
	}
	return d, nil
}

// Company checks that the site answers and has jobs. The seed file supplies the name.
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	b, err := parseToken(token)
	if err != nil {
		return "", err
	}
	d, err := a.search(ctx, b, 0)
	if err != nil {
		return "", err
	}
	if d.Count == 0 {
		return "", fetcher.ErrBoardNotFound
	}
	return "", nil
}

// Fetch returns the location's jobs that pass Keep, each with its description.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	b, err := parseToken(token)
	if err != nil {
		return nil, err
	}
	var (
		jobs []fetcher.RawJob
		seen = map[string]bool{}
	)
	for start := 0; start < maxPositions; {
		d, err := a.search(ctx, b, start)
		if err != nil {
			return nil, err
		}
		if len(d.Positions) == 0 {
			break
		}
		for _, p := range d.Positions {
			id := p.ID.String()
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if a.Keep != nil && !a.Keep(p.Name, p.Department) {
				continue
			}
			raw, err := a.get(ctx, a.detailURL(b, id))
			if err != nil {
				return nil, fmt.Errorf("position %s: %w", id, err)
			}
			rj, err := a.parseDetail(b, raw)
			if err != nil {
				return nil, fmt.Errorf("position %s: %w", id, err)
			}
			jobs = append(jobs, rj)
		}
		start += len(d.Positions)
		if start >= d.Count {
			break
		}
	}
	return jobs, nil
}

// Parse rebuilds a RawJob from one position's original JSON.
func (a *Adapter) Parse(payload []byte) (fetcher.RawJob, error) {
	return a.parseDetail(board{}, payload)
}

type detail struct {
	ID              json.Number `json:"id"`
	DisplayJobID    string      `json:"displayJobId"`
	Name            string      `json:"name"`
	Department      string      `json:"department"`
	Locations       []string    `json:"locations"`
	PostedTs        int64       `json:"postedTs"`
	JobDescription  string      `json:"jobDescription"`
	WorkLocationOpt string      `json:"workLocationOption"`
	PositionURL     string      `json:"positionUrl"`
	PublicURL       string      `json:"publicUrl"`
	EmploymentType  []string    `json:"efcustomTextEmploymentType"`
}

func (a *Adapter) parseDetail(b board, raw []byte) (fetcher.RawJob, error) {
	var d detail
	if err := json.Unmarshal(raw, &d); err != nil {
		return fetcher.RawJob{}, fmt.Errorf("decode position: %w", err)
	}
	id := d.ID.String()
	if id == "" || strings.TrimSpace(d.Name) == "" {
		return fetcher.RawJob{}, fmt.Errorf("position is missing id or name")
	}
	places := make([]string, 0, len(d.Locations))
	for _, l := range d.Locations {
		if l = strings.TrimSpace(l); l != "" {
			places = append(places, l)
		}
	}
	rj := fetcher.RawJob{
		ExternalID:      id,
		URL:             strings.TrimSpace(d.PublicURL),
		Title:           strings.TrimSpace(d.Name),
		Location:        strings.Join(places, "; "), // "India, Karnataka, Bangalore; India, Maharashtra, Mumbai"
		Department:      strings.TrimSpace(d.Department),
		Remote:          strings.TrimSpace(d.WorkLocationOpt),
		DescriptionHTML: d.JobDescription,
		Payload:         append([]byte(nil), raw...),
	}
	if rj.URL == "" && d.PositionURL != "" && b.host != "" {
		rj.URL = a.origin(b) + d.PositionURL
	}
	if len(d.EmploymentType) > 0 {
		rj.Employment = strings.TrimSpace(d.EmploymentType[0])
	}
	if d.PostedTs > 0 {
		t := time.Unix(d.PostedTs, 0).UTC()
		rj.PostedAt = &t
	}
	return rj, nil
}
