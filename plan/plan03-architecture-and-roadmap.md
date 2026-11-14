# Plan 03 — Architecture, Free-Tier Deployment & Roadmap

**Status:** Draft v1
**Depends on:** plan01 (requirements), plan02 (schema)
**Diagram:** [hld-improved.png](hld-improved.png)

---

## 1. Locked decisions

| Area | Decision |
|---|---|
| Language | **Go** (single module, two entry points) |
| Database | **Neon Postgres** (free) — schema in plan02 |
| Scheduler + scraper runtime | **GitHub Actions** scheduled workflows (free), **once per day** (e.g. `30 0 * * *` UTC = 6:00 AM IST), plus `workflow_dispatch` for manual runs |
| API + dashboard hosting | **Vercel Hobby** (free, non-commercial use) |
| Raw payloads | In DB (`posting_raw`, gzip, latest-only, pruned) |
| Alerts | Telegram bot (free) |
| Enrichment | Rules/taxonomy first; LLM only for gaps (optional free tier) |
| Geography | **India only for now** (`TARGET_COUNTRIES=IN`); the data model stays global |
| Sources order | ATS APIs → company career pages → aggregators → (optional) Naukri/Instahyre. LinkedIn excluded |

> **Why the scraper is not on Vercel:** Vercel is serverless — no long-running workers or in-process cron, short function limits, and Hobby cron is ~1×/day. So the scraper runs in GitHub Actions, and Vercel only serves reads.

---

## 2. Architecture

```
┌────────────── GitHub Actions (cron, free) ──────────────┐
│ cmd/scraper                                             │
│  Registry → pick due sources → Validator (robots, rate, │
│  circuit breaker) → Fetcher pool (goroutines) →         │
│  Parser → Normalizer → Enricher → Dedup/Lifecycle       │
└───────────────┬─────────────────────────────────────────┘
                │ writes
                ▼
        Neon Postgres (free)  ◄──── reads ────┐
                │                              │
                ▼                              │
   Telegram alerts (from scraper job)   Vercel Hobby
                                        ├─ api/*.go  (Go serverless, read-only)
                                        └─ web/      (dashboard)
```

Pipeline stages map 1:1 to the HLD diagram (numbers 1–14 in the image).

---

## 3. Repository layout

```
e:\season1\
├─ plan\                       # these planning docs + diagram
├─ cmd\
│  └─ scraper\main.go          # CLI: run-due | run --source X | prune | migrate
├─ api\                        # Vercel Go functions (jobs.go, companies.go, stats.go)
├─ internal\
│  ├─ config\                  # env, limits
│  ├─ registry\                # sources: load, pick due, record health
│  ├─ validator\               # robots.txt, per-domain rate limiter, circuit breaker
│  ├─ fetcher\                 # common interface + adapters
│  │   ├─ greenhouse\  lever\  ashby\  workable\
│  │   ├─ jsonld\              # schema.org JobPosting from career pages
│  │   └─ browser\             # optional headless fallback
│  ├─ raw\                     # gzip store/load for posting_raw
│  ├─ parser\                  # adapter payload -> RawJob
│  ├─ normalizer\              # location(IN), experience, salary, title, remote
│  ├─ enricher\                # skills taxonomy (+ optional LLM)
│  ├─ dedup\                   # fingerprint + trigram match, lifecycle
│  ├─ alerts\                  # watchlists, Telegram sender
│  └─ store\                   # pgx + sqlc generated code, queries\
├─ migrations\                 # goose: 0001_p0 ... 0004_p3
├─ seeds\                      # skills, india_locations, sources
├─ web\                        # dashboard (Next.js or static + JS)
└─ .github\workflows\
   ├─ scrape.yml               # cron + workflow_dispatch
   └─ prune.yml                # nightly raw pruning + db-size check
```

### Core interface (adapter contract)
```go
type Adapter interface {
    Kind() string
    // Fetch returns all currently listed jobs for one source.
    Fetch(ctx context.Context, src Source) ([]RawJob, error)
}
```
A new source = one new adapter package + one row in `sources`. The pipeline does not change.

---

## 4. Environment & secrets (GitHub + Vercel)

