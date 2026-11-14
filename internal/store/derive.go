package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// RenormRow is a stored job together with the raw payload it was derived from.
type RenormRow struct {
	JobID     int64
	CompanyID int64
	Kind      string // source kind, selects the adapter that can parse Body
	Body      []byte // gzip-compressed original payload of the job's primary posting
}

// ListRenormalizable returns up to limit open jobs with id > afterID whose
// normalization version is below `version`, in id order, each with the raw
// payload of its oldest posting. Jobs without a stored payload are not returned.
func (s *Store) ListRenormalizable(ctx context.Context, version int, afterID int64, limit int) ([]RenormRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (j.id) j.id, j.company_id, s.kind, r.body
		FROM jobs j
		JOIN job_postings p ON p.job_id = j.id
		JOIN sources s ON s.id = p.source_id
		JOIN posting_raw r ON r.posting_id = p.id
		WHERE j.id > $1 AND j.norm_version < $2 AND j.closed_at IS NULL
		ORDER BY j.id, p.id
		LIMIT $3`, afterID, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RenormRow
	for rows.Next() {
		var r RenormRow
		if err := rows.Scan(&r.JobID, &r.CompanyID, &r.Kind, &r.Body); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRenormalizable counts open jobs below the given normalization version.
func (s *Store) CountRenormalizable(ctx context.Context, version int) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE norm_version < $1 AND closed_at IS NULL`, version).Scan(&n)
	return n, err
}

// NormUpdate re-derives the cleaned fields of one job. Title, description,
// location_raw and posted_at are source data and are left untouched.
type NormUpdate struct {
	JobID int64
	Job   JobFields
}

// ApplyNormalization writes re-derived fields and replaces each job's places,
// in one transaction.
func (s *Store) ApplyNormalization(ctx context.Context, ups []NormUpdate) error {
	if len(ups) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	b := &pgx.Batch{}
	places := make(map[int64][]Place, len(ups))
	for _, u := range ups {
		j := u.Job
		b.Queue(`
			UPDATE jobs SET title_normalized=$2, city=$3, region=$4, country=$5, remote_type=$6,
			  seniority=$7, employment_type=$8, exp_min_years=$9, exp_max_years=$10,
			  salary_min=$11, salary_max=$12, salary_currency=$13, salary_period=$14,
			  fingerprint=$15, norm_version=$16, role_family=$17
			WHERE id = $1`,
			u.JobID, j.TitleNormalized, j.City, j.Region, j.Country, j.RemoteType,
			j.Seniority, j.EmploymentType, j.ExpMin, j.ExpMax,
			j.SalaryMin, j.SalaryMax, j.SalaryCurrency, j.SalaryPeriod,
			j.Fingerprint, j.NormVersion, j.RoleFamily)
		places[u.JobID] = j.Places
	}
	if err := execBatch(ctx, tx, b, len(ups), "update job normalization"); err != nil {
		return err
	}
	if err := replaceJobLocations(ctx, tx, places); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DedupResult summarises a merge of identical jobs.
type DedupResult struct {
	Groups  int // fingerprints that had more than one job
	Removed int // duplicate job rows merged away
}

// Dedup merges jobs that share a company and fingerprint into the oldest one.
// Their postings are re-pointed at the survivor, so nothing is lost: it simply
// becomes one job with several postings. Only jobs normalized at `version` are
// considered, because fingerprints from older rules mean something different.
// It is idempotent and safe to run after every scrape.
func (s *Store) Dedup(ctx context.Context, version int) (DedupResult, error) {
	var res DedupResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE dedup_map ON COMMIT DROP AS
		SELECT j.id AS dupe_id, k.keep_id
		FROM jobs j
		JOIN (SELECT company_id, fingerprint, min(id) AS keep_id
		      FROM jobs WHERE norm_version = $1
		      GROUP BY company_id, fingerprint HAVING count(*) > 1) k
		  ON k.company_id = j.company_id AND k.fingerprint = j.fingerprint
		WHERE j.norm_version = $1 AND j.id <> k.keep_id`, version); err != nil {
		return res, fmt.Errorf("find duplicates: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT keep_id), count(*) FROM dedup_map`).Scan(&res.Groups, &res.Removed); err != nil {
		return res, err
	}
	if res.Removed == 0 {
		return res, nil
	}

	steps := []struct{ what, sql string }{
		{"re-point postings", `UPDATE job_postings p SET job_id = m.keep_id FROM dedup_map m WHERE p.job_id = m.dupe_id`},
		{"merge timestamps", `
			UPDATE jobs k SET first_seen_at = LEAST(k.first_seen_at, a.first_seen),
			                  last_seen_at  = GREATEST(k.last_seen_at, a.last_seen)
			FROM (SELECT m.keep_id, min(d.first_seen_at) AS first_seen, max(d.last_seen_at) AS last_seen
			      FROM dedup_map m JOIN jobs d ON d.id = m.dupe_id GROUP BY m.keep_id) a
			WHERE k.id = a.keep_id`},
		{"delete duplicate jobs", `DELETE FROM jobs WHERE id IN (SELECT dupe_id FROM dedup_map)`},
		// A survivor stays open if any of its (now merged) postings is still listed.
		{"reopen survivors", `
			UPDATE jobs j SET closed_at = NULL
			WHERE j.id IN (SELECT keep_id FROM dedup_map) AND j.closed_at IS NOT NULL
			  AND EXISTS (SELECT 1 FROM job_postings p WHERE p.job_id = j.id AND p.removed_at IS NULL)`},
	}
	for _, st := range steps {
		if _, err := tx.Exec(ctx, st.sql); err != nil {
			return res, fmt.Errorf("%s: %w", st.what, err)
		}
	}
	return res, tx.Commit(ctx)
}

