package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// jobSpec describes a throwaway job for the search tests.
type jobSpec struct {
	id, title, desc string
	city            string
	remote, senior  string
	employment      string
	expMin, expMax  *int
	salary          bool
	posted          time.Time
}

func (j jobSpec) posting() PostingInput {
	p := mk(j.id, j.title, "h-"+j.id)
	p.Job.Fingerprint = "fp-" + j.id
	p.Job.Description = j.desc
	p.Job.NormVersion = 1
	p.Job.PostedAt = &j.posted
	if j.city != "" {
		p.Job.City, p.Job.Country = sp(j.city), sp("IN")
		p.Job.Places = []Place{{City: j.city, Region: "Karnataka", Country: "IN"}}
	}
	if j.remote != "" {
		p.Job.RemoteType = sp(j.remote)
	}
	if j.senior != "" {
		p.Job.Seniority = sp(j.senior)
	}
	if j.employment != "" {
		p.Job.EmploymentType = sp(j.employment)
	}
	p.Job.ExpMin, p.Job.ExpMax = j.expMin, j.expMax
	if j.salary {
		lo, hi := 2400000.0, 3600000.0
		p.Job.SalaryMin, p.Job.SalaryMax = &lo, &hi
		p.Job.SalaryCurrency, p.Job.SalaryPeriod = sp("INR"), sp("year")
	}
	return p
}

