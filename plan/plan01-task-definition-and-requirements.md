# Plan 01 — Task Definition & Requirements

**Project:** Job Data Scraper & Intelligence Platform
**Status:** Draft v2 — language, DB and hosting decisions resolved, see plan02 and plan03
**Update:** Stack changed to **Go + Neon Postgres + GitHub Actions + Vercel Hobby** (all free tier). The Python/React/Streamlit choices in §5 are superseded by plan03.
**Date:** 2026-11-14

---

## 1. Task Definition

Build a system that automatically collects job postings from multiple online sources, cleans and standardizes them into one schema, removes duplicates, enriches them with extracted insights (skills, seniority, salary), and exposes them through a searchable API and dashboard.

### 1.1 Problem statement
Job postings are scattered across job boards (LinkedIn, Naukri, Instahyre, etc.) and thousands of individual company career portals. Each uses a different format, so there is no single, clean, up-to-date view of the job market.

### 1.2 Goal
One unified, deduplicated, continuously refreshed dataset of job postings, with a layer of intelligence on top (search, trends, alerts, matching).

### 1.3 Out of scope (for now)
- Auto-applying to jobs
- Scraping behind logins or using personal accounts
- Collecting recruiter/personal contact data
- Commercial resale of data (needs legal review first)

---

## 2. Sources & Priority

| Priority | Source type | Examples | Method | Risk |
|---|---|---|---|---|
| P1 | ATS public APIs | Greenhouse, Lever, Ashby, Workable, SmartRecruiters, Recruitee | Direct JSON endpoints | Low |
| P2 | Company career pages | Custom portals, Workday | schema.org `JobPosting` JSON-LD, sitemaps, Playwright | Low–Medium |
| P3 | Job aggregator APIs | Adzuna, Jooble, The Muse, RemoteOK | Official APIs | Low |
| P4 (optional) | Indian job boards | Naukri, Instahyre | Public pages only, low volume | Medium–High |
| Excluded | LinkedIn | — | Not scraped (ToS/legal/anti-bot risk) | High |

---

## 3. Functional Requirements

### FR1 — Source adapters
- FR1.1 One adapter per source/ATS behind a common interface (`fetch_jobs(company) -> list[RawJob]`).
- FR1.2 Adding a new source must not require changes to the core pipeline.
- FR1.3 Maintain a configurable registry of target companies/boards.

### FR2 — Collection
- FR2.1 Handle pagination, infinite scroll and cursor-based APIs.
- FR2.2 Use the cheapest method available per source: API > embedded JSON > static HTML > headless browser.
- FR2.3 Store the raw response (HTML/JSON) before parsing, so data can be re-parsed without re-scraping.

### FR3 — Normalization
Map every job to a unified schema:

| Field | Notes |
|---|---|
| `id` | Internal unique ID |
| `source`, `source_job_id`, `url` | Provenance |
| `title`, `company`, `description` | Core |
| `location_raw`, `city`, `country`, `remote_type` | Normalized (e.g. Bengaluru/Bangalore/BLR -> Bengaluru) |
| `employment_type` | Full-time, contract, intern |
| `experience_min`, `experience_max` | Parsed from text like "3-5 yrs" |
| `salary_min`, `salary_max`, `currency`, `salary_period` | When available |
| `skills[]`, `seniority` | Extracted (LLM / rules) |
| `posted_at`, `first_seen_at`, `last_seen_at`, `is_active` | Freshness tracking |

### FR4 — Deduplication
- FR4.1 Detect the same job across multiple sources (fuzzy match on title + company + location).
- FR4.2 Re-running a scrape must be idempotent (no duplicate rows).

### FR5 — Enrichment
- FR5.1 Extract skills, tech stack, seniority and salary from free-text descriptions.
- FR5.2 Tag jobs by category (backend, data, design, etc.).

### FR6 — Lifecycle tracking
- FR6.1 Detect new, updated and removed (expired) postings.
- FR6.2 Keep history to compute hiring velocity per company.