| Variable | Where | Purpose |
|---|---|---|
| `DATABASE_URL` | Actions secret, Vercel env | Neon connection (use pooled URL for Vercel) |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Actions secret | Alerts |
| `LLM_API_KEY` (optional) | Actions secret | Enrichment gaps |
| `USER_AGENT` | Actions env | Identifiable bot name + contact |

Secrets are never committed. Use a `.env.example` only.

---

## 5. Roadmap (phases, each ends with a working demo)

### Phase 0 — Foundations
- [ ] Create Neon project (branches: `main`, `dev`), enable `pg_trgm`
- [ ] `go mod init`, folder skeleton, `goose` migrations P0, `sqlc` config
- [ ] GitHub repo + empty `scrape.yml` workflow, Vercel project linked
- **Demo:** `scraper migrate` creates P0 tables on Neon

### Phase 1 — Vertical slice (Greenhouse)
- [x] Greenhouse adapter, 16 seed companies (India-hiring) — `seeds/sources.txt`
- [x] Upsert `job_postings` + `posting_raw` (gzip), idempotent
- [x] Minimal `jobs` creation (1 posting → 1 job)
- [x] `seed`, `run --source`, `run --all`, `stats` commands
- [x] Closed-job detection, with a guard against closing everything when a board returns 0 jobs
- Note: used plain pgx queries instead of sqlc (about a dozen queries; sqlc deferred); migration 0002 made `jobs.country` nullable until the Phase 3 normalizer
- **Demo:** `scraper run --source greenhouse:<company>` fills DB; second run adds 0 duplicates

### Phase 2 — Reliable ingestion
- [x] Lever + Ashby adapters (Lever supplies country; Ashby filters unlisted postings)
- [x] Validator as a polite HTTP client: robots.txt (RFC 9309, `Crawl-delay` honoured), per-host rate limit, retries with jittered backoff and `Retry-After`
- [x] Circuit breaker (auto-disable after N consecutive failures), run lock, mass-drop guard
- [x] `run-due` scheduler logic with a 1-hour tolerance; `sources`, `enable`, `disable` commands
- [x] GitHub Actions daily cron (`run-due`), manual `due` / `all` dispatch, keep-alive step
- [x] 26 sources seeded: 16 Greenhouse, 6 Lever, 4 Ashby (6,467 jobs)
- Findings: Greenhouse robots only blocks `/embed/`; Lever allows all with `Crawl-delay: 1`; Ashby's robots returns 401, which RFC 9309 treats as "no restrictions".
- **Demo:** unattended runs, run history visible, removed jobs marked closed, a repeat `run-due` finds nothing due

### Phase 3 — Clean data (India focus)
- [x] **Scope decision: India only.** `TARGET_COUNTRIES=IN`; non-India postings are not stored, existing ones are closed (not deleted), unresolved locations are kept
- [x] Location normalizer: embedded reference data (countries, US/CA/IN regions, 58 Indian cities with aliases, ~150 world cities used to recognise and exclude non-India jobs); handles `Bangalore, IND`, `Pune, IN`, `Mumbai/Delhi/Bengaluru`, `Remote - USA`, `Sydney Or Melbourne`, and the IN / Indiana, Washington / DC, Mumbai-Delhi ambiguities
- [x] Experience parser (`5+ years`, `3-5 yrs`, fresher wording; rejects `founded 15 years ago`), salary parser (US, EU `€48.000`, INR `₹5,900,000`, `12-18 LPA`; rejects bonuses and year ranges), remote / employment / seniority, title normalization
- [x] Structured source hints (Lever, Ashby, Greenhouse metadata) take priority over text parsing
- [x] Adapters expose `Parse(payload)`, so jobs are re-derived from `posting_raw` without re-scraping (`renormalize`, versioned rules)
- [x] Dedup by exact fingerprint **including the description** (see plan02 section 11); `quality` report
- Measured on 1,067 open jobs: 90% country resolved, 89% in India (94% of those with a city), 77% experience parsed, 62% remote type, 49% seniority, 2% salary (Indian postings rarely state pay)
- Plausibility check: average minimum experience rises with seniority (junior 2.2, senior 6.2, staff 9.0, director 10.7)
- Known gaps: misspelt regions (`Uttrakhand`) are not recognised; trigram/fuzzy cross-source dedup is not built because no job currently exists on two platforms
- **Demo:** same job listed twice shows once; `Bangalore, IND` becomes Bengaluru, Karnataka, IN; `make quality`

