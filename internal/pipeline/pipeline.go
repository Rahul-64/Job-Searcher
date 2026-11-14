// Package pipeline wires a source adapter to the database: fetch, normalize,
// scope-filter, sync, and record the run. Politeness (robots.txt, rate limits,
// retries) is handled inside the HTTP client the adapters are built with;
// reliability across runs (run lock, circuit breaker, mass-drop guard) is handled
// here and in the store.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"jobscraper/internal/fetcher"
	"jobscraper/internal/normalizer"
	"jobscraper/internal/raw"
	"jobscraper/internal/store"
)

// Run statuses reported in an Outcome.
const (
	StatusOK        = "ok"
	StatusPartial   = "partial" // fetched fine, but closing was withheld (see Outcome.Note)
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"   // another run of this source is in progress
	StatusCancelled = "cancelled" // the process was interrupted before this source ran
)

// DefaultMaxFailures is how many consecutive failed runs disable a source.
const DefaultMaxFailures = 5

type Runner struct {
	Store           *store.Store
	Adapters        map[string]fetcher.Adapter // keyed by source kind
	Log             *slog.Logger
	MaxFailures     int      // 0 means DefaultMaxFailures
	AllowMassClose  bool     // override the mass-drop guard
	TargetCountries []string // ISO codes to keep; empty keeps everything
}

// Outcome is the result of running one source.
type Outcome struct {
	Source  store.SourceRow
	Status  string
	Result  store.SyncResult
	Note    string // why a run was partial, skipped or failed
	Err     error
	Tripped bool // the circuit breaker disabled the source after this run
	Took    time.Duration
}

func (r *Runner) maxFailures() int {
	if r.MaxFailures > 0 {
		return r.MaxFailures
	}
	return DefaultMaxFailures
}

func (r *Runner) adapter(kind string) (fetcher.Adapter, error) {
	a, ok := r.Adapters[kind]
	if !ok {
		return nil, fmt.Errorf("no adapter for source kind %q", kind)
	}
	return a, nil
}

// EnsureSource makes sure the company and source rows exist for kind:token.
//
// nameOverride is an explicit display name (from the seed file) and wins over
// everything, also renaming an existing company. Otherwise the adapter's name is
// used, falling back to a title-cased token. Without an override an existing
// company keeps its current name.
func (r *Runner) EnsureSource(ctx context.Context, kind, token, nameOverride string) (store.SourceRow, error) {
	a, err := r.adapter(kind)
	if err != nil {
		return store.SourceRow{}, err
	}
	name, err := a.Company(ctx, token) // also verifies the board exists
	if err != nil {
		return store.SourceRow{}, fmt.Errorf("resolve %s:%s: %w", kind, token, err)
	}
	overwrite := strings.TrimSpace(nameOverride) != ""
	switch {
	case overwrite:
		name = strings.TrimSpace(nameOverride)
	case strings.TrimSpace(name) == "":
		name = TitleCase(token)
	}
	companyID, err := r.Store.UpsertCompany(ctx, name, token, overwrite)
	if err != nil {
		return store.SourceRow{}, fmt.Errorf("upsert company: %w", err)
	}
	return r.Store.UpsertSource(ctx, companyID, kind, token, a.BoardURL(token))
}

