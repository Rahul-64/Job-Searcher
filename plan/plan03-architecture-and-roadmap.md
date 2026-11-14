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
| Geography | India-first normalization; global-capable model |
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
- [ ] Lever + Ashby adapters
- [ ] Validator: robots.txt, per-domain rate limiter, retries/backoff, circuit breaker
- [ ] Scheduler logic: pick due sources, `scrape_runs` stats, closed-job detection
- [ ] GitHub Actions cron running every N hours
- **Demo:** unattended runs, run history visible, removed jobs marked closed

### Phase 3 — Clean data
- [ ] Normalizer: India city aliases (seed), experience parser, salary parser, remote detection, title normalization
- [ ] Non-India locations: best-effort country detection, never dropped
- [ ] Dedup: fingerprint + trigram, link postings → canonical job
- **Demo:** same job from 2 sources shows as 1 job; "3-5 yrs" becomes 3/5; BLR → Bengaluru

### Phase 4 — API + dashboard (Vercel)
- [ ] `api/jobs` (search, filters, pagination, `country` param default IN), `api/companies`, `api/stats`
- [ ] Dashboard: search, filters, job detail, source health page
- **Demo:** public URL, filter by city/skill/experience

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
