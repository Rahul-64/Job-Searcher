// Command scraper is the entry point for the job-scraper pipeline.
//
// Commands: migrate*, ping, seed, run, run-due, sources, enable, disable,
// stats, renormalize, dedup, quality, seed-locations, version.
// Pruning arrives in a later phase.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"jobscraper/internal/config"
	"jobscraper/internal/fetcher"
	"jobscraper/internal/fetcher/ashby"
	"jobscraper/internal/fetcher/greenhouse"
	"jobscraper/internal/fetcher/lever"
	"jobscraper/internal/normalizer"
	"jobscraper/internal/pipeline"
	"jobscraper/internal/store"
	"jobscraper/internal/validator"
)

const version = "0.4.0-phase3"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch cmd {
	case "migrate", "migrate-down", "migrate-status":
		return cmdMigrate(ctx, cfg, cmd)
	case "ping":
		return cmdPing(ctx, cfg)
	case "seed":
		return cmdSeed(ctx, cfg, rest)
	case "run":
		return cmdRun(ctx, cfg, rest)
	case "run-due":
		return cmdRunDue(ctx, cfg, rest)
	case "sources":
		return cmdSources(ctx, cfg)
	case "enable", "disable":
		return cmdToggle(ctx, cfg, cmd == "enable", rest)
	case "stats":
		return cmdStats(ctx, cfg)
	case "renormalize":
		return cmdRenormalize(ctx, cfg)
	case "dedup":
		return cmdDedup(ctx, cfg)
	case "quality":
		return cmdQuality(ctx, cfg)
	case "seed-locations":
		return cmdSeedLocations(ctx, cfg)
	case "prune":
		return fmt.Errorf("%q is not implemented yet (planned for a later phase)", cmd)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdMigrate(ctx context.Context, cfg *config.Config, cmd string) error {
	url, err := cfg.MigrationURL()
	if err != nil {
		return err
	}
	switch cmd {
	case "migrate":
		return store.MigrateUp(ctx, url)
	case "migrate-down":
		return store.MigrateDown(ctx, url)
	default:
		return store.MigrateStatus(ctx, url)
	}
}

func cmdPing(ctx context.Context, cfg *config.Config) error {
	url, err := cfg.RequireDatabaseURL()
	if err != nil {
		return err
	}
	start := time.Now()
	pool, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return err
	}
	fmt.Printf("ok: connected in %s, server time %s\n", time.Since(start).Round(time.Millisecond), now.Format(time.RFC3339))
	return nil
}

// newRunner opens the database and builds a pipeline runner. Every adapter
// shares one polite HTTP client (robots.txt, per-host rate limit, retries).
func newRunner(ctx context.Context, cfg *config.Config) (*pipeline.Runner, func(), error) {
	url, err := cfg.RequireDatabaseURL()
	if err != nil {
		return nil, nil, err
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	client := validator.NewClient(validator.Options{
		UserAgent:   cfg.UserAgent,
		MinInterval: cfg.MinInterval,
	})
	adapters := []fetcher.Adapter{
		greenhouse.New(cfg.UserAgent, client),
		lever.New(cfg.UserAgent, client),
		ashby.New(cfg.UserAgent, client),
	}
	byKind := make(map[string]fetcher.Adapter, len(adapters))
	for _, a := range adapters {
		byKind[a.Kind()] = a
	}
	r := &pipeline.Runner{
		Store:       store.New(pool),
		Adapters:    byKind,
		Log:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
		MaxFailures: cfg.MaxFailures,

		TargetCountries: cfg.TargetCountries,
	}
	return r, pool.Close, nil
}

// parseSource splits "kind:token".
func parseSource(s string) (kind, token string, err error) {
	kind, token, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || kind == "" || token == "" {
		return "", "", fmt.Errorf("invalid source %q, want kind:token (e.g. greenhouse:groww)", s)
	}
	return strings.ToLower(kind), token, nil
}

type seedEntry struct {
	Kind, Token, Name string // Name is optional
}

// readSeeds reads `kind:token [Display Name]` lines, ignoring blanks and # comments.
func readSeeds(path string) ([]seedEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []seedEntry
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		spec, name, _ := strings.Cut(line, " ")
		k, t, err := parseSource(spec)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, seedEntry{Kind: k, Token: t, Name: strings.TrimSpace(name)})
	}
	return out, sc.Err()
}

