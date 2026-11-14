// Command scraper is the entry point for the job-scraper pipeline.
//
// Phase 0 implements: migrate, migrate-down, migrate-status, ping, version.
// The scraping commands (run, run-due, prune) arrive in later phases.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"jobscraper/internal/config"
	"jobscraper/internal/store"
)

const version = "0.1.0-phase0"

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
	cmd := args[0]

	if cmd == "version" {
		fmt.Println(version)
		return nil
	}
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		usage()
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch cmd {
	case "migrate":
		url, err := cfg.MigrationURL()
		if err != nil {
			return err
		}
		return store.MigrateUp(ctx, url)
	case "migrate-down":
		url, err := cfg.MigrationURL()
		if err != nil {
			return err
		}
		return store.MigrateDown(ctx, url)
	case "migrate-status":
		url, err := cfg.MigrationURL()
		if err != nil {
			return err
		}
		return store.MigrateStatus(ctx, url)
	case "ping":
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
	case "run", "run-due", "prune":
		return fmt.Errorf("%q is not implemented yet (planned for a later phase)", cmd)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Print(`scraper - job posting scraper

Usage:
  scraper <command>

Commands (available now):
  migrate           apply pending database migrations
  migrate-down      roll back the most recent migration
  migrate-status    show migration status
  ping              check the database connection
  version           print version

Planned:
  run --source K:T  scrape one source        (phase 1)
  run-due           scrape all due sources   (phase 2)
  prune             prune raw payloads       (phase 6)

Configuration is read from environment variables or a .env file
(see .env.example).
`)
}
