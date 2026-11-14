package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Sort orders for SearchJobs. Only these are accepted; the value never reaches
// SQL as text.
const (
	SortNewest    = "newest"
	SortRelevance = "relevance"
)

// SearchParams filters open jobs. Zero values mean "no filter".
type SearchParams struct {
	Q                string // free text, websearch syntax ("go backend", "-intern", "\"data engineer\"")
	City             string // canonical name or alias ("Bengaluru", "blr")
	Country          string // ISO alpha-2 of the job's primary place
	Remote           string // remote | hybrid | onsite
	Seniority        string
	Employment       string
	Company          string // company slug
	Exp              *int   // candidate's years of experience: hides jobs asking for more
	HideUnknownExp   bool   // with Exp: also hide jobs that state no requirement
	PostedWithinDays int
	HasSalary        bool
	EntryLevel       bool   // fresher / entry level, see entryLevelSQL
	Role             string // "tech" (any technical role) or one family: software, ai_data, infra_security, qa
	Sort             string
	Limit, Offset    int
}

// RoleTech is the Role filter value for "any technical role family".
const RoleTech = "tech"

// entryLevelSQL is the single definition of "fresher / entry level". The search
// filter and the quick-view counts both use it, so they always agree. A job is
// entry level when it is never a senior title, and then either states a minimum
// of at most one year, or (stating none) is an intern or junior role. Jobs with
// no stated experience and no junior signal are not guessed to be entry level.
const entryLevelSQL = `(coalesce(j.seniority, '') NOT IN ('senior', 'lead', 'staff', 'principal', 'director', 'executive')
	AND CASE WHEN j.exp_min_years IS NOT NULL THEN j.exp_min_years <= 1
	         ELSE coalesce(j.seniority, '') IN ('intern', 'junior') END)`

// Money is a pay range.
type Money struct {
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	Currency string  `json:"currency"`
	Period   string  `json:"period"`
}

// CompanyRef identifies a company in results.
type CompanyRef struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// JobSummary is one row of a search result.
type JobSummary struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Company       CompanyRef `json:"company"`
	City          *string    `json:"city"`
	Region        *string    `json:"region"`
	Country       *string    `json:"country"`
	LocationRaw   string     `json:"location_raw"`
	LocationCount int        `json:"location_count"`
	RemoteType    *string    `json:"remote_type"`
	Seniority     *string    `json:"seniority"`
	Employment    *string    `json:"employment_type"`
	RoleFamily    *string    `json:"role_family"` // software | ai_data | infra_security | qa, or null
	ExpMin        *int       `json:"exp_min"`
	ExpMax        *int       `json:"exp_max"`
	Salary        *Money     `json:"salary"`
	PostedAt      *time.Time `json:"posted_at"`
	FirstSeenAt   time.Time  `json:"first_seen_at"`
	URL           string     `json:"url"`
	Snippet       string     `json:"snippet"`
	EntryLevel    bool       `json:"entry_level"` // computed by entryLevelSQL, the same rule as the filter
}

// SearchResult is one page of results plus the total number of matches.
type SearchResult struct {
	Total int          `json:"total"`
	Jobs  []JobSummary `json:"jobs"`
}

