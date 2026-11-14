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

## Scraping (Greenhouse, Lever, Ashby, Amazon, Atlassian, Workday, Eightfold)

| Kind | Source | Token | Notes |
|---|---|---|---|
| `greenhouse`, `lever`, `ashby` | each company's public job board API | the board name | most startups and scale-ups |
| `amazon` | amazon.jobs search API | an ISO alpha-3 country code (`IND`) | only technical categories are fetched (software, applied science, data, BI, research, systems/security engineering, database administration): India lists about 2,000 jobs, most of them warehouse, HR and finance |
| `atlassian` | one JSON listing of every opening | `listings` | all openings worldwide; the pipeline keeps India's |
| `workday` | a company's Workday career site (the JSON calls its own page makes) | `tenant/pod/site`, e.g. `salesforce/wd12/External_Career_Site` | India only: the country filter's name differs per company, so it is found in the site's facets. Needs one request per job for its description, so jobs whose title is clearly not technical are skipped first |
| `eightfold` | Eightfold's PCSX API (Microsoft) | `host/domain/location`, e.g. `apply.careers.microsoft.com/microsoft.com/India` | the site's robots.txt allows `/api/pcsx` explicitly. Microsoft answers more than half of requests with 429 ("try again later") whatever the pace, so the adapter waits 10s, 20s, 40s, 60s... and retries |

Workday and Eightfold only keep jobs whose title looks technical (`normalizer.RoleFamily`); the rest never
reach the database. In the seed file today: Salesforce, Adobe (Workday) and Microsoft (Eightfold).
Intel, Mastercard, Dell, Walmart and Qualcomm are Workday companies too but their servers were in
maintenance when this was built, so they are not verified yet; Nvidia has left Workday. Netflix has PCSX
switched off and its only other API lists a handful of India jobs, none technical.

Every adapter goes through the same polite client (robots.txt, rate limits, retries), keeps each job's
original payload, and re-derives from it without re-scraping.

**Not scraped on purpose:** LinkedIn and Naukri (their terms forbid scraping and they block identified bots;
the only way in would be disguising the scraper), Meta (its robots.txt prohibits automated collection),
Google (robots.txt disallows paging through results) and Uber (robots.txt disallows its `/api/`).

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

The dashboard (`public/`) is plain HTML and JavaScript with no build step and no third-party requests
(the Inter font is served from `public/fonts/`, icons are inline). A sidebar holds search and every filter:
quick views (all jobs, **fresher and entry level**, internships, remote, new this week), your years of
experience, location, work arrangement, level, type, company, posting date and pay. The job list sits
beside a detail pane with the full description and a link to apply on the company's site. Filters are kept
in the URL, so a search can be bookmarked or shared, and `#sources` shows the health of every source.
Press `/` to jump to the search box. Light and dark themes follow the system and can be switched.

**Role** (software engineering; AI, ML & data; DevOps, cloud & security; QA & test, or all of them as
"Software & AI roles") is derived from the job title, with the department as a tie-breaker for a bare
"Engineer" (`internal/normalizer/role.go`). It is deliberately conservative: a title that is not clearly
technical gets no role rather than a guess, so product managers, solutions and support engineers, sales
engineers and developer advocates are not counted as software jobs. Departments are team names ("Cloud Data
Path"), so they can show a job is *not* technical (Sales, Support) but never prove that it is.

**Fresher / entry level** is one rule, defined once in SQL (`entryLevelSQL` in `internal/store/search.go`):
never a senior, lead, staff, principal, director or executive title; if a minimum experience is stated it
must be 1 year or less; if none is stated the job must be an intern or junior role. Jobs that state nothing
and carry no junior signal are not guessed to be entry level. The filter, the quick-view counts and the
per-job `entry_level` flag all use that one definition.

| Endpoint | Purpose |
|---|---|
| `GET /api/jobs` | Search. Parameters: `q` (web-search syntax: `go backend`, `"data engineer"`, `-intern`), `city` (aliases work: `blr`), `company`, `remote`, `seniority`, `employment`, `exp`, `hide_unknown_exp`, `entry_level` (1 = fresher / entry level), `role` (`tech`, `software`, `ai_data`, `infra_security`, `qa`), `posted_within`, `has_salary`, `sort`, `limit` (max 100), `offset`. Each job carries `entry_level` and `role_family` |
| `GET /api/job?id=` | One job: full description, all locations, every source posting |
| `GET /api/facets` | Filter options with counts, plus `roles` and `quick` counts for the role filter and quick views |
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

Deployed and working on the Hobby plan (which is for non-commercial use). Vercel's Go runtime reads the Go
version from `go.mod`. It compiles `api/index.go` outside this module's path, so that file may not import
`internal/` packages directly; it calls the public `serverless` package instead, which does. Do not exclude
`migrations/*.sql` or `internal/normalizer/data/*.csv` in `.vercelignore`: the code embeds them.

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
api/             Vercel function entry (a thin wrapper)
serverless/      public package the Vercel function calls (it may import internal/)
internal/        config, store, registry, validator, fetcher, parser, normalizer, ...
migrations/      goose SQL migrations (embedded into the binary)
seeds/           seed data (skills, India locations, sources)
public/          dashboard: HTML, CSS, JS, font (no build step)
.github/         daily scrape workflow (cron, 06:00 IST)
```

## GitHub Actions secrets
`DATABASE_URL` (required), `DATABASE_URL_DIRECT` (optional, non-pooled Neon URL for migrations).
Optional repository variable: `USER_AGENT` (use a name with a contact address).
