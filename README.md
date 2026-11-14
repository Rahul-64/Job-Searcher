# Job Scraper

Collects job postings from ATS public APIs and company career pages, normalizes and
deduplicates them, and serves them through an API and dashboard. Built in Go on free tiers
(Neon Postgres, GitHub Actions, Vercel Hobby).

Planning docs live in [`plan/`](plan/): requirements (plan01), DB schema (plan02),
architecture and roadmap (plan03).

## Quick start (Phase 0)

1. Create a free project at [neon.tech](https://neon.tech) and copy the connection string.
2. `copy .env.example .env` and set `DATABASE_URL` (never commit `.env`).
3. Run:

```powershell
go run ./cmd/scraper ping             # check connectivity
go run ./cmd/scraper migrate          # create the P0 tables
go run ./cmd/scraper migrate-status   # confirm
```

## Scraping (Phase 1: Greenhouse)

```powershell
make seed                          # register the boards in seeds/sources.txt
make run SOURCE=greenhouse:groww   # scrape one board
make run-all                       # scrape every enabled board
make stats                         # counts, per-source status, database size
```

Re-running is safe: unchanged jobs are left alone, changed jobs are updated, and jobs that
disappear from a board are marked closed. If a board suddenly returns 0 jobs, nothing is closed.
Add companies by appending `greenhouse:<token>` lines to `seeds/sources.txt` and running `make seed`.

## Make targets

A [Makefile](Makefile) wraps the common commands. On this Windows machine the binary is
`mingw32-make` (in PowerShell: `Set-Alias make mingw32-make` to type `make` instead).

```powershell
make help             # list all targets
make check            # fmt + vet + test + build
make ping             # database connectivity
make migrate          # apply migrations
make migrate-status
```

## Layout

```
cmd/scraper/     CLI entry point
api/             Vercel Go functions (phase 4)
internal/        config, store, registry, validator, fetcher, parser, normalizer, ...
migrations/      goose SQL migrations (embedded into the binary)
seeds/           seed data (skills, India locations, sources)
web/             dashboard (phase 4)
.github/         daily scrape workflow (cron, 06:00 IST)
```

## GitHub Actions secrets
`DATABASE_URL` (required), `DATABASE_URL_DIRECT` (optional, non-pooled Neon URL for migrations).
Optional repository variable: `USER_AGENT`.
