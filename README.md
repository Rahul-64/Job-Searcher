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

## API and dashboard

```powershell
make serve      # http://127.0.0.1:8080  (dashboard + API, localhost only)
```

The dashboard (`public/`) is plain HTML and JavaScript with no build step. Search, filter by city,
company, work arrangement, level, type, posting date and salary, tell it how many years of experience
you have, and open any job for the full description and a link to apply on the company's site. Filters are
kept in the URL, so a search can be bookmarked or shared. `#sources` shows the health of every source.

| Endpoint | Purpose |
|---|---|
| `GET /api/jobs` | Search. Parameters: `q` (web-search syntax: `go backend`, `"data engineer"`, `-intern`), `city` (aliases work: `blr`), `company`, `remote`, `seniority`, `employment`, `exp`, `hide_unknown_exp`, `posted_within`, `has_salary`, `sort`, `limit` (max 100), `offset` |
| `GET /api/job?id=` | One job: full description, all locations, every source posting |
| `GET /api/facets` | Filter options with counts |
| `GET /api/stats` | Totals and last scrape time |
| `GET /api/sources` | Source health |
| `GET /api/health` | Database reachable? |

The API is read-only (other methods get 405). Every query value is a bound SQL parameter, bad input gets a
400 with a clear message, and internal errors return a generic 500 without database details. Job text comes
from third-party sites, so the dashboard only ever inserts it as text (never HTML) and only links to
`http(s)` URLs; a Content-Security-Policy forbids inline scripts.

### Deploying to Vercel (free Hobby plan)
1. In Vercel choose **Add New → Project** and import this GitHub repository. Framework preset **Other**, no build command.
2. Add the environment variable `DATABASE_URL` with Neon's **pooled** connection string (Production and Preview).
3. Deploy. `vercel.json` rewrites `/api/*` to the single Go function in `api/index.go`; `public/` is served as static files.

**Not yet verified on Vercel itself.** Everything here was tested locally against the real database through
the same handler the function uses, but the deployment has not been run. Two things to check on the first
deploy: that Vercel's Go runtime accepts the Go version in `go.mod`, and that the `/api/*` rewrite delivers the
route (the handler reads it from `?_route=`). The Hobby plan is for non-commercial use.

## India focus and data quality

The scraper currently collects **India only** (`TARGET_COUNTRIES=IN`). Every posting's location is
normalized; a job whose places are all outside India is not stored, and one whose location cannot be
resolved is kept (so nothing Indian is lost to a parsing gap). Jobs already stored that fall out of scope
are *closed*, never deleted.

```powershell
make quality         # coverage of location, remote type, seniority, experience, salary
make renormalize     # re-derive jobs from stored raw payloads (no scraping), then dedup
```

What is derived for each job: normalized title and seniority, one or more places (city, state, country),
remote / hybrid / onsite, employment type, years of experience, and salary. Source data is used when the
source provides it (Lever, Ashby); otherwise it is parsed from the text, favouring precision over recall:
a field stays empty rather than being guessed. Rules are versioned (`normalizer.Version`); after changing
them, `make renormalize` updates every job in seconds from the stored payloads.

Identical postings (same company, title, place **and description**) become one job with several postings.
The description is part of the match on purpose: many roles share a title and city but belong to different
teams.

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
