package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"jobscraper/internal/config"
)

// Integration tests against the real database. They are skipped when
// DATABASE_URL is not configured. Each test works only on rows it creates (a
// throwaway company "zz-itest-<name>") and removes them afterwards.

func newEnv(t *testing.T, name string) (context.Context, *Store, SourceRow, func(string, ...any) int) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping database integration test in -short mode")
	}
	cfg, err := config.Load()
	if err != nil || cfg.DatabaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	pool, err := Open(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs last: the row cleanup below still needs the pool.
	t.Cleanup(pool.Close)
	s := New(pool)

	slug := "zz-itest-" + name
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

	companyID, err := s.UpsertCompany(ctx, "Integration Test Co", slug, true)
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.UpsertSource(ctx, companyID, "greenhouse", slug, "http://example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	// count runs a query returning one int, with $1 = source id and $2 = company id.
	count := func(q string, extra ...any) int {
		var n int
		args := append([]any{src.ID, companyID}, extra...)
		if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	return ctx, s, src, count
}

func mk(id, title, hash string) PostingInput {
	return PostingInput{
		ExternalID: id, URL: "http://x/" + id, TitleRaw: title, LocationRaw: "Pune, India",
		ContentHash: hash, RawGzip: []byte("gz-" + hash),
		Job: JobFields{Title: title, TitleNormalized: title, Description: "Go and Postgres",
			LocationRaw: "Pune, India", Fingerprint: "fp-" + id},
	}
}

const (
	qOpenJobs  = `SELECT count(*) FROM jobs WHERE company_id=$2 AND closed_at IS NULL AND $1::bigint IS NOT NULL`
	qAllJobs   = `SELECT count(*) FROM jobs WHERE company_id=$2 AND $1::bigint IS NOT NULL`
	qActivePst = `SELECT count(*) FROM job_postings WHERE source_id=$1 AND removed_at IS NULL AND $2::bigint IS NOT NULL`
)

func TestSyncPostingsLifecycle(t *testing.T) {
	ctx, s, src, count := newEnv(t, "lifecycle")
	opts := SyncOptions{}

	// 1) first run: two new jobs
	res, err := s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend", "h1"), mk("2", "Frontend", "h2")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 2 || res.Updated != 0 || res.Unchanged != 0 || res.Closed != 0 {
		t.Fatalf("first run: %+v", res)
	}
	if count(qOpenJobs) != 2 || count(qActivePst) != 2 {
		t.Fatalf("expected 2 open jobs/postings, got %d/%d", count(qOpenJobs), count(qActivePst))
	}
	if n := count(`SELECT count(*) FROM posting_raw WHERE posting_id IN (SELECT id FROM job_postings WHERE source_id=$1) AND $2::bigint IS NOT NULL`); n != 2 {
		t.Fatalf("expected 2 raw payloads, got %d", n)
	}

	// 2) identical run: idempotent, nothing new
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend", "h1"), mk("2", "Frontend", "h2")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Updated != 0 || res.Unchanged != 2 || res.Closed != 0 {
		t.Fatalf("idempotent run: %+v", res)
	}
	if n := count(qAllJobs); n != 2 {
		t.Fatalf("re-run must not duplicate jobs, got %d", n)
	}

	// 3) job 1 changes, job 2 disappears, job 3 appears (small board: no drop guard)
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend v2", "h1b"), mk("3", "Data", "h3")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 1 || res.Updated != 1 || res.Closed != 1 {
		t.Fatalf("mixed run: %+v", res)
	}
	if count(qOpenJobs) != 2 {
		t.Fatalf("expected 2 open jobs (1,3), got %d", count(qOpenJobs))
	}

	// 4) source returns nothing: must NOT close everything
	res, err = s.SyncPostings(ctx, src, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.SkippedClose || res.Closed != 0 || res.ActiveBefore != 2 || res.SkipReason == "" {
		t.Fatalf("empty-fetch guard: %+v", res)
	}
	if count(qOpenJobs) != 2 {
		t.Fatalf("jobs must stay open after an empty fetch, got %d open", count(qOpenJobs))
	}

	// 5) job 2 reappears: reopened, not duplicated
	res, err = s.SyncPostings(ctx, src, []PostingInput{mk("1", "Backend v2", "h1b"), mk("2", "Frontend", "h2"), mk("3", "Data", "h3")}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Closed != 0 {
		t.Fatalf("reappear run: %+v", res)
	}
	if count(qOpenJobs) != 3 || count(qActivePst) != 3 {
		t.Fatalf("expected 3 open after reappear, got %d/%d", count(qOpenJobs), count(qActivePst))
	}
	if n := count(qAllJobs); n != 3 {
		t.Fatalf("a reappearing job must reuse its row, got %d jobs total", n)
	}
}

func TestMassDropGuardAndOverride(t *testing.T) {
	ctx, s, src, count := newEnv(t, "massdrop")

	all := make([]PostingInput, 0, 20)
	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		all = append(all, mk(id, "Job "+id, "h-"+id))
	}
	if res, err := s.SyncPostings(ctx, src, all, SyncOptions{}); err != nil || res.New != 20 {
		t.Fatalf("seed run: %+v, %v", res, err)
	}

	// The source suddenly lists only 5 of 20: withheld, nothing closes.
	res, err := s.SyncPostings(ctx, src, all[:5], SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SkippedClose || res.Closed != 0 || !strings.Contains(res.SkipReason, "down from 20") {
		t.Fatalf("drop guard should engage: %+v", res)
	}
	if count(qOpenJobs) != 20 {
		t.Fatalf("all 20 jobs must stay open, got %d", count(qOpenJobs))
	}

	// With explicit approval the 15 missing jobs close.
	res, err = s.SyncPostings(ctx, src, all[:5], SyncOptions{AllowMassClose: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedClose || res.Closed != 15 {
		t.Fatalf("override should close 15: %+v", res)
	}
	if count(qOpenJobs) != 5 {
		t.Fatalf("expected 5 open jobs, got %d", count(qOpenJobs))
	}
}

func TestDueSelectionAndCircuitBreaker(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "breaker")

	isDue := func() bool {
		due, err := s.ListDueSources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range due {
			if d.ID == src.ID {
				return true
			}
		}
		return false
	}
	setLastRun := func(expr string) {
		if _, err := s.pool.Exec(ctx, `UPDATE sources SET last_run_at = `+expr+` WHERE id = $1`, src.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		expr string
		due  bool
		why  string
	}{
		{"NULL", true, "never run"},
		{"now() - interval '25 hours'", true, "overdue"},
		{"now() - interval '23 hours 30 minutes'", true, "within the 1h tolerance (yesterday's run finished late)"},
		{"now() - interval '22 hours'", false, "ran recently"},
		{"now() - interval '1 minute'", false, "just ran"},
	} {
		setLastRun(c.expr)
		if got := isDue(); got != c.due {
			t.Errorf("last_run_at = %s (%s): due = %v, want %v", c.expr, c.why, got, c.due)
		}
	}
	setLastRun("NULL")
	if err := s.SetSourceEnabled(ctx, src.Kind, src.BoardToken, false); err != nil {
		t.Fatal(err)
	}
	if isDue() {
		t.Error("a disabled source must never be due")
	}
	if err := s.SetSourceEnabled(ctx, src.Kind, src.BoardToken, true); err != nil {
		t.Fatal(err)
	}

	type health struct {
		enabled  bool
		failures int
		reason   *string
	}
	get := func() health {
		var h health
		if err := s.pool.QueryRow(ctx, `SELECT enabled, consecutive_failures, disabled_reason FROM sources WHERE id=$1`, src.ID).
			Scan(&h.enabled, &h.failures, &h.reason); err != nil {
			t.Fatal(err)
		}
		return h
	}
	finish := func(status string, max int) bool {
		id, err := s.StartRun(ctx, src.ID)
		if err != nil {
			t.Fatal(err)
		}
		tripped, err := s.FinishRun(ctx, id, src.ID, status, SyncResult{}, "boom", max)
		if err != nil {
			t.Fatal(err)
		}
		return tripped
	}

	// ok resets, partial leaves alone
	finish("failed", 5)
	finish("failed", 5)
	if h := get(); h.failures != 2 {
		t.Fatalf("after 2 failures: %+v", h)
	}
	finish("partial", 5)
	if h := get(); h.failures != 2 {
		t.Fatalf("partial must not change the count: %+v", h)
	}
	finish("ok", 5)
	if h := get(); h.failures != 0 {
		t.Fatalf("ok must reset the count: %+v", h)
	}

	// five consecutive failures trip the breaker on exactly the fifth
	for i := 1; i <= 5; i++ {
		tripped := finish("failed", 5)
		if tripped != (i == 5) {
			t.Fatalf("failure %d: tripped = %v", i, tripped)
		}
	}
	h := get()
	if h.enabled || h.failures != 5 || h.reason == nil || !strings.Contains(*h.reason, "auto-disabled") {
		t.Fatalf("source should be disabled with a reason: %+v", h)
	}
	if isDue() {
		t.Error("a tripped source must not be due")
	}

	// re-enabling starts fresh
	if err := s.SetSourceEnabled(ctx, src.Kind, src.BoardToken, true); err != nil {
		t.Fatal(err)
	}
	if h := get(); !h.enabled || h.failures != 0 || h.reason != nil {
		t.Fatalf("enable should clear failures and reason: %+v", h)
	}
	if err := s.SetSourceEnabled(ctx, "greenhouse", "zz-does-not-exist", true); err == nil {
		t.Error("enabling an unknown source should error")
	}
}

func TestRunLock(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "runlock")

	id1, err := s.StartRun(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, src.ID); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second start must be refused, got %v", err)
	}
	if _, err := s.FinishRun(ctx, id1, src.ID, "ok", SyncResult{}, "", 5); err != nil {
		t.Fatal(err)
	}

	// After finishing, a new run may start. Make it look like the process died.
	id2, err := s.StartRun(ctx, src.ID)
	if err != nil {
		t.Fatalf("start after finish: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE scrape_runs SET started_at = now() - interval '2 hours' WHERE id = $1`, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartRun(ctx, src.ID); err != nil {
		t.Fatalf("a stale run must be reclaimed, got %v", err)
	}
	var status string
	var msg *string
	if err := s.pool.QueryRow(ctx, `SELECT status, error FROM scrape_runs WHERE id=$1`, id2).Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || msg == nil || !strings.Contains(*msg, "abandoned") {
		t.Fatalf("stale run should be marked abandoned: %s %v", status, msg)
	}
}

func TestRunLockUnderConcurrency(t *testing.T) {
	ctx, s, src, _ := newEnv(t, "lockrace")

	const workers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		refused  int
		firstErr error
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.StartRun(ctx, src.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrAlreadyRunning):
				refused++
			case firstErr == nil:
				firstErr = err
			}
		}()
	}
	close(start)
	wg.Wait()

	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if wins != 1 || refused != workers-1 {
		t.Fatalf("%d simultaneous starts gave %d winners and %d refusals; want exactly 1 winner", workers, wins, refused)
	}
}
