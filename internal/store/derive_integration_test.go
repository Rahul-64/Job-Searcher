package store

import (
	"fmt"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

// mkv builds a posting with a chosen fingerprint, title, places and version.
func mkv(id, fp string, places ...Place) PostingInput {
	p := mk(id, "Job "+id, "h-"+id)
	p.Job.Fingerprint = fp
	p.Job.Places = places
	p.Job.NormVersion = 1
	if len(places) > 0 {
		p.Job.City, p.Job.Region, p.Job.Country = sp(places[0].City), sp(places[0].Region), sp(places[0].Country)
	}
	return p
}

func TestIdenticalPostingsShareOneJob(t *testing.T) {
	ctx, s, src, count := newEnv(t, "linking")

	// Two openings of the same role in one run, plus a distinct role.
	res, err := s.SyncPostings(ctx, src, []PostingInput{
		mkv("1", "fp-same"), mkv("2", "fp-same"), mkv("3", "fp-other"),
	}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 3 {
		t.Fatalf("3 postings are new: %+v", res)
	}
	if n := count(qAllJobs); n != 2 {
		t.Fatalf("identical fingerprints must share a job: want 2 jobs, got %d", n)
	}
	if n := count(qActivePst); n != 3 {
		t.Fatalf("all 3 postings stay: got %d", n)
	}

	// A later posting with the same fingerprint attaches to the existing job.
	if _, err := s.SyncPostings(ctx, src, []PostingInput{
		mkv("1", "fp-same"), mkv("2", "fp-same"), mkv("3", "fp-other"), mkv("4", "fp-same"),
	}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := count(qAllJobs); n != 2 {
		t.Fatalf("a new identical posting must not create a job, got %d jobs", n)
	}

	// The job stays open while any of its postings is listed.
	if _, err := s.SyncPostings(ctx, src, []PostingInput{mkv("3", "fp-other"), mkv("4", "fp-same")}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := count(qOpenJobs); n != 2 {
		t.Fatalf("job fp-same still has posting 4 listed, want 2 open jobs, got %d", n)
	}
	// ...and closes when the last posting goes.
	if _, err := s.SyncPostings(ctx, src, []PostingInput{mkv("3", "fp-other")}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := count(qOpenJobs); n != 1 {
		t.Fatalf("want 1 open job after the last fp-same posting went, got %d", n)
	}
}

func TestOutOfScopePostingsAreClosedWithoutTrippingTheGuard(t *testing.T) {
	ctx, s, src, count := newEnv(t, "scope")

	// 40 postings, all stored while the scope was "everything".
	var all []PostingInput
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("p%02d", i)
		all = append(all, mkv(id, "fp-"+id))
	}
	if res, err := s.SyncPostings(ctx, src, all, SyncOptions{}); err != nil || res.New != 40 {
		t.Fatalf("seed: %+v, %v", res, err)
	}

	// Scope narrows: 24 stay in scope, 16 are filtered out by the pipeline.
	const inScope = 24
	var oos []string
	for _, p := range all[inScope:] {
		oos = append(oos, p.ExternalID)
	}
	res, err := s.SyncPostings(ctx, src, all[:inScope], SyncOptions{OutOfScope: oos})
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedClose {
		t.Fatalf("narrowing the scope must not look like a source collapse: %+v", res)
	}
	if res.Closed != 16 || res.OutOfScope != 16 || res.ActiveBefore != inScope {
		t.Fatalf("expected 16 closed, 16 out of scope, baseline 24: %+v", res)
	}
	if n := count(qOpenJobs); n != inScope {
		t.Fatalf("want %d open jobs, got %d", inScope, n)
	}
	// Closed, not deleted: nothing is destroyed by changing the scope.
	if n := count(qAllJobs); n != 40 {
		t.Fatalf("out-of-scope jobs must be closed, not deleted; want 40 rows, got %d", n)
	}

	// A genuine collapse of an in-scope board is still caught.
	res, err = s.SyncPostings(ctx, src, all[:2], SyncOptions{OutOfScope: oos})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SkippedClose || res.Closed != 0 {
		t.Fatalf("losing 22 of 24 in-scope postings must still trip the guard: %+v", res)
	}
	if n := count(qOpenJobs); n != inScope {
		t.Fatalf("the guard must keep all %d in-scope jobs open, got %d", inScope, n)
	}
}

func TestLocationsAreStoredOnceAndReplacedOnChange(t *testing.T) {
	ctx, s, src, count := newEnv(t, "places")
	blr := Place{City: "Bengaluru", Region: "Karnataka", Country: "IN"}
	pune := Place{City: "Pune", Region: "Maharashtra", Country: "IN"}
	inOnly := Place{Country: "IN"}

	// Two jobs sharing places, one of them country-level (NULL city / region).
	a, b := mkv("1", "fp-1", blr, pune), mkv("2", "fp-2", inOnly)
	c := mkv("3", "fp-3", inOnly) // a second country-only place must reuse the row
	if _, err := s.SyncPostings(ctx, src, []PostingInput{a, b, c}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	var nLoc int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM locations WHERE country='IN' AND region IS NULL AND city IS NULL`).Scan(&nLoc); err != nil {
		t.Fatal(err)
	}
	if nLoc != 1 {
		t.Fatalf("a country-only place must exist once (NULLS NOT DISTINCT), got %d", nLoc)
	}
	jl := `SELECT count(*) FROM job_locations jl JOIN jobs j ON j.id = jl.job_id WHERE j.company_id = $2 AND $1::bigint IS NOT NULL`
	if n := count(jl); n != 4 { // job1: 2 places, job2: 1, job3: 1
		t.Fatalf("expected 4 job_locations, got %d", n)
	}

	// Content change on job 1: its places are replaced, not appended.
	a2 := mkv("1", "fp-1", blr)
	a2.ContentHash = "changed"
	res, err := s.SyncPostings(ctx, src, []PostingInput{a2, b, c}, SyncOptions{})
	if err != nil || res.Updated != 1 {
		t.Fatalf("update run: %+v, %v", res, err)
	}
	if n := count(jl); n != 3 {
		t.Fatalf("job 1 shrank from 2 places to 1, want 3 job_locations in total, got %d", n)
	}
}

func TestRenormalizeFlowAndDedup(t *testing.T) {
	ctx, s, src, count := newEnv(t, "renorm")

	// Only look at jobs created by this test: the database also holds real jobs
	// whose raw payloads we must not drag over the network.
	var base int64
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM jobs`).Scan(&base); err != nil {
		t.Fatal(err)
	}

	// Four jobs at an old normalization version (0). Two will turn out identical.
	old := func(id string) PostingInput {
		p := mk(id, "Job "+id, "h-"+id)
		p.Job.Fingerprint = "old-" + id
		p.Job.NormVersion = 0
		return p
	}
	if res, err := s.SyncPostings(ctx, src, []PostingInput{old("1"), old("2"), old("3"), old("4")}, SyncOptions{}); err != nil || res.New != 4 {
		t.Fatalf("seed: %+v, %v", res, err)
	}
	n, err := s.CountRenormalizable(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n < 4 {
		t.Fatalf("at least our 4 jobs are stale, got %d", n)
	}

	rows, err := s.ListRenormalizable(ctx, 1, base, 100)
	if err != nil {
		t.Fatal(err)
	}
	mine := map[int64]RenormRow{}
	for _, r := range rows {
		if r.CompanyID == src.CompanyID {
			mine[r.JobID] = r
		}
	}
	if len(mine) != 4 {
		t.Fatalf("expected our 4 jobs to be listed with payloads, got %d", len(mine))
	}
	for _, r := range mine {
		if r.Kind != "greenhouse" || len(r.Body) == 0 {
			t.Fatalf("row must carry the source kind and raw payload: %+v", r)
		}
	}

	// Re-derive: jobs 1 and 2 become identical (same fingerprint), 3 and 4 differ.
	byTitle := map[string]int64{}
	for id := range mine {
		var title string
		if err := s.pool.QueryRow(ctx, `SELECT title FROM jobs WHERE id=$1`, id).Scan(&title); err != nil {
			t.Fatal(err)
		}
		byTitle[title] = id
	}
	mkUp := func(title, fp string) NormUpdate {
		return NormUpdate{JobID: byTitle[title], Job: JobFields{
			TitleNormalized: strings.ToLower(title), Fingerprint: fp, NormVersion: 1,
			Country: sp("IN"), City: sp("Pune"), Region: sp("Maharashtra"),
			Seniority: sp("senior"), RemoteType: sp("hybrid"), ExpMin: ip(5),
			Places: []Place{{City: "Pune", Region: "Maharashtra", Country: "IN"}},
		}}
	}
	if err := s.ApplyNormalization(ctx, []NormUpdate{
		mkUp("Job 1", "new-same"), mkUp("Job 2", "new-same"), mkUp("Job 3", "new-3"), mkUp("Job 4", "new-4"),
	}); err != nil {
		t.Fatal(err)
	}
	var seniority, remote *string
	var exp *int
	if err := s.pool.QueryRow(ctx, `SELECT seniority, remote_type, exp_min_years FROM jobs WHERE id=$1`, byTitle["Job 3"]).Scan(&seniority, &remote, &exp); err != nil {
		t.Fatal(err)
	}
	if seniority == nil || *seniority != "senior" || remote == nil || *remote != "hybrid" || exp == nil || *exp != 5 {
		t.Fatalf("derived fields not written: %v %v %v", seniority, remote, exp)
	}
	// A re-derived job no longer needs re-deriving.
	after, _ := s.ListRenormalizable(ctx, 1, base, 100)
	for _, r := range after {
		if r.CompanyID == src.CompanyID {
			t.Fatalf("job %d should be up to date", r.JobID)
		}
	}

	// Dedup merges 1 and 2 (same fingerprint at the current version), keeps 3 and 4.
	d, err := s.Dedup(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var ours int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE company_id=$1`, src.CompanyID).Scan(&ours); err != nil {
		t.Fatal(err)
	}
	if ours != 3 || d.Removed < 1 {
		t.Fatalf("expected 3 jobs after merging two identical ones (removed %d), got %d", d.Removed, ours)
	}
	if n := count(qActivePst); n != 4 {
		t.Fatalf("merging jobs must not lose postings: want 4 active postings, got %d", n)
	}
	var multi int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT job_id FROM job_postings WHERE source_id=$1 GROUP BY job_id HAVING count(*) = 2) x`, src.ID).Scan(&multi); err != nil {
		t.Fatal(err)
	}
	if multi != 1 {
		t.Fatalf("the survivor should hold both postings, found %d jobs with 2 postings", multi)
	}
	// Idempotent.
	if d2, err := s.Dedup(ctx, 1); err != nil || d2.Removed != 0 && d2.Groups == 0 {
		t.Fatalf("second dedup: %+v, %v", d2, err)
	}
}

func TestDedupIgnoresOtherNormalizationVersions(t *testing.T) {
	ctx, s, src, count := newEnv(t, "dedupver")
	a, b := mkv("1", "fp-same"), mkv("2", "fp-other")
	a.Job.NormVersion, b.Job.NormVersion = 1, 0
	if _, err := s.SyncPostings(ctx, src, []PostingInput{a, b}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	// Make job 2's fingerprint collide with job 1, but at an older version.
	if _, err := s.pool.Exec(ctx, `UPDATE jobs SET fingerprint='fp-same' WHERE company_id=$1 AND norm_version=0`, src.CompanyID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dedup(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := count(qAllJobs); n != 2 {
		t.Fatalf("jobs from other rule versions must not be merged, got %d jobs", n)
	}
}

func TestSeedLocationsIsIdempotent(t *testing.T) {
	ctx, s, _, _ := newEnv(t, "seedloc")
	cities := []CityRef{
		{Name: "Zz Testcity", Region: "Karnataka", Country: "IN", Aliases: []string{"zztest alias", "zz-blr"}},
	}
	defer s.pool.Exec(ctx, `DELETE FROM city_aliases WHERE alias IN ('zz testcity','zztest alias','zz-blr')`)
	defer s.pool.Exec(ctx, `DELETE FROM locations WHERE city='Zz Testcity'`)

	for i := 0; i < 2; i++ {
		locs, aliases, err := s.SeedLocations(ctx, cities)
		if err != nil || locs != 1 || aliases != 3 {
			t.Fatalf("run %d: %d locations, %d aliases, %v", i, locs, aliases, err)
		}
	}
	var nLoc, nAlias int
	s.pool.QueryRow(ctx, `SELECT count(*) FROM locations WHERE city='Zz Testcity'`).Scan(&nLoc)
	s.pool.QueryRow(ctx, `SELECT count(*) FROM city_aliases a JOIN locations l ON l.id=a.location_id WHERE l.city='Zz Testcity'`).Scan(&nAlias)
	if nLoc != 1 || nAlias != 3 {
		t.Fatalf("seeding twice must not duplicate: %d locations, %d aliases", nLoc, nAlias)
	}
}