### FR7 — Access layer
- FR7.1 REST API: search/filter by keyword, company, location, skill, experience, date.
- FR7.2 Dashboard: job list, filters, and trend charts (e.g. top skills, jobs per city over time).
- FR7.3 Alerts: notify when a watched company or keyword gets a new posting.

### FR8 — Scheduling
- FR8.1 Run scrapes on a configurable schedule per source (e.g. every 6/12/24 hours).
- FR8.2 Support manual one-off runs from the command line.

---

## 4. Non-Functional Requirements

| Area | Requirement |
|---|---|
| **Politeness / compliance** | Respect `robots.txt`; per-domain rate limits; identifiable User-Agent; no logged-in scraping; no personal data collection |
| **Reliability** | Retries with exponential backoff; one failing source must not stop others |
| **Observability** | Per-run logs and metrics (jobs found, new, updated, errors); alert if a source returns zero or drops sharply |
| **Maintainability** | Modular adapters; tests with saved fixtures so selector changes are caught quickly |
| **Performance** | Concurrent fetching with configurable limits; handle 10k+ jobs per run |
| **Data quality** | Schema validation; required-field checks; quality score per source |
| **Privacy / legal** | Comply with DPDP/GDPR principles; personal/portfolio use unless legal review is done |
| **Cost** | Free/low-cost stack; LLM usage batched and cached |

---

## 5. Proposed Tech Stack (to confirm)

| Layer | Choice |
|---|---|
| Language | Python 3.11+ |
| HTTP / parsing | httpx, BeautifulSoup / selectolax |
| Headless browser | Playwright (only where needed) |
| Storage | PostgreSQL (SQLite for early prototype) |
| Raw store | Local files, later S3-compatible storage |
| Enrichment | Claude API (batched) + rule-based parsers |
| API | FastAPI |
| Dashboard | React (or a simple Streamlit prototype first) |
| Scheduling | APScheduler / cron |
| Testing | pytest with saved HTML/JSON fixtures |

---

## 6. Success Criteria

- [ ] At least 3 ATS adapters working (Greenhouse, Lever, + one more)
- [ ] 100+ companies covered with 5,000+ active jobs collected
- [ ] Duplicate rate under 2% after dedup
- [ ] 90%+ of jobs have normalized location and a parsed experience range
- [ ] Re-running a scrape produces no duplicates and correctly marks removed jobs
- [ ] Search API returns filtered results in under 500 ms
- [ ] Dashboard shows at least one useful trend view (e.g. top skills by city)

---

## 7. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Site layout changes break scrapers | Silent data loss | Prefer APIs/JSON-LD; monitoring; fixture tests |
| Anti-bot blocking (Naukri etc.) | Source unusable | Keep it optional; low volume; fall back to APIs |
| ToS / legal exposure | Account bans, legal issues | ATS public APIs first; no logins; personal-use scope |
| Messy/inconsistent data | Poor search quality | Strong normalization + LLM extraction + validation |
| LLM cost at scale | Budget overrun | Batch, cache, use rules first and LLM only for gaps |

---

## 8. Open Questions (need your input)

1. **Purpose:** personal job hunting, market analytics, or a product?
2. **Geography focus:** India only, or global/remote too?
3. **Language/stack:** Python OK, or prefer TypeScript?
4. **Dashboard:** quick Streamlit prototype first, or a full React app?
5. **Include Naukri/Instahyre** (optional P4) now or later?
6. **Roles of interest:** any specific domains (e.g. engineering, data, ops) to prioritize?

---

## 9. Next Plans (roadmap)

- `plan02` — System architecture & data model
- `plan03` — Project setup & first adapter (Greenhouse)
- `plan04` — Normalization, dedup & storage
- `plan05` — Enrichment (skills/seniority/salary)
- `plan06` — API & dashboard
- `plan07` — Scheduling, monitoring & hardening
