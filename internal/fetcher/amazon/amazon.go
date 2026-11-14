// Package amazon implements the fetcher.Adapter for Amazon's public job search, the
// same JSON endpoint that amazon.jobs uses for its own search page:
//
//	GET https://www.amazon.jobs/en/search.json?normalized_country_code[]=IND&category[]=...
//
// Its robots.txt disallows only /internal. The board token is an ISO alpha-3 country code
// ("IND"). Only Amazon's technical job categories are fetched: India lists about 2,000 jobs,
// most of them warehouse, operations, HR and finance roles that this project does not track.
package amazon

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
	Kind           = "amazon"
	defaultBaseURL = "https://www.amazon.jobs"
	pageSize       = 100
	maxPages       = 60       // a ceiling of 6,000 jobs; guards against a loop that never ends
	maxBodyBytes   = 32 << 20 // one page of 100 jobs is well under 2 MiB
)

// technicalCategories are the category slugs kept (Amazon's names in brackets). The slugs
// were checked against the live API; note "Applied Science" is machine-learning-science.
var technicalCategories = []string{
	"software-development",                 // Software Development
	"machine-learning-science",             // Applied Science
	"business-intelligence",                // Business Intelligence
	"data-science",                         // Data Science
	"research-science",                     // Research Science
	"systems-quality-security-engineering", // Systems, Quality, & Security Engineering
	"database-administration",              // Database Administration
}

var tokenRe = regexp.MustCompile(`^[A-Z]{3}$`)

type Adapter struct {
	baseURL   string
	client    *http.Client
	userAgent string
}

// New returns an adapter for the real Amazon jobs site. Pass the polite client from the
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
	return a.baseURL + "/en/search.json?normalized_country_code[]=" + token
}

func checkToken(token string) error {
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("invalid amazon country token %q (want an ISO alpha-3 code such as IND)", token)
	}
	return nil
}

func (a *Adapter) pageURL(token string, offset, limit int) string {
	q := url.Values{}
	q.Add("normalized_country_code[]", token)
	for _, c := range technicalCategories {
		q.Add("category[]", c)
	}
	q.Set("sort", "recent")
	q.Set("result_limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	return a.baseURL + "/en/search.json?" + q.Encode()
}

type page struct {
	Hits int               `json:"hits"`
	Jobs []json.RawMessage `json:"jobs"`
}

func (a *Adapter) getPage(ctx context.Context, token string, offset, limit int) (page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.pageURL(token, offset, limit), nil)
	if err != nil {
		return page{}, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return page{}, fmt.Errorf("amazon request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return page{}, fmt.Errorf("amazon: unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return page{}, fmt.Errorf("amazon read body: %w", err)
	}
	if len(body) > maxBodyBytes {
		return page{}, fmt.Errorf("amazon: response exceeds %d bytes", maxBodyBytes)
	}
	var p page
	if err := json.Unmarshal(body, &p); err != nil {
		return page{}, fmt.Errorf("decode amazon page: %w", err)
	}
	return p, nil
}

// Company checks that the country has technical jobs and returns the display name.
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	if err := checkToken(token); err != nil {
		return "", err
	}
	p, err := a.getPage(ctx, token, 0, 1)
	if err != nil {
		return "", err
	}
	if p.Hits == 0 {
		return "", fetcher.ErrBoardNotFound
	}
	return "Amazon", nil
}

// Fetch returns every technical job in the country, with descriptions and qualifications.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	if err := checkToken(token); err != nil {
		return nil, err
	}
	var (
		jobs   []fetcher.RawJob
		seen   = map[string]bool{}
		offset = 0
	)
	for pageNo := 0; pageNo < maxPages; pageNo++ {
		p, err := a.getPage(ctx, token, offset, pageSize)
		if err != nil {
			return nil, err
		}
		if len(p.Jobs) == 0 {
			break
		}
		for i, raw := range p.Jobs {
			rj, err := parseJob(raw)
			if err != nil {
				return nil, fmt.Errorf("job #%d on page %d: %w", i, pageNo, err)
			}
			// New postings during the scan shift the pages, so a job can appear twice.
			if seen[rj.ExternalID] {
				continue
			}
			seen[rj.ExternalID] = true
			jobs = append(jobs, rj)
		}
		offset += len(p.Jobs)
		if offset >= p.Hits {
			break
		}
	}
	return jobs, nil
}

// Parse rebuilds a RawJob from one job's original JSON.
func (a *Adapter) Parse(payload []byte) (fetcher.RawJob, error) { return parseJob(payload) }

type amzJob struct {
	IDIcims             string `json:"id_icims"`
	Title               string `json:"title"`
	Location            string `json:"location"`
	NormalizedLocation  string `json:"normalized_location"`
	CountryCode         string `json:"country_code"`
	JobCategory         string `json:"job_category"`
	JobPath             string `json:"job_path"`
	PostedDate          string `json:"posted_date"`
	JobScheduleType     string `json:"job_schedule_type"`
	IsIntern            *bool  `json:"is_intern"`
	Description         string `json:"description"`
	BasicQualifications string `json:"basic_qualifications"`
	PreferredQuals      string `json:"preferred_qualifications"`
}

func parseJob(raw []byte) (fetcher.RawJob, error) {
	var j amzJob
	if err := json.Unmarshal(raw, &j); err != nil {
		return fetcher.RawJob{}, fmt.Errorf("decode job: %w", err)
	}
	if strings.TrimSpace(j.IDIcims) == "" || strings.TrimSpace(j.Title) == "" {
		return fetcher.RawJob{}, fmt.Errorf("job is missing id or title")
	}
	loc := strings.TrimSpace(j.NormalizedLocation)
	if loc == "" {
		loc = strings.TrimSpace(j.Location)
	}
	rj := fetcher.RawJob{
		ExternalID: strings.TrimSpace(j.IDIcims),
		Title:      strings.TrimSpace(j.Title),
		Location:   loc,
		Department: strings.TrimSpace(j.JobCategory),
		Country:    strings.TrimSpace(j.CountryCode), // ISO alpha-3, which the normalizer accepts
		Employment: strings.TrimSpace(j.JobScheduleType),
		Payload:    append([]byte(nil), raw...),
	}
	if path := strings.TrimSpace(j.JobPath); path != "" {
		rj.URL = "https://www.amazon.jobs" + path
	}
	if j.IsIntern != nil && *j.IsIntern {
		rj.Employment = "Intern"
	}
	// The years of experience a job asks for are in the qualifications, so they are part of the text.
	var b strings.Builder
	b.WriteString(j.Description)
	if s := strings.TrimSpace(j.BasicQualifications); s != "" {
		b.WriteString("<br/><br/><strong>BASIC QUALIFICATIONS</strong><br/>" + s)
	}
	if s := strings.TrimSpace(j.PreferredQuals); s != "" {
		b.WriteString("<br/><br/><strong>PREFERRED QUALIFICATIONS</strong><br/>" + s)
	}
	rj.DescriptionHTML = b.String()
	if t, err := time.Parse("January 2, 2006", strings.TrimSpace(j.PostedDate)); err == nil {
		rj.PostedAt = &t
	}
	return rj, nil
}