### Phase 4 — API + dashboard
- [x] Read-only API (`internal/api`): `jobs` (full-text search with web-search syntax, city with aliases, company, remote, level, type, experience, posting date, salary, pagination), `job`, `facets`, `stats`, `sources`, `health`
- [x] One handler, two hosts: `cmd/serve` (local) and `api/index.go` (Vercel function, routed via `vercel.json`)
- [x] Dashboard in `public/` (plain HTML/JS, no build step): search, filters kept in the URL, highlighted match snippets, job detail dialog, source-health page, light/dark, responsive
- [x] Safety: bound parameters only, strict validation (400s), generic 500s, read-only, CSP with no inline script, dashboard inserts job text as text and links only to http(s)
- [x] Tests: 13 handler tests with a fake store (validation, errors, headers, caching, routing), 4 query-builder safety tests, 2 database integration tests (every filter, sort, paging, detail, facets, source health), 12 dashboard helper tests (Node)
- Measured (local to Neon in Ohio, from India): search ~550 ms (one round trip), cached aggregates 13 ms; first facets call was 2.5 s before its five queries were made concurrent
- Decisions: `public/` instead of `web/` (Vercel serves it with no config); "new this week" counts by posting date, not by when we first scraped it
- **Deployed and working on Vercel** (Hobby). Two build problems showed up on the first deploy and were fixed: `.vercelignore` excluded the SQL files the code embeds, and `api/index.go` (compiled by Vercel outside the module path) could not import `internal/` — it now calls the public `serverless` package
- **Demo:** `make serve`, search "kubernetes" in Bengaluru, open a job, follow the link

### Phase 4b — UI revamp and a real fresher view
Triggered by real use: a fresher search returned almost nothing useful.
- [x] Measured first: only 17 India jobs stated a minimum of 0–1 years. The old `exp=1` filter also returned every job with *no* stated experience, so the "0–1 year" list was mostly staff and senior roles
- [x] One definition of **fresher / entry level** in SQL (`entryLevelSQL`): never a senior title; stated minimum ≤ 1 year; or (nothing stated) an intern/junior role. Used by the `entry_level=1` filter, the quick-view counts and a per-job `entry_level` flag, so it exists in one place only
- [x] New dashboard: sidebar with search, quick views, experience and every filter; list beside a detail pane; light/dark; phone layout with a filter drawer. Inter font self-hosted and Lucide icons inline (the CSP allows no third-party requests); no framework, no build step
- [x] Data fixes found in the real data (normalizer v3): "Bangkok based" hid the city, Indiana cities (`Evansville, IN`) were read as India, Yokohama was missing. Stored jobs are re-derived from raw payloads; out-of-scope ones are closed by the next scrape
- [x] 14 more India-hiring sources (Sigmoid, InMobi, Agoda, Toast, HighRadius, Glance, Zenoti, HackerRank, 6sense, Observe.AI, Samsara, FamPay, Snowflake, Gainsight), probed first with the polite client (178 candidates, 48 boards exist, 14 with ≥ 5 India jobs)
- [x] Tests: 14 handler tests, 5 query-builder tests (the rule is checked in the WHERE clause only), 3 database search tests (including the awkward cases: a senior title stating 1 year, a junior title stating 4, no signal at all), 17 dashboard helper tests, 76 location cases
- Known limits: the three systems we scrape (Greenhouse, Lever, Ashby) skew senior. More fresher jobs need other sources (Workday, SmartRecruiters, career pages, Naukri/Instahyre) — Phase 6
- Still open data quality: global "Remote" roles and macro-regions ("EMEA", "N/A") stay unresolved on purpose

