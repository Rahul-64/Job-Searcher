package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadyRunning means another run of the same source is in progress.
var ErrAlreadyRunning = errors.New("a run for this source is already in progress")

// staleRunAfter is how long a 'running' run may last before we assume its
// process died and reclaim the source.
const staleRunAfter = 30 * time.Minute

// Store wraps the connection pool with the application's queries.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// SourceRow is a scrapeable source joined with its company.
type SourceRow struct {
	ID          int64
	CompanyID   int64
	CompanyName string
	Kind        string
	BoardToken  string
	BaseURL     string
	Enabled     bool
}

func (s SourceRow) String() string { return s.Kind + ":" + s.BoardToken }

// UpsertCompany creates a company identified by slug. If it already exists, its
// name is only changed when overwriteName is true (an explicit name from the
// seed file); an auto-derived name never clobbers a curated one.
func (s *Store) UpsertCompany(ctx context.Context, name, slug string, overwriteName bool) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO companies (name, slug) VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE
		  SET name = CASE WHEN $3 THEN EXCLUDED.name ELSE companies.name END
		RETURNING id`, name, slug, overwriteName).Scan(&id)
	return id, err
}

// UpsertSource creates a source (or refreshes its URL/company) and returns it.
// It never re-enables a source that was disabled.
func (s *Store) UpsertSource(ctx context.Context, companyID int64, kind, token, baseURL string) (SourceRow, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sources (company_id, kind, board_token, base_url) VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, board_token) DO UPDATE
		  SET base_url = EXCLUDED.base_url, company_id = EXCLUDED.company_id
		RETURNING id`, companyID, kind, token, baseURL).Scan(&id)
	if err != nil {
		return SourceRow{}, err
	}
	return s.GetSource(ctx, kind, token)
}

const sourceSelect = `
	SELECT s.id, s.company_id, c.name, s.kind, s.board_token, s.base_url, s.enabled
	FROM sources s JOIN companies c ON c.id = s.company_id`

// GetSource loads one source by kind and token.
func (s *Store) GetSource(ctx context.Context, kind, token string) (SourceRow, error) {
	var r SourceRow
	err := s.pool.QueryRow(ctx, sourceSelect+` WHERE s.kind = $1 AND s.board_token = $2`, kind, token).
		Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.Kind, &r.BoardToken, &r.BaseURL, &r.Enabled)
	return r, err
}