// TitleCase turns a board token like "mind-tickle" into "Mind Tickle".
func TitleCase(token string) string {
	words := strings.FieldsFunc(token, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// RunSource scrapes one source end to end and records the run. It never
// panics the caller's loop: every failure is returned inside the Outcome.
func (r *Runner) RunSource(ctx context.Context, src store.SourceRow) Outcome {
	log := r.Log.With("source", src.String())
	out := Outcome{Source: src}

	a, err := r.adapter(src.Kind)
	if err != nil {
		out.Status, out.Err, out.Note = StatusFailed, err, err.Error()
		return out
	}
	runID, err := r.Store.StartRun(ctx, src.ID)
	if errors.Is(err, store.ErrAlreadyRunning) {
		out.Status, out.Note = StatusSkipped, err.Error()
		log.Warn("skipped", "reason", out.Note)
		return out
	}
	if err != nil {
		out.Status, out.Err, out.Note = StatusFailed, fmt.Errorf("start run: %w", err), err.Error()
		return out
	}

	start := time.Now()
	res, runErr := r.fetchAndSync(ctx, a, src)
	out.Result, out.Took = res, time.Since(start)

	switch {
	case runErr != nil:
		out.Status, out.Err, out.Note = StatusFailed, runErr, runErr.Error()
	case res.SkippedClose:
		out.Status, out.Note = StatusPartial, res.SkipReason+"; kept all active postings open (use --accept-drop to close them)"
	default:
		out.Status = StatusOK
	}

	// Record the outcome even if ctx was cancelled (e.g. Ctrl-C mid-run).
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	tripped, err := r.Store.FinishRun(fctx, runID, src.ID, out.Status, res, out.Note, r.maxFailures())
	if err != nil {
		log.Error("could not record run result", "err", err)
	}
	out.Tripped = tripped

	log.Info("run finished", "status", out.Status, "found", res.Found, "new", res.New,
		"updated", res.Updated, "unchanged", res.Unchanged, "closed", res.Closed,
		"out_of_scope", res.OutOfScope, "took", out.Took.Round(time.Millisecond))
	if tripped {
		log.Warn("source auto-disabled by circuit breaker", "failures", r.maxFailures())
	}
	return out
}

// RunMany runs sources with at most `concurrency` in flight. A failure in one
// source never affects another. Per-host politeness is enforced by the HTTP
// client, so a higher concurrency only helps when sources live on different hosts.
func (r *Runner) RunMany(ctx context.Context, srcs []store.SourceRow, concurrency int) []Outcome {
	if concurrency < 1 {
		concurrency = 1
	}
	out := make([]Outcome, len(srcs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, src := range srcs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			for j := i; j < len(srcs); j++ {
				out[j] = Outcome{Source: srcs[j], Status: StatusCancelled, Err: ctx.Err(), Note: "interrupted"}
			}
			wg.Wait()
			return out
		}
		wg.Add(1)
		go func(i int, src store.SourceRow) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = r.RunSource(ctx, src)
		}(i, src)
	}
	wg.Wait()
	return out
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
	var outOfScope []string
	for _, j := range jobs {
		in, err := toInput(src.CompanyID, j)
		if err != nil {
			return store.SyncResult{}, err
		}
		if !InScope(in.Job.Places, r.TargetCountries) {
			outOfScope = append(outOfScope, j.ExternalID)
			continue
		}
		inputs = append(inputs, in)
	}
	return r.Store.SyncPostings(ctx, src, inputs, store.SyncOptions{
		AllowMassClose: r.AllowMassClose,
		OutOfScope:     outOfScope,
	})
}

// InScope reports whether a job belongs to the target countries. With no
// targets everything is in scope. A job whose location could not be resolved is
// kept: dropping it would lose Indian jobs to a parsing gap, and the quality
// report lists unresolved locations so the rules can be improved.
func InScope(places []store.Place, targets []string) bool {
	if len(targets) == 0 || len(places) == 0 {
		return true
	}
	for _, p := range places {
		for _, t := range targets {
			if p.Country == t {
				return true
			}
		}
	}
	return false
}

// toInput normalizes a fetched job and converts it to the storage shape.
func toInput(companyID int64, j fetcher.RawJob) (store.PostingInput, error) {
	gz, err := raw.Compress(j.Payload)
	if err != nil {
		return store.PostingInput{}, fmt.Errorf("compress payload for %s: %w", j.ExternalID, err)
	}
	return store.PostingInput{
		ExternalID:  j.ExternalID,
		URL:         j.URL,
		TitleRaw:    j.Title,
		LocationRaw: j.Location,
		ContentHash: j.ContentHash(),
		RawGzip:     gz,
		Job:         jobFields(companyID, j),
	}, nil
}

// jobFields derives the canonical-job fields from a fetched job.
func jobFields(companyID int64, j fetcher.RawJob) store.JobFields {
	desc := normalizer.HTMLToText(j.DescriptionHTML)
	n := normalizer.Normalize(normalizer.Input{
		CompanyID:        companyID,
		Title:            j.Title,
		Location:         j.Location,
		Description:      desc,
		Department:       j.Department,
		SourceCountry:    j.Country,
		SourceRemote:     j.Remote,
		SourceEmployment: j.Employment,
		SourceSalary:     sourceSalary(j),
	})

	f := store.JobFields{
		Title:           j.Title,
		TitleNormalized: n.TitleNormalized,
		Description:     desc,
		LocationRaw:     j.Location,
		Fingerprint:     n.Fingerprint,
		PostedAt:        j.PostedAt,
		RemoteType:      strPtr(n.RemoteType),
		Seniority:       strPtr(n.Seniority),
		EmploymentType:  strPtr(n.EmploymentType),
		ExpMin:          n.ExpMin,
		ExpMax:          n.ExpMax,
		NormVersion:     n.Version,
	}
	for _, p := range n.Places {
		f.Places = append(f.Places, store.Place{City: p.City, Region: p.Region, Country: p.Country})
	}
	if p := n.Primary(); p.Country != "" {
		f.City, f.Region, f.Country = strPtr(p.City), strPtr(p.Region), strPtr(p.Country)
	}
	if s := n.Salary; s != nil {
		f.SalaryMin, f.SalaryMax = &s.Min, &s.Max
		f.SalaryCurrency, f.SalaryPeriod = strPtr(s.Currency), strPtr(s.Period)
	}
	return f
}

func sourceSalary(j fetcher.RawJob) *normalizer.Salary {
	if j.SalaryMax <= 0 {
		return nil
	}
	return &normalizer.Salary{Min: j.SalaryMin, Max: j.SalaryMax, Currency: j.SalaryCurrency, Period: j.SalaryPeriod}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RenormResult summarises a renormalize run.
type RenormResult struct {
	Updated  int
	Failed   int // payloads that could not be parsed (left at their old version)
	Remained int // open jobs still below the current version afterwards
}

// Renormalize re-derives every open job below the current normalizer version
// from its stored raw payload. No site is contacted: this is the payoff of
// keeping the original payloads.
func (r *Runner) Renormalize(ctx context.Context, batchSize int) (RenormResult, error) {
	var res RenormResult
	var after int64
	for {
		rows, err := r.Store.ListRenormalizable(ctx, normalizer.Version, after, batchSize)
		if err != nil {
			return res, err
		}
		if len(rows) == 0 {
			break
		}
		ups := make([]store.NormUpdate, 0, len(rows))
		for _, row := range rows {
			after = row.JobID
			a, ok := r.Adapters[row.Kind]
			if !ok {
				res.Failed++
				continue
			}
			payload, err := raw.Decompress(row.Body)
			if err != nil {
				res.Failed++
				r.Log.Warn("renormalize: bad stored payload", "job", row.JobID, "err", err)
				continue
			}
			rj, err := a.Parse(payload)
			if err != nil {
				res.Failed++
				r.Log.Warn("renormalize: cannot parse payload", "job", row.JobID, "err", err)
				continue
			}
			ups = append(ups, store.NormUpdate{JobID: row.JobID, Job: jobFields(row.CompanyID, rj)})
		}
		if err := r.Store.ApplyNormalization(ctx, ups); err != nil {
			return res, err
		}
		res.Updated += len(ups)
		r.Log.Info("renormalized", "batch", len(ups), "total", res.Updated)
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}
	remained, err := r.Store.CountRenormalizable(ctx, normalizer.Version)
	if err != nil {
		return res, err
	}
	res.Remained = remained
	return res, nil
}
