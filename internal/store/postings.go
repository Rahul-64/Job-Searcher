package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobFields is the canonical-job view of a posting (what `jobs` stores).
type JobFields struct {
	Title           string
	TitleNormalized string
	Description     string // plain text
	LocationRaw     string
	Country         *string // nil until the Phase 3 location normalizer
	Fingerprint     string
	PostedAt        *time.Time
}

// PostingInput is one fetched job, ready to be stored.
type PostingInput struct {
	ExternalID  string
	URL         string
	TitleRaw    string
	LocationRaw string
	ContentHash string
	Job         JobFields
	RawGzip     []byte // compressed original payload
}

// SyncResult counts what a sync did.
type SyncResult struct {
	Found     int
	New       int
	Updated   int
	Unchanged int
	Closed    int
	// SkippedClose is set when the source returned nothing but we had active
	// postings: we refuse to close them all on what is probably a bad response.
	SkippedClose bool
	// ActiveBefore is the number of active postings before this sync.
	ActiveBefore int
}

type existingPosting struct {
	ID      int64
	Hash    string
	JobID   *int64
	Removed bool
}

type changedPosting struct {
	Existing existingPosting
	Input    PostingInput
}

// dedupe keeps the last occurrence of each external id, preserving first-seen order.
func dedupe(in []PostingInput) []PostingInput {
	idx := make(map[string]int, len(in))
	out := make([]PostingInput, 0, len(in))
	for _, p := range in {
		if i, ok := idx[p.ExternalID]; ok {
			out[i] = p
			continue
		}
		idx[p.ExternalID] = len(out)
		out = append(out, p)
	}
	return out
}

// classify splits fetched postings into brand-new ones and ones whose content
// changed. Everything else is unchanged (and only has its last_seen refreshed).
func classify(existing map[string]existingPosting, in []PostingInput) (fresh []PostingInput, changed []changedPosting) {
	for _, p := range in {
		ex, ok := existing[p.ExternalID]
		switch {
		case !ok:
			fresh = append(fresh, p)
		case ex.Hash != p.ContentHash:
			changed = append(changed, changedPosting{Existing: ex, Input: p})
		}
	}
	return fresh, changed
}

