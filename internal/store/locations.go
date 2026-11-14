package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// upsertLocations returns the id of each place, creating rows as needed.
func upsertLocations(ctx context.Context, tx pgx.Tx, places []Place) (map[Place]int64, error) {
	ids := make(map[Place]int64, len(places))
	var uniq []Place
	for _, p := range places {
		if p.Country == "" {
			continue // a place without a country is never stored
		}
		if _, ok := ids[p]; !ok {
			ids[p] = 0
			uniq = append(uniq, p)
		}
	}
	if len(uniq) == 0 {
		return ids, nil
	}
	b := &pgx.Batch{}
	for _, p := range uniq {
		// The no-op DO UPDATE makes RETURNING yield the id for rows that already exist.
		b.Queue(`INSERT INTO locations (city, region, country) VALUES (NULLIF($1,''), NULLIF($2,''), $3)
		         ON CONFLICT ON CONSTRAINT locations_place_key DO UPDATE SET country = EXCLUDED.country
		         RETURNING id`, p.City, p.Region, p.Country)
	}
	br := tx.SendBatch(ctx, b)
	for _, p := range uniq {
		var id int64
		if err := br.QueryRow().Scan(&id); err != nil {
			br.Close()
			return nil, fmt.Errorf("upsert location %+v: %w", p, err)
		}
		ids[p] = id
	}
	return ids, br.Close()
}

// replaceJobLocations makes job_locations match the given places for each job.
func replaceJobLocations(ctx context.Context, tx pgx.Tx, byJob map[int64][]Place) error {
	if len(byJob) == 0 {
		return nil
	}
	var all []Place
	jobIDs := make([]int64, 0, len(byJob))
	for id, ps := range byJob {
		jobIDs = append(jobIDs, id)
		all = append(all, ps...)
	}
	sort.Slice(jobIDs, func(i, j int) bool { return jobIDs[i] < jobIDs[j] })

	locIDs, err := upsertLocations(ctx, tx, all)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM job_locations WHERE job_id = ANY($1)`, jobIDs); err != nil {
		return fmt.Errorf("clear job_locations: %w", err)
	}
	b := &pgx.Batch{}
	n := 0
	for _, id := range jobIDs {
		for _, p := range byJob[id] {
			if lid := locIDs[p]; lid != 0 {
				b.Queue(`INSERT INTO job_locations (job_id, location_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, lid)
				n++
			}
		}
	}
	return execBatch(ctx, tx, b, n, "insert job_locations")
}

// CityRef is a reference city to seed into locations / city_aliases.
type CityRef struct {
	Name, Region, Country string
	Aliases               []string // lower case
}

// SeedLocations upserts reference cities and their aliases, so SQL (and the API)
// can resolve "blr" or "gurgaon" to a canonical location. Idempotent.
func (s *Store) SeedLocations(ctx context.Context, cities []CityRef) (locations, aliases int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	places := make([]Place, 0, len(cities))
	for _, c := range cities {
		places = append(places, Place{City: c.Name, Region: c.Region, Country: c.Country})
	}
	ids, err := upsertLocations(ctx, tx, places)
	if err != nil {
		return 0, 0, err
	}

	b := &pgx.Batch{}
	n := 0
	for _, c := range cities {
		lid := ids[Place{City: c.Name, Region: c.Region, Country: c.Country}]
		if lid == 0 {
			continue
		}
		for _, a := range append([]string{strings.ToLower(c.Name)}, c.Aliases...) {
			b.Queue(`INSERT INTO city_aliases (alias, location_id) VALUES ($1, $2)
			         ON CONFLICT (alias) DO UPDATE SET location_id = EXCLUDED.location_id`, a, lid)
			n++
		}
	}
	if err := execBatch(ctx, tx, b, n, "upsert city_aliases"); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return len(ids), n, nil
}
