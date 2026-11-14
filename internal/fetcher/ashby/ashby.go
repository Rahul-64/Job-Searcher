// Package ashby implements the fetcher.Adapter for Ashby public job boards:
//
//	GET https://api.ashbyhq.com/posting-api/job-board/{token}?includeCompensation=true
//
// The endpoint is Ashby's documented public posting API. Its robots.txt answers
// 401, which RFC 9309 treats as "no restrictions".
package ashby

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/normalizer"
)

const (
	Kind           = "ashby"
	defaultBaseURL = "https://api.ashbyhq.com"
	maxBodyBytes   = 64 << 20
)

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

type Adapter struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// New returns an adapter for the real Ashby API (use the validator's polite client).
func New(userAgent string, client *http.Client) *Adapter {
	return NewWithBaseURL(defaultBaseURL, userAgent, client)
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	return &Adapter{baseURL: strings.TrimRight(baseURL, "/"), client: client, userAgent: userAgent}
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) BoardURL(token string) string {
	return a.baseURL + "/posting-api/job-board/" + token
}

func checkToken(token string) error {
	if !tokenRe.MatchString(token) || token == "." || token == ".." {
		return fmt.Errorf("invalid ashby board token %q", token)
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
		return nil, fmt.Errorf("ashby request: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fetcher.ErrBoardNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("ashby: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ashby read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("ashby: response exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}

// Company checks the board exists. Ashby exposes no company name, so it returns "".
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	_, err := a.get(ctx, a.BoardURL(token))
	return "", err
}

type ashbyJob struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	Department         string `json:"department"`
	Team               string `json:"team"`
	Location           string `json:"location"`
	SecondaryLocations []struct {
		Location string `json:"location"`
	} `json:"secondaryLocations"`
	PublishedAt     string `json:"publishedAt"`
	IsListed        *bool  `json:"isListed"`
	JobURL          string `json:"jobUrl"`
	DescriptionHTML string `json:"descriptionHtml"`
	Address         struct {
		PostalAddress struct {
			AddressCountry string `json:"addressCountry"`
		} `json:"postalAddress"`
	} `json:"address"`
}

// Fetch returns all listed postings on the board.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	if err := checkToken(token); err != nil {
		return nil, err
	}
	body, err := a.get(ctx, a.BoardURL(token)+"?includeCompensation=true")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Jobs []json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}

	jobs := make([]fetcher.RawJob, 0, len(payload.Jobs))
	for i, raw := range payload.Jobs {
		var j ashbyJob
		if err := json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("decode job #%d: %w", i, err)
		}
		if j.IsListed != nil && !*j.IsListed {
			continue // unlisted postings are not public
		}
		if j.ID == "" || strings.TrimSpace(j.Title) == "" {
			return nil, fmt.Errorf("job #%d is missing id or title", i)
		}
		rj := fetcher.RawJob{
			ExternalID:      j.ID,
			URL:             j.JobURL,
			Title:           strings.TrimSpace(j.Title),
			Location:        location(j),
			Department:      firstNonEmpty(j.Department, j.Team),
			DescriptionHTML: j.DescriptionHTML,
			Payload:         append([]byte(nil), raw...),
		}
		if c := normalizer.CountryHint(j.Address.PostalAddress.AddressCountry); c != nil {
			rj.Country = *c
		}
		if t, err := time.Parse(time.RFC3339, j.PublishedAt); err == nil {
			rj.PostedAt = &t
		}
		jobs = append(jobs, rj)
	}
	return jobs, nil
}

func location(j ashbyJob) string {
	parts := []string{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[strings.ToLower(s)] {
			seen[strings.ToLower(s)] = true
			parts = append(parts, s)
		}
	}
	add(j.Location)
	for _, s := range j.SecondaryLocations {
		add(s.Location)
	}
	return strings.Join(parts, " / ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
