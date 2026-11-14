package store

import (
	"context"
	"testing"
	"time"

	"jobscraper/internal/config"
)

// Integration test against the real database. It is skipped when DATABASE_URL
// is not configured. It works only on rows it creates (a throwaway company
// "zz-sync-test") and removes them afterwards.
func TestSyncPostingsLifecycle(t *testing.T) {
	cfg, err := config.Load()
	if err != nil || cfg.DatabaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs last: the row cleanup below still needs the pool.
	t.Cleanup(pool.Close)
	s := New(pool)

	const slug = "zz-sync-test"
	cleanup := func() {
		for _, q := range []string{
			`DELETE FROM posting_raw WHERE posting_id IN (SELECT p.id FROM job_postings p JOIN sources s ON s.id=p.source_id JOIN companies c ON c.id=s.company_id WHERE c.slug=$1)`,
			`DELETE FROM job_postings WHERE source_id IN (SELECT s.id FROM sources s JOIN companies c ON c.id=s.company_id WHERE c.slug=$1)`,
			`DELETE FROM jobs WHERE company_id IN (SELECT id FROM companies WHERE slug=$1)`,
			`DELETE FROM scrape_runs WHERE source_id IN (SELECT s.id FROM sources s JOIN companies c ON c.id=s.company_id WHERE c.slug=$1)`,
			`DELETE FROM sources WHERE company_id IN (SELECT id FROM companies WHERE slug=$1)`,
			`DELETE FROM companies WHERE slug=$1`,
		} {
			if _, err := pool.Exec(context.Background(), q, slug); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	companyID, err := s.UpsertCompany(ctx, "Sync Test Co", slug)
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.UpsertSource(ctx, companyID, "greenhouse", "zz-sync-test", "http://example.invalid")
	if err != nil {
		t.Fatal(err)
	}

	mk := func(id, title, hash string) PostingInput {
		return PostingInput{
			ExternalID: id, URL: "http://x/" + id, TitleRaw: title, LocationRaw: "Pune, India",
			ContentHash: hash, RawGzip: []byte("gz-" + hash),
			Job: JobFields{Title: title, TitleNormalized: title, Description: "Go and Postgres",
				LocationRaw: "Pune, India", Fingerprint: "fp-" + id},
		}
	}
	count := func(q string) int {
		var n int
		if err := pool.QueryRow(ctx, q, src.ID, companyID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	openJobs := func() int {
		return count(`SELECT count(*) FROM jobs WHERE company_id=$2 AND closed_at IS NULL AND $1::bigint IS NOT NULL`)
	}
	activePostings := func() int {
		return count(`SELECT count(*) FROM job_postings WHERE source_id=$1 AND removed_at IS NULL AND $2::bigint IS NOT NULL`)
	}

	// 1) first run: two new jobs
	res, err := s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend", "h1"), mk("2", "Frontend", "h2")})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 2 || res.Updated != 0 || res.Unchanged != 0 || res.Closed != 0 {
		t.Fatalf("first run: %+v", res)
	}
	if openJobs() != 2 || activePostings() != 2 {
		t.Fatalf("expected 2 open jobs/postings, got %d/%d", openJobs(), activePostings())
	}
	if n := count(`SELECT count(*) FROM posting_raw WHERE posting_id IN (SELECT id FROM job_postings WHERE source_id=$1) AND $2::bigint IS NOT NULL`); n != 2 {
		t.Fatalf("expected 2 raw payloads, got %d", n)
	}

	// 2) identical run: idempotent, nothing new
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend", "h1"), mk("2", "Frontend", "h2")})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Updated != 0 || res.Unchanged != 2 || res.Closed != 0 {
		t.Fatalf("idempotent run: %+v", res)
	}
	if n := count(`SELECT count(*) FROM jobs WHERE company_id=$2 AND $1::bigint IS NOT NULL`); n != 2 {
		t.Fatalf("re-run must not duplicate jobs, got %d", n)
	}

	// 3) job 1 changes, job 2 disappears, job 3 appears
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend v2", "h1b"), mk("3", "Data", "h3")})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 1 || res.Updated != 1 || res.Closed != 1 {
		t.Fatalf("mixed run: %+v", res)
	}
	var title string
	if err := pool.QueryRow(ctx, `SELECT j.title FROM jobs j JOIN job_postings p ON p.job_id=j.id WHERE p.source_id=$1 AND p.external_id='1'`, src.ID).Scan(&title); err != nil || title != "Backend v2" {
		t.Fatalf("changed title not applied: %q, %v", title, err)
	}
	if openJobs() != 2 {
		t.Fatalf("expected 2 open jobs (1,3), got %d", openJobs())
	}

	// 4) source returns nothing: must NOT close everything
	res, err = s.SyncPostings(ctx, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SkippedClose || res.Closed != 0 || res.ActiveBefore != 2 {
		t.Fatalf("empty-fetch guard: %+v", res)
	}
	if openJobs() != 2 {
		t.Fatalf("jobs must stay open after an empty fetch, got %d open", openJobs())
	}

	// 5) job 2 reappears: reopened, not duplicated
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend v2", "h1b"), mk("2", "Frontend", "h2"), mk("3", "Data", "h3")})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Closed != 0 {
		t.Fatalf("reappear run: %+v", res)
	}
	if openJobs() != 3 || activePostings() != 3 {
		t.Fatalf("expected 3 open after reappear, got %d/%d", openJobs(), activePostings())
	}
	if n := count(`SELECT count(*) FROM jobs WHERE company_id=$2 AND $1::bigint IS NOT NULL`); n != 3 {
		t.Fatalf("reappearing job must reuse its row, got %d jobs total", n)
	}
}