// Count is a labelled number in a report.
type Count struct {
	Label string
	N     int
}

// Quality is a data-quality snapshot of the open jobs.
type Quality struct {
	Open, Normalized                   int
	CountryKnown, India, IndiaWithCity int
	RemoteKnown, SeniorityKnown        int
	EmploymentKnown, ExperienceKnown   int
	SalaryKnown, MultiLocation         int
	IndiaCities                        []Count // top cities among India jobs
	IndiaRemote                        []Count // remote_type distribution among India jobs
	IndiaSeniority                     []Count
	Unresolved                         []Count // most common location_raw with no country
}

// Quality computes coverage of the derived fields over open jobs.
func (s *Store) Quality(ctx context.Context, version int) (Quality, error) {
	var q Quality
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		  count(*) FILTER (WHERE norm_version >= $1),
		  count(*) FILTER (WHERE country IS NOT NULL),
		  count(*) FILTER (WHERE country = 'IN'),
		  count(*) FILTER (WHERE country = 'IN' AND city IS NOT NULL),
		  count(*) FILTER (WHERE remote_type IS NOT NULL),
		  count(*) FILTER (WHERE seniority IS NOT NULL),
		  count(*) FILTER (WHERE employment_type IS NOT NULL),
		  count(*) FILTER (WHERE exp_min_years IS NOT NULL),
		  count(*) FILTER (WHERE salary_min IS NOT NULL),
		  count(*) FILTER (WHERE (SELECT count(*) FROM job_locations jl WHERE jl.job_id = jobs.id) > 1)
		FROM jobs WHERE closed_at IS NULL`, version).
		Scan(&q.Open, &q.Normalized, &q.CountryKnown, &q.India, &q.IndiaWithCity, &q.RemoteKnown,
			&q.SeniorityKnown, &q.EmploymentKnown, &q.ExperienceKnown, &q.SalaryKnown, &q.MultiLocation)
	if err != nil {
		return q, err
	}
	dists := []struct {
		dst *[]Count
		sql string
	}{
		{&q.IndiaCities, `SELECT coalesce(city, '(country only)'), count(*) FROM jobs WHERE closed_at IS NULL AND country = 'IN' GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 12`},
		{&q.IndiaRemote, `SELECT coalesce(remote_type, '(unknown)'), count(*) FROM jobs WHERE closed_at IS NULL AND country = 'IN' GROUP BY 1 ORDER BY 2 DESC`},
		{&q.IndiaSeniority, `SELECT coalesce(seniority, '(none)'), count(*) FROM jobs WHERE closed_at IS NULL AND country = 'IN' GROUP BY 1 ORDER BY 2 DESC`},
		{&q.Unresolved, `SELECT coalesce(nullif(location_raw, ''), '(empty)'), count(*) FROM jobs WHERE closed_at IS NULL AND country IS NULL GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 15`},
	}
	for _, d := range dists {
		rows, err := s.pool.Query(ctx, d.sql)
		if err != nil {
			return q, err
		}
		for rows.Next() {
			var c Count
			if err := rows.Scan(&c.Label, &c.N); err != nil {
				rows.Close()
				return q, err
			}
			*d.dst = append(*d.dst, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return q, err
		}
	}
	return q, nil
}
