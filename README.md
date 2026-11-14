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

## Scraping (Greenhouse, Lever, Ashby)

```powershell
make seed                          # register the sources in seeds/sources.txt
make run-due                       # scrape only sources that are due (what the daily cron runs)
make run SOURCE=lever:paytm        # scrape one source
make run-all                       # scrape every enabled source, due or not
make sources                       # list sources: state, failures, last run
make stats                         # counts, per-source status, database size
```

Re-running is safe: unchanged jobs are left alone, changed jobs are updated, and jobs that
disappear from a board are marked closed. Add companies by appending `kind:token [Display Name]`
lines to `seeds/sources.txt` and running `make seed`.

### Built-in safeguards
| Safeguard | Behaviour |
|---|---|
| **robots.txt** | Checked for every host (RFC 9309): a missing/4xx file means allowed, a 5xx means blocked. `Crawl-delay` is honoured. |
| **Rate limit** | At least `SCRAPE_MIN_INTERVAL` (default 1s) between requests to one host, even with concurrent workers. |
| **Retries** | Network errors, 429 and 5xx retry with jittered exponential backoff and `Retry-After`; 4xx never retries. |
| **Circuit breaker** | After `SCRAPE_MAX_FAILURES` (default 5) consecutive failures a source is disabled with a reason. Fix it, then `make enable SOURCE=kind:token`. |
| **Mass-drop guard** | If a source returns 0 jobs, or under half of a 20+ job board, nothing is closed. Override with `run --accept-drop` once you have checked. |
| **Run lock** | Two overlapping runs of one source cannot both start; a crashed run is reclaimed after 30 minutes. |

## Daily schedule (GitHub Actions)
[`.github/workflows/scrape.yml`](.github/workflows/scrape.yml) runs `run-due` at 06:00 IST. It also runs
manually from the Actions tab (choose `due` or `all`). A source is "due" 23 hours after its last run
(a 1-hour tolerance, so a run that finished a few minutes late yesterday is not skipped today).

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
Optional repository variable: `USER_AGENT` (use a name with a contact address).
