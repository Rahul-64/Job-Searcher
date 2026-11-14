// Package lever implements the fetcher.Adapter for Lever public job boards:
//
//	GET https://api.lever.co/v0/postings/{token}?mode=json
//
// The endpoint is public. Its robots.txt allows crawling with Crawl-delay 1,
// which the validator's polite client honours.
package lever

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
)

const (
	Kind           = "lever"
	defaultBaseURL = "https://api.lever.co"
	maxBodyBytes   = 64 << 20
)

var (
	tokenRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
	countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

type Adapter struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// New returns an adapter for the real Lever API (use the validator's polite client).
func New(userAgent string, client *http.Client) *Adapter {
	return NewWithBaseURL(defaultBaseURL, userAgent, client)
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	return &Adapter{baseURL: strings.TrimRight(baseURL, "/"), client: client, userAgent: userAgent}
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) BoardURL(token string) string {
	return a.baseURL + "/v0/postings/" + token
}

func checkToken(token string) error {
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("invalid lever board token %q", token)
	}
	return nil
}

func (a *Adapter) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lever request: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fetcher.ErrBoardNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("lever: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("lever read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("lever: response exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}

// Company checks the board exists. Lever exposes no company name, so it returns "".
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	_, err := a.get(ctx, a.BoardURL(token)+"?mode=json&limit=1")
	return "", err
}

type leverJob struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	HostedURL   string `json:"hostedUrl"`
	CreatedAt   int64  `json:"createdAt"` // milliseconds since epoch
	Country     string `json:"country"`
	Workplace   string `json:"workplaceType"`
	SalaryRange *struct {
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
		Currency string  `json:"currency"`
		Interval string  `json:"interval"`
	} `json:"salaryRange"`
	Desc       string `json:"description"`
	DescBody   string `json:"descriptionBody"`
	Additional string `json:"additional"`
	Lists      []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
	Categories struct {
		Location     string   `json:"location"`
		AllLocations []string `json:"allLocations"`
		Team         string   `json:"team"`
		Department   string   `json:"department"`
		Commitment   string   `json:"commitment"`
	} `json:"categories"`
}

// Fetch returns all postings on the board.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	if err := checkToken(token); err != nil {
		return nil, err
	}
	body, err := a.get(ctx, a.BoardURL(token)+"?mode=json")
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode postings: %w", err)
	}

	jobs := make([]fetcher.RawJob, 0, len(items))
	for i, raw := range items {
		rj, err := parseJob(raw)
		if err != nil {
			return nil, fmt.Errorf("posting #%d: %w", i, err)
		}
		jobs = append(jobs, rj)
	}
	return jobs, nil
}

// Parse rebuilds a RawJob from one posting's original JSON.
func (a *Adapter) Parse(payload []byte) (fetcher.RawJob, error) { return parseJob(payload) }

func parseJob(raw []byte) (fetcher.RawJob, error) {
	var j leverJob
	if err := json.Unmarshal(raw, &j); err != nil {
		return fetcher.RawJob{}, fmt.Errorf("decode posting: %w", err)
	}
	if j.ID == "" || strings.TrimSpace(j.Text) == "" {
		return fetcher.RawJob{}, fmt.Errorf("posting is missing id or title")
	}
	rj := fetcher.RawJob{
		ExternalID:      j.ID,
		URL:             j.HostedURL,
		Title:           strings.TrimSpace(j.Text),
		Location:        location(j),
		Department:      firstNonEmpty(j.Categories.Team, j.Categories.Department),
		DescriptionHTML: description(j),
		Remote:          j.Workplace,
		Employment:      strings.TrimSpace(j.Categories.Commitment),
		Payload:         append([]byte(nil), raw...),
	}
	if countryRe.MatchString(j.Country) {
		rj.Country = strings.ToUpper(j.Country)
	}
	if j.CreatedAt > 0 {
		t := time.UnixMilli(j.CreatedAt).UTC()
		rj.PostedAt = &t
	}
	// Lever sends {"min":0,"max":0} for "no range", which must stay empty.
	if sr := j.SalaryRange; sr != nil && sr.Max > 0 && sr.Min > 0 && sr.Max >= sr.Min {
		rj.SalaryMin, rj.SalaryMax = sr.Min, sr.Max
		rj.SalaryCurrency = strings.ToUpper(sr.Currency)
		rj.SalaryPeriod = periodFrom(sr.Interval)
	}
	return rj, nil
}

// periodFrom maps Lever's interval ("per-year-salary", "per-hour-wage", ...) to year/month/hour.
func periodFrom(interval string) string {
	l := strings.ToLower(interval)
	switch {
	case strings.Contains(l, "hour"):
		return "hour"
	case strings.Contains(l, "month"):
		return "month"
	case strings.Contains(l, "year"), strings.Contains(l, "annual"):
		return "year"
	}
	return ""
}

// location prefers the primary location and appends other distinct ones.
func location(j leverJob) string {
	var parts []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			parts = append(parts, s)
		}
	}
	add(j.Categories.Location)
	for _, l := range j.Categories.AllLocations {
		add(l)
	}
	return strings.Join(parts, " / ")
}

// description stitches Lever's separate sections back into one HTML document.
func description(j leverJob) string {
	var b strings.Builder
	b.WriteString(firstNonEmpty(j.Desc, j.DescBody))
	for _, l := range j.Lists {
		if strings.TrimSpace(l.Content) == "" {
			continue
		}
		b.WriteString("<h3>" + html.EscapeString(strings.TrimSpace(l.Text)) + "</h3><ul>" + l.Content + "</ul>")
	}
	b.WriteString(j.Additional)
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