// ListSources returns all sources, optionally only the enabled ones.
func (s *Store) ListSources(ctx context.Context, enabledOnly bool) ([]SourceRow, error) {
	rows, err := s.pool.Query(ctx, sourceSelect+` WHERE ($1 = false OR s.enabled) ORDER BY c.name, s.kind`, enabledOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceRow
	for rows.Next() {
		var r SourceRow
		if err := rows.Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.Kind, &r.BoardToken, &r.BaseURL, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StartRun records the beginning of a scrape and returns the run id. It takes a
// per-source advisory lock so two overlapping processes cannot both start, and
// reclaims runs that have been 'running' for longer than staleRunAfter.
// It returns ErrAlreadyRunning if a live run exists.
func (s *Store) StartRun(ctx context.Context, sourceID int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, sourceID); err != nil {
		return 0, fmt.Errorf("lock source: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE scrape_runs SET status = 'failed', finished_at = now(),
		  error = 'abandoned: no result recorded, the process probably died'
		WHERE source_id = $1 AND status = 'running'
		  AND started_at < now() - make_interval(secs => $2)`,
		sourceID, staleRunAfter.Seconds()); err != nil {
		return 0, fmt.Errorf("reclaim stale runs: %w", err)
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO scrape_runs (source_id, status)
		SELECT $1::bigint, 'running'
		WHERE NOT EXISTS (SELECT 1 FROM scrape_runs WHERE source_id = $1 AND status = 'running')
		RETURNING id`, sourceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrAlreadyRunning
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

// FinishRun closes a run with its status and counters and updates source health.
// This is the circuit breaker: after maxFailures consecutive 'failed' runs the
// source is disabled with a reason, and tripped is true. 'ok' resets the failure
// count; 'partial' leaves it unchanged.
func (s *Store) FinishRun(ctx context.Context, runID, sourceID int64, status string, r SyncResult, errMsg string, maxFailures int) (tripped bool, err error) {
	if _, err := s.pool.Exec(ctx, `
		UPDATE scrape_runs SET finished_at = now(), status = $2,
		  jobs_found = $3, jobs_new = $4, jobs_updated = $5, jobs_closed = $6, error = NULLIF($7, '')
		WHERE id = $1`, runID, status, r.Found, r.New, r.Updated, r.Closed, errMsg); err != nil {
		return false, fmt.Errorf("update scrape_runs: %w", err)
	}
	reason := fmt.Sprintf("auto-disabled after %d consecutive failures; last error: %s", maxFailures, truncate(errMsg, 300))
	var enabled bool
	err = s.pool.QueryRow(ctx, `
		UPDATE sources SET last_run_at = now(),
		  consecutive_failures = CASE $2 WHEN 'failed' THEN consecutive_failures + 1
		                                 WHEN 'ok' THEN 0 ELSE consecutive_failures END,
		  enabled = CASE WHEN $2 = 'failed' AND consecutive_failures + 1 >= $3 THEN false ELSE enabled END,
		  disabled_reason = CASE WHEN $2 = 'failed' AND consecutive_failures + 1 >= $3
		                         THEN $4 ELSE disabled_reason END
		WHERE id = $1
		RETURNING enabled`, sourceID, status, maxFailures, reason).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("update sources: %w", err)
	}
	return status == "failed" && !enabled, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// SetSourceEnabled enables or disables a source. Enabling also clears its
// failure count and disabled reason, so a repaired source starts fresh.
func (s *Store) SetSourceEnabled(ctx context.Context, kind, token string, enabled bool) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE sources SET enabled = $3,
		  consecutive_failures = CASE WHEN $3 THEN 0 ELSE consecutive_failures END,
		  disabled_reason = CASE WHEN $3 THEN NULL ELSE coalesce(disabled_reason, 'disabled manually') END
		WHERE kind = $1 AND board_token = $2`, kind, token, enabled)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("no such source %s:%s", kind, token)
	}
	return nil
}

// ListDueSources returns enabled sources whose next scrape is due, oldest first.
//
// A source is due when last_run_at + scrape_every has passed, less a tolerance
// of 10% of the interval (at most 1 hour). Without the tolerance a daily cron
// that fires a few minutes earlier than yesterday's run finished would skip a
// whole day.
func (s *Store) ListDueSources(ctx context.Context) ([]SourceRow, error) {
	rows, err := s.pool.Query(ctx, sourceSelect+`
		WHERE s.enabled
		  AND (s.last_run_at IS NULL
		       OR s.last_run_at + s.scrape_every - LEAST(s.scrape_every * 0.1, interval '1 hour') <= now())
		ORDER BY s.last_run_at NULLS FIRST, c.name, s.kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceRow
	for rows.Next() {
		var r SourceRow
		if err := rows.Scan(&r.ID, &r.CompanyID, &r.CompanyName, &r.Kind, &r.BoardToken, &r.BaseURL, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SourceDetail is a source with its health, for the `sources` command.
type SourceDetail struct {
	SourceRow
	Failures       int
	Interval       time.Duration
	LastRun        *time.Time
	DisabledReason string
}

// ListSourceDetails returns every source with its health fields.
func (s *Store) ListSourceDetails(ctx context.Context) ([]SourceDetail, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.company_id, c.name, s.kind, s.board_token, s.base_url, s.enabled,
		       s.consecutive_failures, EXTRACT(EPOCH FROM s.scrape_every)::float8,
		       s.last_run_at, coalesce(s.disabled_reason, '')
		FROM sources s JOIN companies c ON c.id = s.company_id
		ORDER BY s.enabled DESC, c.name, s.kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceDetail
	for rows.Next() {
		var d SourceDetail
		var secs float64
		if err := rows.Scan(&d.ID, &d.CompanyID, &d.CompanyName, &d.Kind, &d.BoardToken, &d.BaseURL, &d.Enabled,
			&d.Failures, &secs, &d.LastRun, &d.DisabledReason); err != nil {
			return nil, err
		}
		d.Interval = time.Duration(secs * float64(time.Second))
		out = append(out, d)
	}
	return out, rows.Err()
}

// SourceStat is one row of the stats report.
type SourceStat struct {
	Company    string
	Kind       string
	Token      string
	OpenJobs   int
	LastRun    *time.Time
	LastStatus string
}

// Stats summarises what is in the database.
type Stats struct {
	Companies, Sources, Runs int
	OpenJobs, ClosedJobs     int
	ActivePostings           int
	RawPayloads              int
	RawBytes, DBBytes        int64
	PerSource                []SourceStat
}

// Stats collects counts and sizes for the `stats` command and size monitoring.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM companies),
		  (SELECT count(*) FROM sources),
		  (SELECT count(*) FROM scrape_runs),
		  (SELECT count(*) FROM jobs WHERE closed_at IS NULL),
		  (SELECT count(*) FROM jobs WHERE closed_at IS NOT NULL),
		  (SELECT count(*) FROM job_postings WHERE removed_at IS NULL),
		  (SELECT count(*) FROM posting_raw),
		  (SELECT coalesce(sum(octet_length(body)), 0) FROM posting_raw),
		  pg_database_size(current_database())`).
		Scan(&st.Companies, &st.Sources, &st.Runs, &st.OpenJobs, &st.ClosedJobs,
			&st.ActivePostings, &st.RawPayloads, &st.RawBytes, &st.DBBytes)
	if err != nil {
		return st, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.name, s.kind, s.board_token,
		       count(p.id) FILTER (WHERE p.removed_at IS NULL),
		       s.last_run_at,
		       coalesce((SELECT r.status FROM scrape_runs r WHERE r.source_id = s.id
		                 ORDER BY r.started_at DESC LIMIT 1), '')
		FROM sources s
		JOIN companies c ON c.id = s.company_id
		LEFT JOIN job_postings p ON p.source_id = s.id
		GROUP BY s.id, c.name
		ORDER BY c.name`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var x SourceStat
		if err := rows.Scan(&x.Company, &x.Kind, &x.Token, &x.OpenJobs, &x.LastRun, &x.LastStatus); err != nil {
			return st, err
		}
		st.PerSource = append(st.PerSource, x)
	}
	return st, rows.Err()
}