// buildSearch turns params into SQL and arguments. Every user-supplied value is
// a bound parameter; the only text spliced into the statement is chosen from
// fixed strings here (the ORDER BY clause is whitelisted).
func buildSearch(p SearchParams) (string, []any) {
	var (
		args  []any
		where = []string{"j.closed_at IS NULL"}
	)
	bind := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	var qRef string
	if p.Q != "" {
		qRef = bind(p.Q)
		where = append(where, "j.search_vector @@ websearch_to_tsquery('english', "+qRef+")")
	}
	if p.City != "" {
		c := bind(p.City)
		where = append(where, `EXISTS (SELECT 1 FROM job_locations jl WHERE jl.job_id = j.id AND jl.location_id IN (
			SELECT location_id FROM city_aliases WHERE alias = lower(`+c+`)
			UNION SELECT id FROM locations WHERE lower(city) = lower(`+c+`)))`)
	}
	if p.Country != "" {
		where = append(where, "j.country = "+bind(p.Country))
	}
	if p.Remote != "" {
		where = append(where, "j.remote_type = "+bind(p.Remote))
	}
	if p.Seniority != "" {
		where = append(where, "j.seniority = "+bind(p.Seniority))
	}
	if p.Employment != "" {
		where = append(where, "j.employment_type = "+bind(p.Employment))
	}
	if p.Company != "" {
		where = append(where, "c.slug = "+bind(p.Company))
	}
	if p.Exp != nil {
		cond := "j.exp_min_years <= " + bind(*p.Exp)
		if !p.HideUnknownExp {
			cond = "(j.exp_min_years IS NULL OR " + cond + ")"
		}
		where = append(where, cond)
	}
	if p.PostedWithinDays > 0 {
		where = append(where, "coalesce(j.posted_at, j.first_seen_at) >= now() - make_interval(days => "+bind(p.PostedWithinDays)+")")
	}
	if p.HasSalary {
		where = append(where, "j.salary_min IS NOT NULL")
	}
	if p.EntryLevel {
		where = append(where, entryLevelSQL)
	}
	switch p.Role {
	case "":
	case RoleTech:
		where = append(where, "j.role_family IS NOT NULL")
	default:
		where = append(where, "j.role_family = "+bind(p.Role))
	}

	order := "coalesce(j.posted_at, j.first_seen_at) DESC, j.id DESC"
	if p.Sort == SortRelevance && qRef != "" {
		order = "ts_rank(j.search_vector, websearch_to_tsquery('english', " + qRef + ")) DESC, " + order
	}

	// With a text query the snippet is the passage around the match, with matched
	// words wrapped in ⟦ ⟧ (the dashboard turns them into highlights using text
	// nodes, never HTML). Without one it is the start of the description.
	snippet := `left(regexp_replace(coalesce(j.description, ''), '\s+', ' ', 'g'), 220)`
	if qRef != "" {
		snippet = `regexp_replace(ts_headline('english', coalesce(j.description, ''), websearch_to_tsquery('english', ` + qRef + `), ` +
			`'StartSel=⟦, StopSel=⟧, MaxFragments=1, MaxWords=34, MinWords=18, ShortWord=2'), '\s+', ' ', 'g')`
	}

	limit, offset := bind(p.Limit), bind(p.Offset)
	sql := `
		SELECT j.id, j.title, c.name, c.slug, j.city, j.region, j.country, coalesce(j.location_raw, ''),
		       (SELECT count(*) FROM job_locations jl WHERE jl.job_id = j.id),
		       j.remote_type, j.seniority, j.employment_type, j.role_family, j.exp_min_years, j.exp_max_years,
		       j.salary_min, j.salary_max, j.salary_currency, j.salary_period,
		       j.posted_at, j.first_seen_at,
		       coalesce((SELECT p.url FROM job_postings p WHERE p.job_id = j.id AND p.removed_at IS NULL
		                 ORDER BY p.id LIMIT 1), ''),
		       ` + snippet + `,
		       ` + entryLevelSQL + `,
		       count(*) OVER ()
		FROM jobs j JOIN companies c ON c.id = j.company_id
		WHERE ` + strings.Join(where, "\n		  AND ") + `
		ORDER BY ` + order + `
		LIMIT ` + limit + ` OFFSET ` + offset
	return sql, args
}

// SearchJobs returns one page of open jobs matching the filters.
func (s *Store) SearchJobs(ctx context.Context, p SearchParams) (SearchResult, error) {
	sql, args := buildSearch(p)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return SearchResult{}, err
	}
	defer rows.Close()

	res := SearchResult{Jobs: []JobSummary{}}
	for rows.Next() {
		var (
			j          JobSummary
			smin, smax *float64
			scur, sper *string
		)
		if err := rows.Scan(&j.ID, &j.Title, &j.Company.Name, &j.Company.Slug, &j.City, &j.Region, &j.Country,
			&j.LocationRaw, &j.LocationCount, &j.RemoteType, &j.Seniority, &j.Employment, &j.RoleFamily, &j.ExpMin, &j.ExpMax,
			&smin, &smax, &scur, &sper, &j.PostedAt, &j.FirstSeenAt, &j.URL, &j.Snippet, &j.EntryLevel, &res.Total); err != nil {
			return SearchResult{}, err
		}
		j.Salary = money(smin, smax, scur, sper)
		res.Jobs = append(res.Jobs, j)
	}
	return res, rows.Err()
}

func money(min, max *float64, cur, per *string) *Money {
	if min == nil || max == nil || cur == nil || per == nil {
		return nil
	}
	return &Money{Min: *min, Max: *max, Currency: *cur, Period: *per}
}

// PlaceOut is a location of a job.
type PlaceOut struct {
	City    *string `json:"city"`
	Region  *string `json:"region"`
	Country string  `json:"country"`
}

