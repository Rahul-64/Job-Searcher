// Package pipeline wires a source adapter to the database: fetch, convert, sync,
// and record the run. Phase 2 adds the validator (robots.txt, rate limits,
// circuit breaker) in front of Fetch and concurrency across sources.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/normalizer"
	"jobscraper/internal/raw"
	"jobscraper/internal/store"
)

type Runner struct {
	Store    *store.Store
	Adapters map[string]fetcher.Adapter // keyed by source kind
	Log      *slog.Logger
}

func (r *Runner) adapter(kind string) (fetcher.Adapter, error) {
	a, ok := r.Adapters[kind]
	if !ok {
		return nil, fmt.Errorf("no adapter for source kind %q", kind)
	}
	return a, nil
}

// EnsureSource makes sure the company and source rows exist for kind:token,
// resolving the company name from the source itself.
func (r *Runner) EnsureSource(ctx context.Context, kind, token string) (store.SourceRow, error) {
	a, err := r.adapter(kind)
	if err != nil {
		return store.SourceRow{}, err
	}
	name, err := a.Company(ctx, token)
	if err != nil {
		return store.SourceRow{}, fmt.Errorf("resolve %s:%s: %w", kind, token, err)
	}
	companyID, err := r.Store.UpsertCompany(ctx, name, token)
	if err != nil {
		return store.SourceRow{}, fmt.Errorf("upsert company: %w", err)
	}
	return r.Store.UpsertSource(ctx, companyID, kind, token, a.BoardURL(token))
}

// RunSource scrapes one source end to end and records the run.
func (r *Runner) RunSource(ctx context.Context, src store.SourceRow) (store.SyncResult, error) {
	log := r.Log.With("source", src.String())

	a, err := r.adapter(src.Kind)
	if err != nil {
		return store.SyncResult{}, err
	}
	runID, err := r.Store.StartRun(ctx, src.ID)
	if err != nil {
		return store.SyncResult{}, fmt.Errorf("start run: %w", err)
	}

	start := time.Now()
	res, runErr := r.fetchAndSync(ctx, a, src)

	status, msg := "ok", ""
	switch {
	case runErr != nil:
		status, msg = "failed", runErr.Error()
	case res.SkippedClose:
		status = "partial"
		msg = fmt.Sprintf("source returned 0 jobs; kept %d active postings open", res.ActiveBefore)
	}

	// Record the outcome even if ctx was cancelled (e.g. Ctrl-C mid-run).
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := r.Store.FinishRun(fctx, runID, src.ID, status, res, msg); err != nil {
		log.Error("could not record run result", "err", err)
	}

	log.Info("run finished", "status", status, "found", res.Found, "new", res.New,
		"updated", res.Updated, "unchanged", res.Unchanged, "closed", res.Closed,
		"took", time.Since(start).Round(time.Millisecond))
	return res, runErr
}

func (r *Runner) fetchAndSync(ctx context.Context, a fetcher.Adapter, src store.SourceRow) (store.SyncResult, error) {
	jobs, err := a.Fetch(ctx, src.BoardToken)
	if err != nil {
		if errors.Is(err, fetcher.ErrBoardNotFound) {
			return store.SyncResult{}, fmt.Errorf("board %s no longer exists: %w", src, err)
		}
		return store.SyncResult{}, fmt.Errorf("fetch: %w", err)
	}
	inputs := make([]store.PostingInput, 0, len(jobs))
	for _, j := range jobs {
		in, err := toInput(src.CompanyID, j)
		if err != nil {
			return store.SyncResult{}, err
		}
		inputs = append(inputs, in)
	}
	return r.Store.SyncPostings(ctx, src, inputs)
}

// toInput converts a fetched job into the storage shape, applying the light
// Phase 1 normalization (full normalization arrives in Phase 3).
func toInput(companyID int64, j fetcher.RawJob) (store.PostingInput, error) {
	gz, err := raw.Compress(j.Payload)
	if err != nil {
		return store.PostingInput{}, fmt.Errorf("compress payload for %s: %w", j.ExternalID, err)
	}
	titleNorm := normalizer.NormalizeTitle(j.Title)
	return store.PostingInput{
		ExternalID:  j.ExternalID,
		URL:         j.URL,
		TitleRaw:    j.Title,
		LocationRaw: j.Location,
		ContentHash: j.ContentHash(),
		RawGzip:     gz,
		Job: store.JobFields{
			Title:           j.Title,
			TitleNormalized: titleNorm,
			Description:     normalizer.HTMLToText(j.DescriptionHTML),
			LocationRaw:     j.Location,
			Country:         normalizer.CountryHint(j.Location),
			Fingerprint:     normalizer.Fingerprint(companyID, titleNorm, j.Location),
			PostedAt:        j.PostedAt,
		},
	}, nil
}