// pause waits d or until ctx is cancelled.
func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func cmdSeed(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	file := fs.String("file", "seeds/sources.txt", "file listing `kind:token [Display Name]` sources")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seeds, err := readSeeds(*file)
	if err != nil {
		return err
	}
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()

	failed := 0
	for i, s := range seeds {
		src, err := r.EnsureSource(ctx, s.Kind, s.Token, s.Name)
		if err != nil {
			failed++
			fmt.Printf("  FAIL  %s:%s  %v\n", s.Kind, s.Token, err)
		} else {
			fmt.Printf("  ok    %-24s %s\n", src.String(), src.CompanyName)
		}
		if i < len(seeds)-1 {
			if err := pause(ctx, 300*time.Millisecond); err != nil {
				return err
			}
		}
	}
	fmt.Printf("seeded %d of %d sources\n", len(seeds)-failed, len(seeds))
	if failed > 0 {
		return fmt.Errorf("%d sources failed", failed)
	}
	return nil
}

// runFlags are shared by `run` and `run-due`.
type runFlags struct {
	concurrency int
	acceptDrop  bool
}

func addRunFlags(fs *flag.FlagSet) *runFlags {
	f := &runFlags{}
	fs.IntVar(&f.concurrency, "concurrency", 3, "sources to scrape at once (per-host rate limits still apply)")
	fs.BoolVar(&f.acceptDrop, "accept-drop", false, "close postings even if a source suddenly lists far fewer jobs")
	return f
}

func cmdRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	source := fs.String("source", "", "scrape one source, as kind:token (e.g. greenhouse:groww)")
	all := fs.Bool("all", false, "scrape every enabled source, due or not")
	rf := addRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*source == "") == !*all { // exactly one of --source / --all
		return errors.New("use exactly one of --source kind:token or --all")
	}

	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	r.AllowMassClose = rf.acceptDrop

	if *source != "" {
		kind, token, err := parseSource(*source)
		if err != nil {
			return err
		}
		src, err := r.EnsureSource(ctx, kind, token, "")
		if err != nil {
			return err
		}
		if !src.Enabled {
			fmt.Printf("note: %s is disabled; running it anyway because you asked for it\n", src)
		}
		return report(r.RunMany(ctx, []store.SourceRow{src}, 1))
	}

	srcs, err := r.Store.ListSources(ctx, true)
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		return errors.New("no enabled sources; run `scraper seed` first")
	}
	return report(r.RunMany(ctx, srcs, rf.concurrency))
}

func cmdRunDue(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("run-due", flag.ContinueOnError)
	rf := addRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	r.AllowMassClose = rf.acceptDrop

	srcs, err := r.Store.ListDueSources(ctx)
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		fmt.Println("nothing due: every enabled source was scraped recently")
		return nil
	}
	fmt.Printf("%d source(s) due\n", len(srcs))
	return report(r.RunMany(ctx, srcs, rf.concurrency))
}

// report prints a table of outcomes and returns an error if any source failed.
// Partial and skipped runs are shown but do not fail the command.
func report(outs []pipeline.Outcome) error {
	fmt.Printf("\n%-26s %-9s %6s %5s %5s %6s %6s  %s\n", "SOURCE", "STATUS", "FOUND", "NEW", "UPD", "CLOSED", "OUT", "TOOK")
	var found, nw, upd, closed, outScope int
	counts := map[string]int{}
	for _, o := range outs {
		counts[o.Status]++
		found, nw, upd, closed = found+o.Result.Found, nw+o.Result.New, upd+o.Result.Updated, closed+o.Result.Closed
		outScope += o.Result.OutOfScope
		took := "-"
		if o.Took > 0 {
			took = o.Took.Round(100 * time.Millisecond).String()
		}
		fmt.Printf("%-26s %-9s %6d %5d %5d %6d %6d  %s\n", o.Source.String(), o.Status,
			o.Result.Found, o.Result.New, o.Result.Updated, o.Result.Closed, o.Result.OutOfScope, took)
	}
	fmt.Printf("%-26s %-9s %6d %5d %5d %6d %6d\n", "TOTAL", "", found, nw, upd, closed, outScope)
	fmt.Println("(FOUND counts in-scope jobs only; OUT = listed by the source but outside the target countries, not stored)")

	problems := false
	for _, o := range outs {
		if o.Status == pipeline.StatusOK {
			continue
		}
		if !problems {
			fmt.Println("\nneeds attention:")
			problems = true
		}
		line := fmt.Sprintf("  %-9s %s: %s", o.Status, o.Source, o.Note)
		if o.Tripped {
			line += "  [AUTO-DISABLED by circuit breaker; fix then `scraper enable " + o.Source.String() + "`]"
		}
		fmt.Println(line)
	}
	fmt.Printf("\nsummary: %d ok, %d partial, %d failed, %d skipped\n",
		counts[pipeline.StatusOK], counts[pipeline.StatusPartial], counts[pipeline.StatusFailed], counts[pipeline.StatusSkipped])

	if counts[pipeline.StatusFailed] > 0 {
		return fmt.Errorf("%d source(s) failed", counts[pipeline.StatusFailed])
	}
	return nil
}

