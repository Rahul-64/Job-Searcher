// Package workday implements the fetcher.Adapter for career sites hosted on Workday:
//
//	POST https://{tenant}.{pod}.myworkdayjobs.com/wday/cxs/{tenant}/{site}/jobs      (the list)
//	GET  https://{tenant}.{pod}.myworkdayjobs.com/wday/cxs/{tenant}/{site}{path}     (one job)
//
// These are the JSON calls the career site's own page makes. The board token is
// "tenant/pod/site", for example "salesforce/wd12/External_Career_Site".
//
// The list is filtered to one country. The name of that filter differs from company to
// company (Adobe calls it locationCountry, Salesforce something much longer), so it is found
// in the facets the list returns, by the country's name. A job's description needs its own
// request, so a Keep filter lets the caller skip jobs that are clearly not wanted before that
// request is made.
package workday

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"jobscraper/internal/fetcher"
)

const (
	Kind           = "workday"
	pageSize       = 20 // Workday's maximum
	maxPostings    = 3000
	maxBodyBytes   = 16 << 20
	defaultCountry = "India"
)

var tokenRe = regexp.MustCompile(`^([a-z0-9]+)/(wd[0-9]{1,3})/([A-Za-z0-9_-]+)$`)

type Adapter struct {
	baseURL   string // set by tests; empty means the real tenant host
	client    *http.Client
	userAgent string

	// Country is the country whose jobs are listed (its Workday name).
	Country string
	// Keep, when set, decides from a job's title whether it is worth the request for its
	// description. Jobs it rejects are not returned at all.
	Keep func(title, department string) bool
}

// New returns an adapter for real Workday tenants. Pass the polite client from the validator
// package so robots.txt, rate limits and retries apply.
func New(userAgent string, client *http.Client) *Adapter {
	return &Adapter{client: client, userAgent: userAgent, Country: defaultCountry}
}

// NewWithBaseURL is used by tests to point the adapter at a fake server.
func NewWithBaseURL(baseURL, userAgent string, client *http.Client) *Adapter {
	a := New(userAgent, client)
	a.baseURL = strings.TrimRight(baseURL, "/")
	return a
}

func (a *Adapter) Kind() string { return Kind }

type board struct{ tenant, pod, site string }

func parseToken(token string) (board, error) {
	m := tokenRe.FindStringSubmatch(token)
	if m == nil {
		return board{}, fmt.Errorf("invalid workday token %q (want tenant/pod/site, e.g. salesforce/wd12/External_Career_Site)", token)
	}
	return board{tenant: m[1], pod: m[2], site: m[3]}, nil
}

func (a *Adapter) origin(b board) string {
	if a.baseURL != "" {
		return a.baseURL
	}
	return "https://" + b.tenant + "." + b.pod + ".myworkdayjobs.com"
}

func (a *Adapter) apiBase(b board) string {
	return a.origin(b) + "/wday/cxs/" + b.tenant + "/" + b.site
}

func (a *Adapter) BoardURL(token string) string {
	b, err := parseToken(token)
	if err != nil {
		return ""
	}
	return a.apiBase(b) + "/jobs"
}

// ---- requests ---------------------------------------------------------------------

func (a *Adapter) do(ctx context.Context, method, url string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workday request: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		// Workday answers 422 for a tenant or site name it does not know.
		return nil, fetcher.ErrBoardNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("workday: unexpected status %s", resp.Status)
	}
	// During maintenance Workday redirects to an HTML page with status 200.
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "json") {
		return nil, fmt.Errorf("workday returned %q instead of JSON (the site may be under maintenance)", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("workday read body: %w", err)
	}
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("workday: response exceeds %d bytes", maxBodyBytes)
	}
	return data, nil
}

type listRequest struct {
	AppliedFacets map[string][]string `json:"appliedFacets"`
	Limit         int                 `json:"limit"`
	Offset        int                 `json:"offset"`
	SearchText    string              `json:"searchText"`
}

type posting struct {
	Title        string `json:"title"`
	ExternalPath string `json:"externalPath"`
}

type facet struct {
	FacetParameter string  `json:"facetParameter"`
	Descriptor     string  `json:"descriptor"`
	ID             string  `json:"id"`
	Values         []facet `json:"values"`
}

type listResponse struct {
	Total       int       `json:"total"`
	JobPostings []posting `json:"jobPostings"`
	Facets      []facet   `json:"facets"`
}

func (a *Adapter) list(ctx context.Context, b board, facets map[string][]string, search string, offset, limit int) (listResponse, error) {
	if facets == nil {
		facets = map[string][]string{}
	}
	data, err := a.do(ctx, http.MethodPost, a.apiBase(b)+"/jobs", listRequest{AppliedFacets: facets, Limit: limit, Offset: offset, SearchText: search})
	if err != nil {
		return listResponse{}, err
	}
	var r listResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return listResponse{}, fmt.Errorf("decode workday list: %w", err)
	}
	return r, nil
}

