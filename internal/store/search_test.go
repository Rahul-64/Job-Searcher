package store

import (
	"regexp"
	"strings"
	"testing"
)

func TestBuildSearchDefaults(t *testing.T) {
	sql, args := buildSearch(SearchParams{Limit: 20, Offset: 0})
	if !strings.Contains(sql, "j.closed_at IS NULL") {
		t.Error("only open jobs may be searched")
	}
	if !strings.Contains(sql, "ORDER BY coalesce(j.posted_at, j.first_seen_at) DESC, j.id DESC") {
		t.Errorf("default order should be newest first: %s", sql)
	}
	if len(args) != 2 || args[0] != 20 || args[1] != 0 {
		t.Errorf("limit/offset must be bound parameters: %v", args)
	}
	if strings.Contains(sql, "websearch_to_tsquery") {
		t.Error("no text query, no full-text clause")
	}
}

// whereOf returns the filtering part of a statement. The SELECT list also carries
// the entry-level flag for every row, so the tests must not look at the whole text.
func whereOf(sql string) string {
	// The SELECT list has sub-queries with their own WHERE, so start at the main FROM.
	_, w, _ := strings.Cut(sql, "FROM jobs j JOIN companies c ON c.id = j.company_id")
	w, _, _ = strings.Cut(w, "ORDER BY ")
	return w
}

func TestBuildSearchEntryLevel(t *testing.T) {
	sql, args := buildSearch(SearchParams{EntryLevel: true, Limit: 20})
	where := whereOf(sql)
	if !strings.Contains(where, "'intern', 'junior'") || !strings.Contains(where, "exp_min_years <= 1") {
		t.Errorf("entry-level condition missing from WHERE: %s", where)
	}
	for _, senior := range []string{"'senior'", "'lead'", "'staff'", "'principal'", "'director'", "'executive'"} {
		if !strings.Contains(where, senior) {
			t.Errorf("entry level must exclude %s", senior)
		}
	}
	selectList, _, _ := strings.Cut(sql, "FROM jobs j")
	if !strings.Contains(selectList, "'intern', 'junior'") {
		t.Error("every row should carry the entry-level flag in the SELECT list")
	}
	if len(args) != 2 { // only limit and offset: the rule is a constant, not user input
		t.Errorf("entry level must not add bound values: %v", args)
	}
	if sql, _ := buildSearch(SearchParams{Limit: 20}); strings.Contains(whereOf(sql), "'intern', 'junior'") {
		t.Error("entry-level condition must be absent from WHERE unless asked for")
	}
}

func TestBuildSearchBindsEveryValue(t *testing.T) {
	exp := 5
	nasty := `x'); DROP TABLE jobs; --`
	p := SearchParams{
		Q: nasty, City: nasty, Country: "IN", Remote: "hybrid", Seniority: "senior",
		Employment: "full_time", Company: nasty, Exp: &exp, PostedWithinDays: 7, HasSalary: true,
		Sort: SortRelevance, Limit: 20, Offset: 40,
	}
	sql, args := buildSearch(p)

	// Hostile text must only ever appear in args, never in the statement.
	if strings.Contains(sql, "DROP") || strings.Contains(sql, "x'") {
		t.Fatalf("user text leaked into SQL: %s", sql)
	}
	found := 0
	for _, a := range args {
		if a == nasty {
			found++
		}
	}
	if found != 3 { // q, city, company (city is bound once and referenced twice)
		t.Errorf("expected the hostile string bound exactly 3 times, got %d (args %v)", found, args)
	}
	// Every placeholder in the SQL must have an argument, and vice versa.
	max := 0
	for _, m := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(sql, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n > max {
			max = n
		}
	}
	if max != len(args) {
		t.Errorf("highest placeholder $%d but %d args", max, len(args))
	}
	for _, want := range []string{"j.country =", "j.remote_type =", "j.seniority =", "j.employment_type =",
		"c.slug =", "j.exp_min_years <=", "make_interval(days =>", "j.salary_min IS NOT NULL", "ts_rank("} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing clause %q", want)
		}
	}
}

func TestBuildSearchRelevanceNeedsAQuery(t *testing.T) {
	sql, _ := buildSearch(SearchParams{Sort: SortRelevance, Limit: 10})
	if strings.Contains(sql, "ts_rank") {
		t.Error("relevance order without a query must fall back to newest")
	}
	sql, _ = buildSearch(SearchParams{Q: "go", Sort: SortNewest, Limit: 10})
	if strings.Contains(sql, "ts_rank") {
		t.Error("newest order must not rank")
	}
}

func TestBuildSearchExperienceSemantics(t *testing.T) {
	exp := 3
	sqlLoose, _ := buildSearch(SearchParams{Exp: &exp, Limit: 1})
	loose := whereOf(sqlLoose)
	if !strings.Contains(loose, "(j.exp_min_years IS NULL OR j.exp_min_years <=") {
		t.Errorf("by default jobs that state no requirement stay visible: %s", loose)
	}
	sqlStrict, _ := buildSearch(SearchParams{Exp: &exp, HideUnknownExp: true, Limit: 1})
	strict := whereOf(sqlStrict)
	if strings.Contains(strict, "exp_min_years IS NULL") || !strings.Contains(strict, "j.exp_min_years <=") {
		t.Errorf("HideUnknownExp must drop unknowns: %s", strict)
	}
	sqlNone, _ := buildSearch(SearchParams{Limit: 1})
	none := whereOf(sqlNone)
	if strings.Contains(none, "exp_min_years <=") {
		t.Error("no Exp, no experience clause")
	}
}