func TestSearchJobsFiltersSortAndPaging(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "search")
	const slug = "zz-itest-search"

	// A throwaway city with an alias, so alias resolution is exercised.
	if _, _, err := s.SeedLocations(ctx, []CityRef{{Name: "Zz Searchcity", Region: "Karnataka", Country: "IN", Aliases: []string{"zzsc"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM job_locations WHERE location_id IN (SELECT id FROM locations WHERE city = 'Zz Searchcity')`,
			`DELETE FROM city_aliases WHERE alias IN ('zz searchcity', 'zzsc')`,
			`DELETE FROM locations WHERE city = 'Zz Searchcity'`,
		} {
			s.pool.Exec(ctx, q)
		}
	})

	now := time.Now()
	jobs := []jobSpec{
		{id: "1", title: "Senior Go Backend Engineer", desc: "We use zzquokka and Postgres.\n\n   Lots   of   whitespace here.",
			city: "Zz Searchcity", remote: "hybrid", senior: "senior", employment: "full_time",
			expMin: ip(5), expMax: ip(8), salary: true, posted: now.Add(-2 * 24 * time.Hour)},
		{id: "2", title: "Junior Designer", desc: "Design things with zzquokka flair.",
			city: "Zz Searchcity", remote: "onsite", senior: "junior", employment: "intern",
			expMin: ip(0), posted: now.Add(-40 * 24 * time.Hour)},
		{id: "3", title: "Data Analyst", desc: "Spreadsheets only.",
			remote: "remote", posted: now.Add(-1 * 24 * time.Hour)}, // no city, no experience stated
		{id: "4", title: "Staff Engineer", desc: "Architecture. zzquokka everywhere.",
			city: "Zz Searchcity", senior: "staff", expMin: ip(12), posted: now.Add(-5 * 24 * time.Hour)},
	}
	var in []PostingInput
	for _, j := range jobs {
		in = append(in, j.posting())
	}
	if res, err := s.SyncPostings(ctx, src, in, SyncOptions{}); err != nil || res.New != 4 {
		t.Fatalf("seed: %+v, %v", res, err)
	}

	search := func(p SearchParams) SearchResult {
		t.Helper()
		p.Company = slug // isolate from real data
		if p.Limit == 0 {
			p.Limit = 50
		}
		r, err := s.SearchJobs(ctx, p)
		if err != nil {
			t.Fatalf("SearchJobs(%+v): %v", p, err)
		}
		return r
	}
	ids := func(r SearchResult) string {
		out := ""
		for _, j := range r.Jobs {
			out += j.URL[len(j.URL)-1:] // urls end in the job id digit
		}
		return out
	}

	// default: newest first (3 is 1 day old, 1 is 2, 4 is 5, 2 is 40)
	r := search(SearchParams{})
	if r.Total != 4 || ids(r) != "3142" {
		t.Fatalf("default order: total %d ids %q, want 4 / 3142", r.Total, ids(r))
	}

	// text search (description) and web-search syntax
	if r := search(SearchParams{Q: "zzquokka"}); r.Total != 3 {
		t.Errorf("q=zzquokka: total %d, want 3", r.Total)
	}
	if r := search(SearchParams{Q: "zzquokka -designer"}); r.Total != 2 {
		t.Errorf("q with exclusion: total %d, want 2", r.Total)
	}
	if r := search(SearchParams{Q: `"go backend"`}); r.Total != 1 {
		t.Errorf("phrase search: total %d, want 1", r.Total)
	}
	if r := search(SearchParams{Q: "nosuchwordanywhere"}); r.Total != 0 || r.Jobs == nil {
		t.Errorf("no match must be an empty list, not null: %+v", r)
	}
	if r := search(SearchParams{Q: "'; DROP TABLE jobs; --"}); r.Total != 0 {
		t.Errorf("hostile q must just match nothing, got %d", r.Total)
	}
	// relevance puts the title match first
	if r := search(SearchParams{Q: "engineer", Sort: SortRelevance}); r.Total != 2 {
		t.Errorf("q=engineer: total %d, want 2 (jobs 1 and 4)", r.Total)
	}

	// city by canonical name, alias and case
	for _, c := range []string{"Zz Searchcity", "zzsc", "ZZSC", "zz searchcity"} {
		if r := search(SearchParams{City: c}); r.Total != 3 {
			t.Errorf("city=%q: total %d, want 3", c, r.Total)
		}
	}
	if r := search(SearchParams{City: "Atlantis"}); r.Total != 0 {
		t.Errorf("unknown city: %d", r.Total)
	}

	// enums
	if r := search(SearchParams{Remote: "hybrid"}); r.Total != 1 {
		t.Errorf("remote=hybrid: %d", r.Total)
	}
	if r := search(SearchParams{Seniority: "staff"}); r.Total != 1 {
		t.Errorf("seniority=staff: %d", r.Total)
	}
	if r := search(SearchParams{Employment: "intern"}); r.Total != 1 {
		t.Errorf("employment=intern: %d", r.Total)
	}
	if r := search(SearchParams{Country: "IN"}); r.Total != 3 {
		t.Errorf("country=IN: %d, want 3 (job 3 has no place)", r.Total)
	}

	// experience: "I have 6 years" hides the staff role (12) but keeps unknowns
	six := 6
	if r := search(SearchParams{Exp: &six}); r.Total != 3 {
		t.Errorf("exp=6: total %d, want 3 (jobs 1, 2 and the unstated 3)", r.Total)
	}
	if r := search(SearchParams{Exp: &six, HideUnknownExp: true}); r.Total != 2 {
		t.Errorf("exp=6 hiding unknown: total %d, want 2", r.Total)
	}

	// recency and salary
	if r := search(SearchParams{PostedWithinDays: 7}); r.Total != 3 {
		t.Errorf("posted within 7 days: total %d, want 3 (job 2 is 40 days old)", r.Total)
	}
	if r := search(SearchParams{HasSalary: true}); r.Total != 1 {
		t.Errorf("has_salary: %d", r.Total)
	}

	// paging: total stays the full match count
	p1 := search(SearchParams{Limit: 2, Offset: 0})
	p2 := search(SearchParams{Limit: 2, Offset: 2})
	p3 := search(SearchParams{Limit: 2, Offset: 4})
	if p1.Total != 4 || len(p1.Jobs) != 2 || len(p2.Jobs) != 2 || len(p3.Jobs) != 0 || ids(p1)+ids(p2) != "3142" {
		t.Errorf("paging: %d/%d %q %q %d", p1.Total, len(p1.Jobs), ids(p1), ids(p2), len(p3.Jobs))
	}

	// result fields
	one := search(SearchParams{Q: "backend"}).Jobs
	if len(one) != 1 {
		t.Fatalf("expected the backend job, got %d", len(one))
	}
	j := one[0]
	if j.Company.Slug != slug || j.Salary == nil || j.Salary.Currency != "INR" || j.Salary.Min != 2400000 ||
		j.ExpMin == nil || *j.ExpMin != 5 || j.ExpMax == nil || *j.ExpMax != 8 || j.LocationCount != 1 ||
		j.City == nil || *j.City != "Zz Searchcity" || j.URL == "" || j.PostedAt == nil {
		t.Errorf("summary fields wrong: %+v", j)
	}
	if want := "We use zzquokka and Postgres. Lots of whitespace here."; j.Snippet != want {
		t.Errorf("snippet should collapse whitespace: %q", j.Snippet)
	}

	// a text query returns the passage around the match, matched words marked
	hit := search(SearchParams{Q: "zzquokka"}).Jobs
	if len(hit) == 0 || !strings.Contains(hit[0].Snippet, "⟦zzquokka⟧") {
		t.Errorf("snippet should mark the matched word with ⟦ ⟧: %+v", hit)
	}

	// closed jobs disappear from search
	if _, err := s.pool.Exec(ctx, `UPDATE jobs SET closed_at = now() WHERE company_id = $1 AND title = 'Data Analyst'`, src.CompanyID); err != nil {
		t.Fatal(err)
	}
	if r := search(SearchParams{}); r.Total != 3 {
		t.Errorf("closed job must not be listed: total %d", r.Total)
	}
}

func TestJobDetailFacetsStatsAndSourceHealth(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "detail")
	const slug = "zz-itest-detail"

	spec := jobSpec{id: "1", title: "Platform Engineer", desc: "Full description text.", city: "Pune",
		remote: "remote", senior: "senior", posted: time.Now().Add(-time.Hour)}
	if res, err := s.SyncPostings(ctx, src, []PostingInput{spec.posting()}, SyncOptions{}); err != nil || res.New != 1 {
		t.Fatalf("seed: %+v, %v", res, err)
	}
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE company_id = $1`, src.CompanyID).Scan(&id); err != nil {
		t.Fatal(err)
	}

	d, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Description != "Full description text." || d.Title != "Platform Engineer" || d.ClosedAt != nil {
		t.Errorf("detail: %+v", d)
	}
	if len(d.Places) != 1 || d.Places[0].City == nil || *d.Places[0].City != "Pune" || d.Places[0].Country != "IN" {
		t.Errorf("places: %+v", d.Places)
	}
	if len(d.Postings) != 1 || d.Postings[0].Source != "greenhouse" || !d.Postings[0].Active || d.Postings[0].URL == "" {
		t.Errorf("postings: %+v", d.Postings)
	}

	if _, err := s.GetJob(ctx, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing job should be ErrNotFound, got %v", err)
	}

	f, err := s.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var foundCo bool
	for _, c := range f.Companies {
		if c.Value == slug && c.Count == 1 && c.Label == "Integration Test Co" {
			foundCo = true
		}
	}
	if !foundCo {
		t.Errorf("facets must list the company with its count and name: %+v", f.Companies)
	}
	if f.Cities == nil || f.Remote == nil || f.Seniority == nil || f.Employment == nil {
		t.Error("facet lists must be empty arrays, never null")
	}

	st, err := s.PublicStats(ctx)
	if err != nil || st.OpenJobs < 1 || st.Companies < 1 {
		t.Errorf("stats: %+v, %v", st, err)
	}

	health, err := s.SourceHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, h := range health {
		if h.Slug == slug {
			seen = true
			if h.OpenJobs != 1 || !h.Enabled || h.Kind != "greenhouse" {
				t.Errorf("source health: %+v", h)
			}
		}
	}
	if !seen {
		t.Error("the source must appear in source health")
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("ping: %v", err)
	}

	// a closed job is still viewable by id, with its closing time
	if _, err := s.pool.Exec(ctx, `UPDATE jobs SET closed_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if d, err := s.GetJob(ctx, id); err != nil || d.ClosedAt == nil {
		t.Errorf("closed job detail: %+v, %v", d, err)
	}
}

// The fresher / entry-level rule is the part of search users rely on most, so it is
// tested against the real SQL with the awkward cases: a senior title that states
// one year, a junior title that states four, and jobs with no signal at all.
func TestEntryLevelFilterAndQuickCounts(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "entry")
	const slug = "zz-itest-entry"
	before, err := s.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	specs := []struct {
		jobSpec
		entry bool
	}{
		{jobSpec{id: "1", title: "Associate Engineer", senior: "junior", expMin: ip(0)}, true},
		{jobSpec{id: "2", title: "Product Intern", senior: "intern"}, true}, // no years stated, intern
		{jobSpec{id: "3", title: "Senior Support Specialist", senior: "senior"}, false},
		{jobSpec{id: "4", title: "Staff Engineer", senior: "staff", expMin: ip(1)}, false}, // senior title wins
		{jobSpec{id: "5", title: "Analyst", expMin: ip(1)}, true},                          // stated 1 year, no level
		{jobSpec{id: "6", title: "Manager"}, false},                                        // no signal at all
		{jobSpec{id: "7", title: "Specialist", senior: "mid", expMin: ip(3)}, false},
		{jobSpec{id: "8", title: "Junior Associate", senior: "junior", expMin: ip(4)}, false}, // junior but asks 4 years
	}
	want := map[string]bool{}
	var in []PostingInput
	for _, sp := range specs {
		sp.posted = now
		sp.desc = "entry level test"
		in = append(in, sp.posting())
		want[sp.id] = sp.entry
	}
	if res, err := s.SyncPostings(ctx, src, in, SyncOptions{}); err != nil || res.New != len(specs) {
		t.Fatalf("seed: %+v, %v", res, err)
	}

	res, err := s.SearchJobs(ctx, SearchParams{Company: slug, EntryLevel: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, j := range res.Jobs {
		got[j.URL[len(j.URL)-1:]] = true
	}
	for id, isEntry := range want {
		if got[id] != isEntry {
			t.Errorf("job %s: entry-level = %v, want %v", id, got[id], isEntry)
		}
	}
	if res.Total != 3 {
		t.Errorf("entry-level total %d, want 3 (jobs 1, 2, 5)", res.Total)
	}

	// The quick-view counts use the same rule: seeding 3 entry-level jobs, 1 intern
	// and 8 new jobs moves the counts by exactly that (compared with the live data,
	// which no other test touches).
	after, err := s.Facets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d := after.Quick.EntryLevel - before.Quick.EntryLevel; d != 3 {
		t.Errorf("entry-level count moved by %d, want 3", d)
	}
	if d := after.Quick.Internships - before.Quick.Internships; d != 1 {
		t.Errorf("internships count moved by %d, want 1", d)
	}
	if d := after.Quick.New7Days - before.Quick.New7Days; d != len(specs) {
		t.Errorf("new-this-week count moved by %d, want %d", d, len(specs))
	}
}
