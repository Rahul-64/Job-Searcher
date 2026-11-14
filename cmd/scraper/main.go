// Command scraper is the entry point for the job-scraper pipeline.
//
// Phase 1 commands: migrate*, ping, seed, run, stats, version.
// Scheduling, pruning and the validator arrive in later phases.
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
	"jobscraper/internal/fetcher/greenhouse"
	"jobscraper/internal/pipeline"
	"jobscraper/internal/store"
)

const version = "0.2.0-phase1"

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
	case "stats":
		return cmdStats(ctx, cfg)
	case "run-due", "prune":
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

// newRunner opens the database and builds a pipeline runner with all adapters.
func newRunner(ctx context.Context, cfg *config.Config) (*pipeline.Runner, func(), error) {
	url, err := cfg.RequireDatabaseURL()
	if err != nil {
		return nil, nil, err
	}
	pool, err := store.Open(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	gh := greenhouse.New(cfg.UserAgent)
	r := &pipeline.Runner{
		Store:    store.New(pool),
		Adapters: map[string]fetcher.Adapter{gh.Kind(): gh},
		Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
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

// readSeeds reads kind:token lines, ignoring blanks and # comments.
func readSeeds(path string) ([][2]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][2]string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, t, err := parseSource(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, [2]string{k, t})
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
	file := fs.String("file", "seeds/sources.txt", "file listing kind:token sources")
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
		src, err := r.EnsureSource(ctx, s[0], s[1])
		if err != nil {
			failed++
			fmt.Printf("  FAIL  %s:%s  %v\n", s[0], s[1], err)
		} else {
			fmt.Printf("  ok    %-22s %s\n", src.String(), src.CompanyName)
		}
		if i < len(seeds)-1 {
			if err := pause(ctx, 500*time.Millisecond); err != nil {
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

func cmdRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	source := fs.String("source", "", "scrape one source, as kind:token (e.g. greenhouse:groww)")
	all := fs.Bool("all", false, "scrape every enabled source, one after another")
	delay := fs.Duration("delay", time.Second, "pause between sources when using --all")
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

	if *source != "" {
		kind, token, err := parseSource(*source)
		if err != nil {
			return err
		}
		src, err := r.EnsureSource(ctx, kind, token)
		if err != nil {
			return err
		}
		_, err = r.RunSource(ctx, src)
		return err
	}

	srcs, err := r.Store.ListSources(ctx, true)
	if err != nil {
		return err
	}
	if len(srcs) == 0 {
		return errors.New("no enabled sources; run `scraper seed` first")
	}
	failed := 0
	for i, src := range srcs {
		if _, err := r.RunSource(ctx, src); err != nil {
			failed++
			r.Log.Error("source failed", "source", src.String(), "err", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i < len(srcs)-1 {
			if err := pause(ctx, *delay); err != nil {
				return err
			}
		}
	}
	fmt.Printf("done: %d sources, %d failed\n", len(srcs), failed)
	if failed > 0 {
		return fmt.Errorf("%d sources failed", failed)
	}
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
		fmt.Printf("%-22s %-14s %6s  %-8s %s\n", "SOURCE", "COMPANY", "OPEN", "STATUS", "LAST RUN")
		for _, s := range st.PerSource {
			last := "never"
			if s.LastRun != nil {
				last = s.LastRun.Local().Format("2006-01-02 15:04")
			}
			status := s.LastStatus
			if status == "" {
				status = "-"
			}
			fmt.Printf("%-22s %-14s %6d  %-8s %s\n", s.Kind+":"+s.Token, trunc(s.Company, 14), s.OpenJobs, status, last)
		}
	}
	return nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func usage() {
	fmt.Print(`scraper - job posting scraper

Usage:
  scraper <command> [flags]

Database:
  migrate           apply pending database migrations
  migrate-down      roll back the most recent migration
  migrate-status    show migration status
  ping              check the database connection
  stats             show counts, per-source status and database size

Scraping:
  seed [--file F]               register sources from seeds/sources.txt
  run --source kind:token       scrape one source (e.g. greenhouse:groww)
  run --all [--delay 1s]        scrape every enabled source sequentially

Other:
  version           print version

Planned: run-due (phase 2), prune (phase 6).
Configuration comes from environment variables or a .env file (see .env.example).
`)
}