### Phase 4c — Tech focus and big-company sources
Triggered by real use: a software developer saw HR, finance and sales jobs, and none of the big names.
- [x] Measured first: of 1,364 open jobs about 540 were software-ish (the rest HR, finance, sales, support); none of Google, Amazon, Microsoft, Apple, Meta, Netflix, Uber, Adobe or Salesforce were present
- [x] **Role family** on every job (software, AI/ML/data, DevOps/cloud/security, QA), derived from title + department (normalizer v4–v8), with a `role` API filter, role counts in the facets, and a Role control in the sidebar. Reviewed on real data in three rounds; each round found a real mistake (product managers labelled AI, "Staff Engineer" missed because departments are team names, Amazon's shorthand BIE/QAE, warehouse technicians)
- [x] **Amazon** source (amazon.jobs search API, India, technical categories only): 574 jobs, 535 after merging duplicates, 501 with years of experience parsed
- [x] **Atlassian** source (one JSON listing): 34 India jobs
- [x] Checked each candidate's robots.txt and API before promising anything. **Not built, on purpose:** Naukri and LinkedIn (terms; Naukri also stalls identified bots), Meta (robots.txt prohibits automated collection), Google (disallows paging), Uber (disallows /api/), SmartRecruiters (api host disallows /)
- [x] **Workday** adapter (India only, technical titles only, discovers each company's country filter): Salesforce and Adobe verified live. **Eightfold** adapter (PCSX API): Microsoft verified live. Three bugs found only by running against the real services and fixed with tests: Workday sends the list total on the first page only (a 103-job board returned 40), Microsoft sends an empty `error` object with every success, and Microsoft rejects over half of requests with 429 (so the adapter waits and retries with growing pauses)
- [ ] Workday companies not yet verified because their servers were in maintenance: Intel, Mastercard (wd1), Dell, Walmart, Qualcomm. Add a line to seeds/sources.txt for each once reachable. Nvidia left Workday. Netflix has PCSX switched off (its other API: 11 India jobs, none technical). Apple needs a token handshake and is skipped
- [x] Tests: 111 role-classification cases, 1 new database test for the role filter and counts (4 search database tests in all), 7 Amazon and 4 Atlassian adapter tests (fixtures are real jobs), 18 dashboard helper tests

### Phase 5 — Intelligence & alerts
- [ ] P1 tables live: skills taxonomy + `job_skills`, multi-location
- [ ] Optional LLM enrichment for descriptions the rules miss (batched, cached)
- [ ] Watchlists + Telegram notifications (dedup via `notifications` unique key)
- [ ] Trend views: top skills by city, hiring velocity (`job_events`)
- **Demo:** Telegram ping when a watched company opens a matching role

### Phase 6 — Breadth & hardening
- [ ] Career-page adapter (JSON-LD, sitemaps), aggregator APIs
- [ ] Fixture-based tests for every adapter; schema-drift alerts
- [ ] Prune workflow + DB-size guard (alert at 80% of budget)
- [ ] Move `posting_raw` to R2 only if size forces it

### Phase 7 — Optional / later
- [ ] Naukri / Instahyre (public pages, low volume, off by default)
- [ ] pgvector embeddings, resume matcher, user profiles (P3)

---

## 6. Definition of done (maps to plan01 success criteria)
- [ ] ≥ 3 adapters working (Greenhouse, Lever, Ashby)
- [ ] 100+ companies, 5,000+ active jobs
- [ ] Duplicate rate < 2%
- [ ] ≥ 90% jobs with normalized location and parsed experience
- [ ] Re-run produces no duplicates; removed jobs marked closed
- [ ] Search API < 500 ms for typical queries
- [ ] DB stays under ~600 MB on Neon free tier (1 GB limit, 40% headroom)

---

## 7. Risks specific to this setup

| Risk | Mitigation |
|---|---|
| Neon free storage (1 GB) with raw payloads in DB | Compress, latest-only, prune, size alerts, R2 escape hatch |
| GitHub Actions IPs blocked by some sites | ATS APIs first; skip/disable blocked sources automatically |
| Scheduled workflows pause after 60 days of repo inactivity (daily runs do not count as activity) | Keep-alive step in the workflow, or manual dispatch; `cron-job.org` as backup trigger |
| Daily schedule means alerts lag up to 24h | Acceptable for personal use; `scrape_every` can be lowered per source later |
| Vercel Hobby = non-commercial | Fine for personal/portfolio; revisit if it becomes a product |
| Neon cold start on idle DB | Accept ~1s first-query latency; cache hot API responses |
| Free-tier limits change over time | Re-check provider pricing pages before each phase |
| ToS / legal exposure on job boards | Excluded LinkedIn; Naukri/Instahyre optional, public pages only |

---

## 8. Immediate next step
Start **Phase 0**: create the Neon project, give me the connection string via an environment variable (never paste it into chat or commit it), and I will scaffold the Go module, migrations and the first adapter.