func cmdSources(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	ds, err := r.Store.ListSourceDetails(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%-26s %-16s %-5s %5s %-6s %-16s %s\n", "SOURCE", "COMPANY", "STATE", "FAILS", "EVERY", "LAST RUN", "NOTE")
	for _, d := range ds {
		state := "on"
		if !d.Enabled {
			state = "OFF"
		}
		last := "never"
		if d.LastRun != nil {
			last = d.LastRun.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("%-26s %-16s %-5s %5d %-6s %-16s %s\n", d.SourceRow.String(), trunc(d.CompanyName, 16),
			state, d.Failures, fmtEvery(d.Interval), last, trunc(d.DisabledReason, 70))
	}
	return nil
}

func cmdToggle(ctx context.Context, cfg *config.Config, enable bool, args []string) error {
	verb := "disable"
	if enable {
		verb = "enable"
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: scraper %s kind:token", verb)
	}
	kind, token, err := parseSource(args[0])
	if err != nil {
		return err
	}
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	if err := r.Store.SetSourceEnabled(ctx, kind, token, enable); err != nil {
		return err
	}
	fmt.Printf("%sd %s:%s\n", verb, kind, token)
	return nil
}

func cmdStats(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()

	st, err := r.Store.Stats(ctx)
	if err != nil {
		return err
	}
	mb := func(b int64) float64 { return float64(b) / (1 << 20) }
	fmt.Printf("companies %d | sources %d | runs %d\n", st.Companies, st.Sources, st.Runs)
	fmt.Printf("jobs: %d open, %d closed | active postings %d\n", st.OpenJobs, st.ClosedJobs, st.ActivePostings)
	fmt.Printf("raw payloads: %d (%.1f MB compressed) | database size: %.1f MB (limit ~1024 MB)\n",
		st.RawPayloads, mb(st.RawBytes), mb(st.DBBytes))
	if len(st.PerSource) > 0 {
		fmt.Println()
		fmt.Printf("%-26s %-16s %6s  %-8s %s\n", "SOURCE", "COMPANY", "OPEN", "STATUS", "LAST RUN")
		for _, s := range st.PerSource {
			last := "never"
			if s.LastRun != nil {
				last = s.LastRun.Local().Format("2006-01-02 15:04")
			}
			status := s.LastStatus
			if status == "" {
				status = "-"
			}
			fmt.Printf("%-26s %-16s %6d  %-8s %s\n", s.Kind+":"+s.Token, trunc(s.Company, 16), s.OpenJobs, status, last)
		}
	}
	return nil
}

func seedLocations(ctx context.Context, r *pipeline.Runner) (int, int, error) {
	var refs []store.CityRef
	for _, c := range normalizer.Cities() {
		if len(r.TargetCountries) > 0 && !contains(r.TargetCountries, c.Country) {
			continue
		}
		refs = append(refs, store.CityRef{Name: c.Name, Region: c.Region, Country: c.Country, Aliases: c.Aliases})
	}
	return r.Store.SeedLocations(ctx, refs)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func cmdSeedLocations(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	locs, aliases, err := seedLocations(ctx, r)
	if err != nil {
		return err
	}
	fmt.Printf("seeded %d locations and %d city aliases (countries: %s)\n", locs, aliases, scopeLabel(r.TargetCountries))
	return nil
}

func scopeLabel(targets []string) string {
	if len(targets) == 0 {
		return "all"
	}
	return strings.Join(targets, ",")
}

// cmdRenormalize re-derives all stale jobs from their stored raw payloads (no
// scraping), then merges any identical jobs it produced.
func cmdRenormalize(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()

	if _, _, err := seedLocations(ctx, r); err != nil {
		return fmt.Errorf("seed locations: %w", err)
	}
	start := time.Now()
	res, err := r.Renormalize(ctx, 250)
	if err != nil {
		return err
	}
	fmt.Printf("renormalized %d jobs in %s (%d failed, %d still below version %d)\n",
		res.Updated, time.Since(start).Round(time.Second), res.Failed, res.Remained, normalizer.Version)

	d, err := r.Store.Dedup(ctx, normalizer.Version)
	if err != nil {
		return fmt.Errorf("dedup: %w", err)
	}
	fmt.Printf("dedup: merged %d duplicate jobs into %d (identical company, title, place and description)\n", d.Removed, d.Groups)
	if res.Failed > 0 {
		return fmt.Errorf("%d jobs could not be re-derived", res.Failed)
	}
	return nil
}

func cmdDedup(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	d, err := r.Store.Dedup(ctx, normalizer.Version)
	if err != nil {
		return err
	}
	fmt.Printf("dedup: merged %d duplicate jobs into %d\n", d.Removed, d.Groups)
	return nil
}

func cmdQuality(ctx context.Context, cfg *config.Config) error {
	r, closeDB, err := newRunner(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	q, err := r.Store.Quality(ctx, normalizer.Version)
	if err != nil {
		return err
	}
	pct := func(n, d int) string {
		if d == 0 {
			return "  -"
		}
		return fmt.Sprintf("%3d%%", n*100/d)
	}
	fmt.Printf("open jobs: %d  (normalized with rules v%d: %d)\n", q.Open, normalizer.Version, q.Normalized)
	fmt.Printf("  country resolved   %6d  %s\n", q.CountryKnown, pct(q.CountryKnown, q.Open))
	fmt.Printf("  in India           %6d  %s\n", q.India, pct(q.India, q.Open))
	fmt.Printf("    with a city      %6d  %s of India jobs\n", q.IndiaWithCity, pct(q.IndiaWithCity, q.India))
	fmt.Printf("  several locations  %6d  %s\n", q.MultiLocation, pct(q.MultiLocation, q.Open))
	fmt.Printf("  remote type known  %6d  %s\n", q.RemoteKnown, pct(q.RemoteKnown, q.Open))
	fmt.Printf("  seniority known    %6d  %s\n", q.SeniorityKnown, pct(q.SeniorityKnown, q.Open))
	fmt.Printf("  employment known   %6d  %s\n", q.EmploymentKnown, pct(q.EmploymentKnown, q.Open))
	fmt.Printf("  experience parsed  %6d  %s   (only when the description states it)\n", q.ExperienceKnown, pct(q.ExperienceKnown, q.Open))
	fmt.Printf("  salary parsed      %6d  %s   (only when the posting states it)\n", q.SalaryKnown, pct(q.SalaryKnown, q.Open))
	printCounts := func(title string, cs []store.Count) {
		if len(cs) == 0 {
			return
		}
		fmt.Printf("\n%s\n", title)
		for _, c := range cs {
			fmt.Printf("  %6d  %s\n", c.N, c.Label)
		}
	}
	printCounts("India jobs by city (top 12):", q.IndiaCities)
	printCounts("India jobs by work arrangement:", q.IndiaRemote)
	printCounts("India jobs by seniority:", q.IndiaSeniority)
	printCounts("Locations still unresolved (no country) - add to the reference data if any are Indian:", q.Unresolved)
	return nil
}

func fmtEvery(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	return d.String()
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func usage() {
	fmt.Print(`scraper - job posting scraper

Usage:
  scraper <command> [flags]

Scraping:
  seed [--file F]                register sources from seeds/sources.txt
  run-due [flags]                scrape only sources that are due (what the daily cron runs)
  run --source kind:token        scrape one source (e.g. lever:paytm)
  run --all [flags]              scrape every enabled source, due or not
      --concurrency N            sources at once (default 3; per-host rate limit still applies)
      --accept-drop              close postings even if a source suddenly lists far fewer jobs

Source health:
  sources                        list sources with state, failures and last run
  enable  kind:token             re-enable a source (clears its failure count)
  disable kind:token             stop scraping a source
  stats                          counts, per-source status and database size

Data quality:
  renormalize                    re-derive stale jobs from stored raw payloads (no scraping), then dedup
  dedup                          merge identical jobs (same company, title, place and description)
  quality                        coverage of location, remote type, seniority, experience, salary
  seed-locations                 load reference cities and aliases into the database

Database:
  migrate | migrate-down | migrate-status | ping

Other:
  version

Sources: greenhouse, lever, ashby. Settings come from environment variables or a
.env file: DATABASE_URL, USER_AGENT, SCRAPE_MIN_INTERVAL, SCRAPE_MAX_FAILURES,
TARGET_COUNTRIES (default IN; jobs located only elsewhere are not stored, ALL disables).
`)
}
