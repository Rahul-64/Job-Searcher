// Package greenhouse implements the fetcher.Adapter for Greenhouse public job boards:
//
//	GET https://boards-api.greenhouse.io/v1/boards/{token}/jobs?content=true
//
// The endpoint is public and intended for consumption, so no authentication is needed.
package greenhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
)

const (
	Kind           = "greenhouse"
	defaultBaseURL = "https://boards-api.greenhouse.io"
	maxBodyBytes   = 64 << 20 // 64 MiB; the largest board seen is ~9 MB
)

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

type Adapter struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// New returns an adapter that talks to the real Greenhouse API.
func New(userAgent string) *Adapter {
	return NewWithBaseURL(defaultBaseURL, userAgent, &http.Client{Timeout: 90 * time.Second})
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	return &Adapter{baseURL: strings.TrimRight(baseURL, "/"), client: client, userAgent: userAgent}
}

func (a *Adapter) Kind() string { return Kind }

func (a *Adapter) BoardURL(token string) string {
	return a.baseURL + "/v1/boards/" + token
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
		return nil, fmt.Errorf("greenhouse request: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fetcher.ErrBoardNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("greenhouse: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("greenhouse read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("greenhouse: response exceeds %d bytes", maxBodyBytes)
	}
	return body, nil
}

func checkToken(token string) error {
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("invalid greenhouse board token %q", token)
	}
	return nil
}

// Company returns the board's display name (falls back to the token).
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	body, err := a.get(ctx, a.BoardURL(token))
	if err != nil {
		return "", err
	}
	var b struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return "", fmt.Errorf("decode board: %w", err)
	}
	if strings.TrimSpace(b.Name) == "" {
		return token, nil
	}
	return strings.TrimSpace(b.Name), nil
}

type ghJob struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	AbsoluteURL    string `json:"absolute_url"`
	Content        string `json:"content"`
	UpdatedAt      string `json:"updated_at"`
	FirstPublished string `json:"first_published"`
	Location       struct {
		Name string `json:"name"`
	} `json:"location"`
	Departments []struct {
		Name string `json:"name"`
	} `json:"departments"`
}

// Fetch returns all jobs on the board, including descriptions.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	if err := checkToken(token); err != nil {
		return nil, err
	}
	body, err := a.get(ctx, a.BoardURL(token)+"/jobs?content=true")
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
		var j ghJob
		if err := json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("decode job #%d: %w", i, err)
		}
		if j.ID == 0 || strings.TrimSpace(j.Title) == "" {
			return nil, fmt.Errorf("job #%d is missing id or title", i)
		}
		depts := make([]string, 0, len(j.Departments))
		for _, d := range j.Departments {
			if n := strings.TrimSpace(d.Name); n != "" {
				depts = append(depts, n)
			}
		}
		jobs = append(jobs, fetcher.RawJob{
			ExternalID: strconv.FormatInt(j.ID, 10),
			URL:        j.AbsoluteURL,
			Title:      strings.TrimSpace(j.Title),
			Location:   strings.TrimSpace(j.Location.Name),
			Department: strings.Join(depts, ", "),
			// Greenhouse returns the description HTML entity-escaped.
			DescriptionHTML: html.UnescapeString(j.Content),
			PostedAt:        parseTime(j.FirstPublished),
			UpdatedAt:       parseTime(j.UpdatedAt),
			Payload:         append([]byte(nil), raw...),
		})
	}
	return jobs, nil
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
