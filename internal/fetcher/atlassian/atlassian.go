// Package atlassian implements the fetcher.Adapter for Atlassian's careers listings, the
// JSON file behind atlassian.com/company/careers/all-jobs:
//
//	GET https://www.atlassian.com/endpoint/careers/listings
//
// One request returns every opening worldwide (about 300) with the full description, so there
// is no paging. Atlassian's robots.txt disallows only the individual job detail pages, not this
// endpoint. The board token is always "listings".
package atlassian

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
)

const (
	Kind           = "atlassian"
	Token          = "listings"
	defaultBaseURL = "https://www.atlassian.com"
	maxBodyBytes   = 32 << 20 // the file is about 2 MB
)

type Adapter struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// New returns an adapter for the real Atlassian site. Pass the polite client from the
// validator package so robots.txt, rate limits and retries apply.
func New(userAgent string, client *http.Client) *Adapter {
	return NewWithBaseURL(defaultBaseURL, userAgent, client)
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	return &Adapter{baseURL: strings.TrimRight(baseURL, "/"), client: client, userAgent: userAgent}
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) BoardURL(token string) string {
	return a.baseURL + "/endpoint/careers/listings"
}

func checkToken(token string) error {
	if token != Token {
		return fmt.Errorf("invalid atlassian token %q (the only board is %q)", token, Token)
	}
	return nil
}

func (a *Adapter) get(ctx context.Context) ([]json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.BoardURL(Token), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("atlassian request: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fetcher.ErrBoardNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("atlassian: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("atlassian read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("atlassian: response exceeds %d bytes", maxBodyBytes)
	}
	var listings []json.RawMessage
	if err := json.Unmarshal(body, &listings); err != nil {
		return nil, fmt.Errorf("decode atlassian listings: %w", err)
	}
	return listings, nil
}

// Company checks the endpoint answers and returns the display name.
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	if _, err := a.get(ctx); err != nil {
		return "", err
	}
	return "Atlassian", nil
}

// Fetch returns every opening worldwide; the pipeline keeps the ones in the target countries.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	if err := checkToken(token); err != nil {
		return nil, err
	}
	listings, err := a.get(ctx)
	if err != nil {
		return nil, err
	}
	jobs := make([]fetcher.RawJob, 0, len(listings))
	seen := map[string]bool{}
	for i, raw := range listings {
		rj, err := parseJob(raw)
		if err != nil {
			return nil, fmt.Errorf("listing #%d: %w", i, err)
		}
		if seen[rj.ExternalID] {
			continue
		}
		seen[rj.ExternalID] = true
		jobs = append(jobs, rj)
	}
	return jobs, nil
}

// Parse rebuilds a RawJob from one listing's original JSON.
func (a *Adapter) Parse(payload []byte) (fetcher.RawJob, error) { return parseJob(payload) }

type listing struct {
	ID        int      `json:"id"`
	Title     string   `json:"title"`
	Locations []string `json:"locations"`
	Category  string   `json:"category"`
	Type      string   `json:"type"`
	ApplyURL  string   `json:"applyUrl"`
	Portal    struct {
		URL         string `json:"portalUrl"`
		UpdatedDate string `json:"updatedDate"`
	} `json:"portalJobPost"`
	Overview         string `json:"overview"`
	Responsibilities string `json:"responsibilities"`
	Qualifications   string `json:"qualifications"`
}

func parseJob(raw []byte) (fetcher.RawJob, error) {
	var l listing
	if err := json.Unmarshal(raw, &l); err != nil {
		return fetcher.RawJob{}, fmt.Errorf("decode listing: %w", err)
	}
	if l.ID == 0 || strings.TrimSpace(l.Title) == "" {
		return fetcher.RawJob{}, fmt.Errorf("listing is missing id or title")
	}
	// "Bengaluru - India -   Bengaluru,  560071 India" and "Remote - Remote" are the site's own
	// wording; the location parser copes with them, and several places are joined with ";".
	places := make([]string, 0, len(l.Locations))
	for _, p := range l.Locations {
		if p = strings.TrimSpace(p); p != "" {
			places = append(places, p)
		}
	}
	rj := fetcher.RawJob{
		ExternalID: strconv.Itoa(l.ID),
		URL:        firstNonEmpty(l.ApplyURL, l.Portal.URL),
		Title:      strings.TrimSpace(l.Title),
		Location:   strings.Join(places, "; "),
		Department: strings.TrimSpace(l.Category),
		Employment: strings.TrimSpace(l.Type),
		Payload:    append([]byte(nil), raw...),
	}
	var b strings.Builder
	b.WriteString(l.Overview)
	if s := strings.TrimSpace(l.Responsibilities); s != "" {
		b.WriteString("<h3>Responsibilities</h3>" + s)
	}
	if s := strings.TrimSpace(l.Qualifications); s != "" {
		b.WriteString("<h3>Qualifications</h3>" + s)
	}
	rj.DescriptionHTML = b.String()
	if t, err := time.Parse("2006-01-02 03:04 PM", strings.TrimSpace(l.Portal.UpdatedDate)); err == nil {
		rj.UpdatedAt = &t
	}
	return rj, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