func loadExisting(ctx context.Context, tx pgx.Tx, sourceID int64) (map[string]existingPosting, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, external_id, content_hash, job_id, removed_at IS NOT NULL
		FROM job_postings WHERE source_id = $1`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]existingPosting)
	for rows.Next() {
		var (
			e   existingPosting
			ext string
		)
		if err := rows.Scan(&e.ID, &ext, &e.Hash, &e.JobID, &e.Removed); err != nil {
			return nil, err
		}
		m[ext] = e
	}
	return m, rows.Err()
}

const (
	insertJobSQL = `
		INSERT INTO jobs (company_id, title, title_normalized, description, location_raw,
		                  country, fingerprint, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`
	insertPostingSQL = `
		INSERT INTO job_postings (job_id, source_id, external_id, url, title_raw, location_raw, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`
	upsertRawSQL = `
		INSERT INTO posting_raw (posting_id, content_hash, body) VALUES ($1, $2, $3)
		ON CONFLICT (posting_id) DO UPDATE
		  SET content_hash = EXCLUDED.content_hash, body = EXCLUDED.body, fetched_at = now()`
	updatePostingSQL = `
		UPDATE job_postings SET url = $2, title_raw = $3, location_raw = $4, content_hash = $5,
		  last_seen_at = now(), removed_at = NULL
		WHERE id = $1`
	updateJobSQL = `
		UPDATE jobs SET title = $2, title_normalized = $3, description = $4, location_raw = $5,
		  country = $6, fingerprint = $7, posted_at = $8, last_seen_at = now(), closed_at = NULL
		WHERE id = $1`
)

// SyncPostings reconciles a source's fetched jobs with the database in one
// transaction: inserts new postings (and their canonical jobs), updates changed
// ones, refreshes last_seen on the rest, and marks vanished postings removed.
// It is idempotent: running it twice with the same input changes nothing.
func (s *Store) SyncPostings(ctx context.Context, src SourceRow, in []PostingInput) (SyncResult, error) {
	in = dedupe(in)
	res := SyncResult{Found: len(in)}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	existing, err := loadExisting(ctx, tx, src.ID)
	if err != nil {
		return res, fmt.Errorf("load existing postings: %w", err)
	}
	for _, e := range existing {
		if !e.Removed {
			res.ActiveBefore++
		}
	}

	fresh, changed := classify(existing, in)
	res.New, res.Updated = len(fresh), len(changed)
	res.Unchanged = res.Found - res.New - res.Updated

	if err := insertNew(ctx, tx, src, fresh); err != nil {
		return res, err
	}
	if err := updateChanged(ctx, tx, changed); err != nil {
		return res, err
	}

	ids := make([]string, 0, len(in)) // non-nil: an empty array, never NULL
	for _, p := range in {
		ids = append(ids, p.ExternalID)
	}

	// Everything we saw this run is alive: refresh timestamps and reopen if needed.
	if _, err := tx.Exec(ctx, `
		UPDATE job_postings SET last_seen_at = now(), removed_at = NULL
		WHERE source_id = $1 AND external_id = ANY($2)`, src.ID, ids); err != nil {
		return res, fmt.Errorf("touch postings: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE jobs SET last_seen_at = now(), closed_at = NULL
		WHERE id IN (SELECT job_id FROM job_postings
		             WHERE source_id = $1 AND external_id = ANY($2) AND job_id IS NOT NULL)`,
		src.ID, ids); err != nil {
		return res, fmt.Errorf("touch jobs: %w", err)
	}

	// Anything active that we did NOT see is gone from the source.
	if len(in) == 0 && res.ActiveBefore > 0 {
		res.SkippedClose = true
	} else if err := closeMissing(ctx, tx, src.ID, ids, &res); err != nil {
		return res, err
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

func insertNew(ctx context.Context, tx pgx.Tx, src SourceRow, fresh []PostingInput) error {
	if len(fresh) == 0 {
		return nil
	}

	// 1) canonical jobs
	b := &pgx.Batch{}
	for _, p := range fresh {
		j := p.Job
		b.Queue(insertJobSQL, src.CompanyID, j.Title, j.TitleNormalized, j.Description,
			j.LocationRaw, j.Country, j.Fingerprint, j.PostedAt)
	}
	jobIDs := make([]int64, len(fresh))
	br := tx.SendBatch(ctx, b)
	for i := range fresh {
		if err := br.QueryRow().Scan(&jobIDs[i]); err != nil {
			br.Close()
			return fmt.Errorf("insert job %q: %w", fresh[i].ExternalID, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}

	// 2) postings
	b = &pgx.Batch{}
	for i, p := range fresh {
		b.Queue(insertPostingSQL, jobIDs[i], src.ID, p.ExternalID, p.URL, p.TitleRaw, p.LocationRaw, p.ContentHash)
	}
	postingIDs := make([]int64, len(fresh))
	br = tx.SendBatch(ctx, b)
	for i := range fresh {
		if err := br.QueryRow().Scan(&postingIDs[i]); err != nil {
			br.Close()
			return fmt.Errorf("insert posting %q: %w", fresh[i].ExternalID, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}

	// 3) raw payloads
	b = &pgx.Batch{}
	for i, p := range fresh {
		b.Queue(upsertRawSQL, postingIDs[i], p.ContentHash, p.RawGzip)
	}
	return execBatch(ctx, tx, b, len(fresh), "insert raw payload")
}

func updateChanged(ctx context.Context, tx pgx.Tx, changed []changedPosting) error {
	if len(changed) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	n := 0
	for _, c := range changed {
		p, j := c.Input, c.Input.Job
		b.Queue(updatePostingSQL, c.Existing.ID, p.URL, p.TitleRaw, p.LocationRaw, p.ContentHash)
		n++
		if c.Existing.JobID != nil {
			b.Queue(updateJobSQL, *c.Existing.JobID, j.Title, j.TitleNormalized, j.Description,
				j.LocationRaw, j.Country, j.Fingerprint, j.PostedAt)
			n++
		}
		b.Queue(upsertRawSQL, c.Existing.ID, p.ContentHash, p.RawGzip)
		n++
	}
	return execBatch(ctx, tx, b, n, "update changed posting")
}

func execBatch(ctx context.Context, tx pgx.Tx, b *pgx.Batch, n int, what string) error {
	br := tx.SendBatch(ctx, b)
	for i := 0; i < n; i++ {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	return br.Close()
}

func closeMissing(ctx context.Context, tx pgx.Tx, sourceID int64, seen []string, res *SyncResult) error {
	rows, err := tx.Query(ctx, `
		UPDATE job_postings SET removed_at = now()
		WHERE source_id = $1 AND removed_at IS NULL AND NOT (external_id = ANY($2))
		RETURNING job_id`, sourceID, seen)
	if err != nil {
		return fmt.Errorf("mark removed postings: %w", err)
	}
	var jobIDs []int64
	for rows.Next() {
		var id *int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if id != nil {
			jobIDs = append(jobIDs, *id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(jobIDs) == 0 {
		return nil
	}
	// A job closes only when none of its postings is still listed anywhere.
	ct, err := tx.Exec(ctx, `
		UPDATE jobs SET closed_at = now()
		WHERE id = ANY($1) AND closed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM job_postings p
		                  WHERE p.job_id = jobs.id AND p.removed_at IS NULL)`, jobIDs)
	if err != nil {
		return fmt.Errorf("close jobs: %w", err)
	}
	res.Closed = int(ct.RowsAffected())
	return nil
}
