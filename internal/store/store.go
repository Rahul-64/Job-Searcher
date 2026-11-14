package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

// UpsertCompany creates or renames a company identified by slug.
func (s *Store) UpsertCompany(ctx context.Context, name, slug string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO companies (name, slug) VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`, name, slug).Scan(&id)
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

// StartRun records the beginning of a scrape and returns the run id.
func (s *Store) StartRun(ctx context.Context, sourceID int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO scrape_runs (source_id, status) VALUES ($1, 'running') RETURNING id`, sourceID).Scan(&id)
	return id, err
}

// FinishRun closes a run with its status and counters, and updates source health.
func (s *Store) FinishRun(ctx context.Context, runID, sourceID int64, status string, r SyncResult, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE scrape_runs SET finished_at = now(), status = $2,
		  jobs_found = $3, jobs_new = $4, jobs_updated = $5, jobs_closed = $6, error = NULLIF($7, '')
		WHERE id = $1`, runID, status, r.Found, r.New, r.Updated, r.Closed, errMsg)
	if err != nil {
		return fmt.Errorf("update scrape_runs: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE sources SET last_run_at = now(),
		  consecutive_failures = CASE WHEN $2 = 'failed' THEN consecutive_failures + 1 ELSE 0 END
		WHERE id = $1`, sourceID, status)
	if err != nil {
		return fmt.Errorf("update sources: %w", err)
	}
	return nil
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