// PostingOut is one source listing of a job.
type PostingOut struct {
	Source      string    `json:"source"` // greenhouse | lever | ashby
	URL         string    `json:"url"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	Active      bool      `json:"active"`
}

// JobDetail is a job with its full description, places and postings.
type JobDetail struct {
	JobSummary
	Description string       `json:"description"`
	ClosedAt    *time.Time   `json:"closed_at"`
	Places      []PlaceOut   `json:"places"`
	Postings    []PostingOut `json:"postings"`
}

// GetJob loads one job (open or closed) by id, or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, id int64) (*JobDetail, error) {
	var (
		d          JobDetail
		smin, smax *float64
		scur, sper *string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT j.id, j.title, c.name, c.slug, j.city, j.region, j.country, coalesce(j.location_raw, ''),
		       (SELECT count(*) FROM job_locations jl WHERE jl.job_id = j.id),
		       j.remote_type, j.seniority, j.employment_type, j.role_family, j.exp_min_years, j.exp_max_years,
		       j.salary_min, j.salary_max, j.salary_currency, j.salary_period,
		       j.posted_at, j.first_seen_at,
		       coalesce((SELECT p.url FROM job_postings p WHERE p.job_id = j.id AND p.removed_at IS NULL
		                 ORDER BY p.id LIMIT 1), ''),
		       coalesce(j.description, ''), j.closed_at, `+entryLevelSQL+`
		FROM jobs j JOIN companies c ON c.id = j.company_id WHERE j.id = $1`, id).
		Scan(&d.ID, &d.Title, &d.Company.Name, &d.Company.Slug, &d.City, &d.Region, &d.Country,
			&d.LocationRaw, &d.LocationCount, &d.RemoteType, &d.Seniority, &d.Employment, &d.RoleFamily, &d.ExpMin, &d.ExpMax,
			&smin, &smax, &scur, &sper, &d.PostedAt, &d.FirstSeenAt, &d.URL, &d.Description, &d.ClosedAt, &d.EntryLevel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Salary = money(smin, smax, scur, sper)
	d.Snippet = ""

	d.Places = []PlaceOut{}
	rows, err := s.pool.Query(ctx, `
		SELECT l.city, l.region, l.country FROM job_locations jl JOIN locations l ON l.id = jl.location_id
		WHERE jl.job_id = $1 ORDER BY l.city NULLS LAST`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p PlaceOut
		if err := rows.Scan(&p.City, &p.Region, &p.Country); err != nil {
			rows.Close()
			return nil, err
		}
		d.Places = append(d.Places, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	d.Postings = []PostingOut{}
	rows, err = s.pool.Query(ctx, `
		SELECT s.kind, p.url, p.first_seen_at, p.last_seen_at, p.removed_at IS NULL
		FROM job_postings p JOIN sources s ON s.id = p.source_id
		WHERE p.job_id = $1 ORDER BY p.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PostingOut
		if err := rows.Scan(&p.Source, &p.URL, &p.FirstSeenAt, &p.LastSeenAt, &p.Active); err != nil {
			return nil, err
		}
		d.Postings = append(d.Postings, p)
	}
	return &d, rows.Err()
}

// FacetValue is one option of a filter with its job count.
type FacetValue struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

// Facets are the available filter options with counts over open jobs.
type Facets struct {
	Cities     []FacetValue `json:"cities"`
	Companies  []FacetValue `json:"companies"` // Value is the slug, Label the name
	Remote     []FacetValue `json:"remote_types"`
	Seniority  []FacetValue `json:"seniorities"`
	Employment []FacetValue `json:"employment_types"`
	Roles      []FacetValue `json:"roles"` // role families with their job counts
	Quick      QuickCounts  `json:"quick"`
}

// QuickCounts are the sizes of the dashboard's quick views.
type QuickCounts struct {
	Tech        int `json:"tech"` // jobs with any technical role family
	EntryLevel  int `json:"entry_level"`
	Internships int `json:"internships"`
	New7Days    int `json:"new_7_days"`
}

func (s *Store) facetQuery(ctx context.Context, sql string) ([]FacetValue, error) {
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FacetValue{}
	for rows.Next() {
		var f FacetValue
		if err := rows.Scan(&f.Value, &f.Label, &f.Count); err != nil {
			return nil, err
		}
		if f.Label == f.Value {
			f.Label = ""
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Facets computes filter options over open jobs. The queries are independent,
// so they run concurrently: one network round trip of latency instead of five.
func (s *Store) Facets(ctx context.Context) (Facets, error) {
	var f Facets
	steps := []struct {
		dst *[]FacetValue
		sql string
	}{
		{&f.Cities, `SELECT l.city, l.city, count(DISTINCT jl.job_id)::int
			FROM job_locations jl JOIN locations l ON l.id = jl.location_id JOIN jobs j ON j.id = jl.job_id
			WHERE j.closed_at IS NULL AND l.city IS NOT NULL
			GROUP BY l.city ORDER BY 3 DESC, 1 LIMIT 40`},
		{&f.Companies, `SELECT c.slug, c.name, count(*)::int FROM jobs j JOIN companies c ON c.id = j.company_id
			WHERE j.closed_at IS NULL GROUP BY c.slug, c.name ORDER BY 3 DESC, 2`},
		{&f.Remote, `SELECT remote_type, remote_type, count(*)::int FROM jobs WHERE closed_at IS NULL AND remote_type IS NOT NULL GROUP BY 1 ORDER BY 3 DESC`},
		{&f.Seniority, `SELECT seniority, seniority, count(*)::int FROM jobs WHERE closed_at IS NULL AND seniority IS NOT NULL GROUP BY 1 ORDER BY 3 DESC`},
		{&f.Employment, `SELECT employment_type, employment_type, count(*)::int FROM jobs WHERE closed_at IS NULL AND employment_type IS NOT NULL GROUP BY 1 ORDER BY 3 DESC`},
		{&f.Roles, `SELECT role_family, role_family, count(*)::int FROM jobs WHERE closed_at IS NULL AND role_family IS NOT NULL GROUP BY 1 ORDER BY 3 DESC`},
	}
	errs := make([]error, len(steps))
	var wg sync.WaitGroup
	for i, st := range steps {
		wg.Add(1)
		go func(i int, dst *[]FacetValue, sql string) {
			defer wg.Done()
			*dst, errs[i] = s.facetQuery(ctx, sql) // each goroutine writes its own field
		}(i, st.dst, st.sql)
	}
	// The quick-view counts run alongside the facet lists (they write a different field).
	var quickErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		quickErr = s.pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE j.role_family IS NOT NULL)::int,
			       count(*) FILTER (WHERE `+entryLevelSQL+`)::int,
			       count(*) FILTER (WHERE j.seniority = 'intern')::int,
			       count(*) FILTER (WHERE coalesce(j.posted_at, j.first_seen_at) >= now() - interval '7 days')::int
			FROM jobs j WHERE j.closed_at IS NULL`).
			Scan(&f.Quick.Tech, &f.Quick.EntryLevel, &f.Quick.Internships, &f.Quick.New7Days)
	}()
	wg.Wait()
	errs = append(errs, quickErr)
	for _, err := range errs {
		if err != nil {
			return Facets{}, fmt.Errorf("facets: %w", err)
		}
	}
	return f, nil
}

// PublicStats is a small summary for the dashboard header.
type PublicStats struct {
	OpenJobs   int        `json:"open_jobs"`
	Companies  int        `json:"companies"`
	Sources    int        `json:"sources"`
	LastScrape *time.Time `json:"last_scrape"`
	NewLast7d  int        `json:"new_last_7_days"`
}

// PublicStats summarises the open jobs and when sources last ran.
func (s *Store) PublicStats(ctx context.Context) (PublicStats, error) {
	var p PublicStats
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM jobs WHERE closed_at IS NULL),
		       (SELECT count(DISTINCT company_id) FROM jobs WHERE closed_at IS NULL),
		       (SELECT count(*) FROM sources WHERE enabled),
		       (SELECT max(finished_at) FROM scrape_runs WHERE status IN ('ok', 'partial')),
		       (SELECT count(*) FROM jobs WHERE closed_at IS NULL AND coalesce(posted_at, first_seen_at) >= now() - interval '7 days')`).
		Scan(&p.OpenJobs, &p.Companies, &p.Sources, &p.LastScrape, &p.NewLast7d)
	return p, err
}

// SourceHealth is the public view of one source's state.
type SourceHealth struct {
	Company    string     `json:"company"`
	Slug       string     `json:"slug"`
	Kind       string     `json:"kind"`
	Enabled    bool       `json:"enabled"`
	Failures   int        `json:"consecutive_failures"`
	LastRun    *time.Time `json:"last_run"`
	LastStatus string     `json:"last_status"`
	OpenJobs   int        `json:"open_jobs"`
	Note       string     `json:"note,omitempty"`
}

// SourceHealth lists every source with its last run and open job count.
func (s *Store) SourceHealth(ctx context.Context) ([]SourceHealth, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.name, c.slug, s.kind, s.enabled, s.consecutive_failures, s.last_run_at,
		       coalesce((SELECT r.status FROM scrape_runs r WHERE r.source_id = s.id
		                 ORDER BY r.started_at DESC LIMIT 1), ''),
		       (SELECT count(*) FROM job_postings p JOIN jobs j ON j.id = p.job_id
		        WHERE p.source_id = s.id AND p.removed_at IS NULL AND j.closed_at IS NULL),
		       coalesce(s.disabled_reason, '')
		FROM sources s JOIN companies c ON c.id = s.company_id
		ORDER BY s.enabled DESC, 8 DESC, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceHealth{}
	for rows.Next() {
		var h SourceHealth
		if err := rows.Scan(&h.Company, &h.Slug, &h.Kind, &h.Enabled, &h.Failures, &h.LastRun, &h.LastStatus, &h.OpenJobs, &h.Note); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.pool.QueryRow(ctx, `SELECT 1`).Scan(&one)
}