// findCountryFacet looks through the facets (which nest differently per company) for the
// value named like the country, and returns the filter name it belongs to and its id.
func findCountryFacet(facets []facet, country string) (param, id string, ok bool) {
	var walk func(fs []facet, parent string) bool
	walk = func(fs []facet, parent string) bool {
		for _, f := range fs {
			p := parent
			if f.FacetParameter != "" {
				p = f.FacetParameter
			}
			if f.ID != "" && p != "" && strings.EqualFold(strings.TrimSpace(f.Descriptor), country) {
				param, id, ok = p, f.ID, true
				return true
			}
			if walk(f.Values, p) {
				return true
			}
		}
		return false
	}
	walk(facets, "")
	return
}

// Company checks that the site exists and has jobs. Workday does not say the company's name,
// so the seed file supplies it.
func (a *Adapter) Company(ctx context.Context, token string) (string, error) {
	b, err := parseToken(token)
	if err != nil {
		return "", err
	}
	r, err := a.list(ctx, b, nil, "", 0, 1)
	if err != nil {
		return "", err
	}
	if r.Total == 0 {
		return "", fetcher.ErrBoardNotFound
	}
	return "", nil
}

// Fetch returns the country's jobs that pass Keep, each with its description.
func (a *Adapter) Fetch(ctx context.Context, token string) ([]fetcher.RawJob, error) {
	b, err := parseToken(token)
	if err != nil {
		return nil, err
	}

	// One unfiltered call, only to learn the name of the country filter.
	first, err := a.list(ctx, b, nil, "", 0, 1)
	if err != nil {
		return nil, err
	}
	facets, search := map[string][]string(nil), ""
	if param, id, ok := findCountryFacet(first.Facets, a.Country); ok {
		facets = map[string][]string{param: {id}}
	} else {
		search = a.Country // no country filter on this site: fall back to text search
	}

	var (
		jobs  []fetcher.RawJob
		seen  = map[string]bool{}
		total int
	)
	for offset := 0; offset < maxPostings; offset += pageSize {
		r, err := a.list(ctx, b, facets, search, offset, pageSize)
		if err != nil {
			return nil, err
		}
		// Workday reports the total on the first page only; later pages say 0.
		if offset == 0 {
			total = r.Total
		}
		for _, p := range r.JobPostings {
			if seen[p.ExternalPath] || !validPath(p.ExternalPath) {
				continue
			}
			seen[p.ExternalPath] = true
			if a.Keep != nil && !a.Keep(p.Title, "") {
				continue
			}
			data, err := a.do(ctx, http.MethodGet, a.apiBase(b)+p.ExternalPath, nil)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", p.ExternalPath, err)
			}
			rj, err := parseJob(data)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", p.ExternalPath, err)
			}
			jobs = append(jobs, rj)
		}
		if len(r.JobPostings) == 0 || offset+pageSize >= total {
			break
		}
	}
	return jobs, nil
}

// validPath accepts only the job paths Workday lists ("/job/Bangalore/Title_JR1"), so a
// strange value in a response cannot send a request somewhere else.
func validPath(p string) bool {
	return strings.HasPrefix(p, "/job/") && !strings.Contains(p, "..") && !strings.ContainsAny(p, "?#\\ ")
}

// Parse rebuilds a RawJob from one job's original JSON.
func (a *Adapter) Parse(payload []byte) (fetcher.RawJob, error) { return parseJob(payload) }

type detail struct {
	Info struct {
		ID                  string   `json:"id"`
		Title               string   `json:"title"`
		JobDescription      string   `json:"jobDescription"`
		Location            string   `json:"location"`
		AdditionalLocations []string `json:"additionalLocations"`
		StartDate           string   `json:"startDate"`
		TimeType            string   `json:"timeType"`
		JobReqID            string   `json:"jobReqId"`
		ExternalURL         string   `json:"externalUrl"`
		RemoteType          string   `json:"remoteType"`
		Country             struct {
			Descriptor string `json:"descriptor"`
		} `json:"country"`
	} `json:"jobPostingInfo"`
}

func parseJob(raw []byte) (fetcher.RawJob, error) {
	var d detail
	if err := json.Unmarshal(raw, &d); err != nil {
		return fetcher.RawJob{}, fmt.Errorf("decode job: %w", err)
	}
	i := d.Info
	id := strings.TrimSpace(i.JobReqID)
	if id == "" {
		id = strings.TrimSpace(i.ID)
	}
	if id == "" || strings.TrimSpace(i.Title) == "" {
		return fetcher.RawJob{}, fmt.Errorf("job is missing id or title")
	}
	places := []string{}
	for _, p := range append([]string{i.Location}, i.AdditionalLocations...) {
		if p = strings.TrimSpace(p); p != "" {
			places = append(places, p)
		}
	}
	rj := fetcher.RawJob{
		ExternalID:      id,
		URL:             strings.TrimSpace(i.ExternalURL),
		Title:           strings.TrimSpace(i.Title),
		Location:        strings.Join(places, "; "), // "India - Bangalore; India - Hyderabad"
		Country:         strings.TrimSpace(i.Country.Descriptor),
		Employment:      strings.TrimSpace(i.TimeType),
		Remote:          strings.TrimSpace(i.RemoteType),
		DescriptionHTML: i.JobDescription,
		Payload:         append([]byte(nil), raw...),
	}
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(i.StartDate)); err == nil {
		rj.PostedAt = &t
	}
	return rj, nil
}
