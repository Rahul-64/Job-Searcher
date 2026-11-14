# Plan 02 — Database Schema (Neon Postgres, prioritized)

**Status:** Draft v1
**Depends on:** plan01 (requirements)
**Decisions locked in:**
- Database: **Neon** (free serverless Postgres, 1 GB storage per user's plan, scales to zero, 10 branches/project)
- Schema: **full**, split into priority tiers P0 → P3 (build in order)
- Raw payloads: **stored in the DB** (compressed, pruned — see §4)
- Location: **India-first normalization, global-capable model** (see §5)
- Language/driver: Go + `pgx`, `sqlc` for typed queries, `goose` for migrations

---

## 1. Design principles
1. **Two levels of jobs:** `job_postings` = what one source said; `jobs` = canonical deduplicated opening.
2. **Idempotent ingestion:** `UNIQUE (source_id, external_id)` + upserts, so re-runs never duplicate.
3. **Lifecycle by timestamps, never hard-delete:** `first_seen_at`, `last_seen_at`, `closed_at`.
4. **Hot vs cold data:** large raw bodies live in a separate table so list/search queries stay fast and small.
5. **Always keep `*_raw` fields** next to normalized fields — normalization can be re-run when rules improve.
6. **Extensions:** `pg_trgm` (fuzzy dedup/search), `pgcrypto` optional, `vector` (pgvector) only in P3.

---

## 2. Priority tiers overview

| Tier | Goal | Tables |
|---|---|---|
| **P0 — Core (MVP)** | Scrape, store, dedup, search a job | `companies`, `sources`, `scrape_runs`, `job_postings`, `posting_raw`, `jobs` |
| **P1 — Quality** | Better filtering & insight | `locations`, `city_aliases`, `job_locations`, `skills`, `job_skills` |
| **P2 — Product** | History, alerts, analytics | `posting_snapshots`, `job_events`, `watchlists`, `notifications` |
| **P3 — Advanced** | Smart features | `job_embeddings` (pgvector), `user_profiles`, `company_daily_stats`, `users` |

---

## 3. P0 — Core tables

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE companies (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  slug        TEXT NOT NULL UNIQUE,               -- normalized, e.g. "razorpay"
  website     TEXT,
  hq_country  CHAR(2),                            -- ISO-3166 alpha-2
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One scrapeable endpoint (a company board on one platform, or an aggregator)
CREATE TABLE sources (
  id                   BIGSERIAL PRIMARY KEY,
  company_id           BIGINT REFERENCES companies(id),   -- NULL for aggregators
  kind                 TEXT NOT NULL,   -- greenhouse|lever|ashby|workable|career_page|aggregator|naukri|instahyre
  board_token          TEXT NOT NULL,   -- "razorpay" in boards-api.greenhouse.io/v1/boards/razorpay
  base_url             TEXT NOT NULL,
  scrape_every         INTERVAL NOT NULL DEFAULT '24 hours',   -- daily by default; override per source if needed
  enabled              BOOLEAN NOT NULL DEFAULT true,
  last_run_at          TIMESTAMPTZ,
  consecutive_failures INT NOT NULL DEFAULT 0,
  disabled_reason      TEXT,
  UNIQUE (kind, board_token)
);

CREATE TABLE scrape_runs (
  id            BIGSERIAL PRIMARY KEY,
  source_id     BIGINT NOT NULL REFERENCES sources(id),
  started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at   TIMESTAMPTZ,
  status        TEXT NOT NULL,                -- running|ok|partial|failed
  jobs_found    INT NOT NULL DEFAULT 0,
  jobs_new      INT NOT NULL DEFAULT 0,
  jobs_updated  INT NOT NULL DEFAULT 0,
  jobs_closed   INT NOT NULL DEFAULT 0,
  error         TEXT
);
CREATE INDEX scrape_runs_source_idx ON scrape_runs (source_id, started_at DESC);

-- What a single source says about a single job (source-level truth)
CREATE TABLE job_postings (
  id              BIGSERIAL PRIMARY KEY,
  job_id          BIGINT,                      -- FK added after jobs table; set by dedup step
  source_id       BIGINT NOT NULL REFERENCES sources(id),
  external_id     TEXT NOT NULL,               -- the source's own job id
  url             TEXT NOT NULL,
  title_raw       TEXT NOT NULL,
  location_raw    TEXT,
  description_raw TEXT,
  content_hash    TEXT NOT NULL,               -- detect real changes cheaply
  first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at      TIMESTAMPTZ,                 -- NULL = still listed at source
  UNIQUE (source_id, external_id)
);
CREATE INDEX job_postings_job_idx    ON job_postings (job_id);
CREATE INDEX job_postings_active_idx ON job_postings (source_id) WHERE removed_at IS NULL;

-- Cold storage: original payload, gzip-compressed, 1 row per posting (latest only)
CREATE TABLE posting_raw (
  posting_id    BIGINT PRIMARY KEY REFERENCES job_postings(id) ON DELETE CASCADE,
  content_hash  TEXT NOT NULL,
  encoding      TEXT NOT NULL DEFAULT 'gzip+json',
  body          BYTEA NOT NULL,
  fetched_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Canonical, deduplicated job — what the API and UI query
CREATE TABLE jobs (
  id                BIGSERIAL PRIMARY KEY,
  company_id        BIGINT NOT NULL REFERENCES companies(id),
  title             TEXT NOT NULL,
  title_normalized  TEXT NOT NULL,             -- "sr backend engg" -> "senior backend engineer"
  description       TEXT,
  category          TEXT,                      -- backend|frontend|data|design|ops|...
  seniority         TEXT,                      -- intern|junior|mid|senior|staff|lead
  employment_type   TEXT,                      -- full_time|contract|intern
  remote_type       TEXT,                      -- onsite|hybrid|remote
  -- location (denormalized primary; full list arrives in P1)
  city              TEXT,
  region            TEXT,
  country           CHAR(2) NOT NULL DEFAULT 'IN',
  location_raw      TEXT,
  exp_min_years     SMALLINT,
  exp_max_years     SMALLINT,
  salary_min        NUMERIC(14,2),
  salary_max        NUMERIC(14,2),
  salary_currency   CHAR(3),
  salary_period     TEXT,                      -- year|month|hour
  fingerprint       TEXT NOT NULL,             -- hash(company + title_normalized + city)
  posted_at         TIMESTAMPTZ,
  first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at         TIMESTAMPTZ,               -- NULL = open
  search_vector     TSVECTOR GENERATED ALWAYS AS (     -- auto-maintained; title weighted above description
                      setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                      setweight(to_tsvector('english', coalesce(description, '')), 'B')
                    ) STORED,
  extra             JSONB NOT NULL DEFAULT '{}'
);
ALTER TABLE job_postings ADD CONSTRAINT job_postings_job_fk FOREIGN KEY (job_id) REFERENCES jobs(id);
-- NOTE: in the real migration (migrations/0001_p0_core.sql) `jobs` is created before
-- `job_postings`, so the FK is declared inline and this ALTER is not needed.

CREATE INDEX jobs_open_company_idx ON jobs (company_id) WHERE closed_at IS NULL;
CREATE INDEX jobs_country_city_idx ON jobs (country, city) WHERE closed_at IS NULL;
CREATE INDEX jobs_fingerprint_idx  ON jobs (fingerprint);
CREATE INDEX jobs_title_trgm_idx   ON jobs USING GIN (title_normalized gin_trgm_ops);
CREATE INDEX jobs_search_idx       ON jobs USING GIN (search_vector);
```

---

## 4. Raw payloads in the DB — keeping Neon's free tier alive

Storing raw data in Postgres on a 1 GB budget is comfortable, but still needs discipline so it doesn't creep up:

| Rule | Detail |
|---|---|
| **Compress** | gzip the JSON/HTML before insert (typically 5–10× smaller) |
| **Latest only** | `posting_raw` has `posting_id` as PK → one row per posting, overwritten when `content_hash` changes |
| **Skip unchanged** | If `content_hash` is identical, do not write — only bump `last_seen_at` |
| **Prune closed** | Nightly: delete `posting_raw` for postings removed > 30 days (keep the hash + parsed data) |
| **Store per job, not per page** | ATS APIs return one big list; split into per-job payloads |
| **Don't store descriptions twice** | Keep `description_raw` in `job_postings`; `jobs.description` is the cleaned version — consider dropping `description_raw` once parsing is stable |

**Rough size budget (estimates, to be measured):**

| Item | Count | Size each | Total |
|---|---|---|---|
| `job_postings` rows | 20,000 | ~3 KB | ~60 MB |
| `jobs` rows | 15,000 | ~4 KB | ~60 MB |
| `posting_raw` (gzip) | 20,000 | ~3 KB | ~60 MB |
| Indexes (GIN, trgm) | — | — | ~100 MB |
| `scrape_runs` + misc | — | — | ~20 MB |
| **Total** | | | **≈ 300 MB** (about 30% of the 1 GB limit; leaves room for P1/P2 tables and growth) |

A `/admin/db-size` check (query `pg_total_relation_size`) should be part of the nightly job, alerting at 80% of the 1 GB budget (~800 MB). **If we outgrow free tier, move `posting_raw` to Cloudflare R2 without touching the other tables.**

---

## 5. Location strategy — India-first, global-capable

- `country` is **ISO alpha-2 on every job**, default `'IN'` only as a fallback when the source is India-specific.
- API filters accept `country` (default `IN` in the UI, but any code is allowed → searching outside India works from day one).
- Normalization seed data covers **Indian cities + aliases** (Bengaluru/Bangalore/BLR, Gurugram/Gurgaon, Mumbai/Bombay, Delhi NCR, Hyderabad, Pune, Chennai, Kolkata, Noida, Ahmedabad, ...).
- Non-India locations are **not rejected**: they are parsed best-effort (country detected from text/`remote`), and always keep `location_raw`.
- `remote_type` is separate from location (e.g. `remote` + `country=IN` = "Remote, India").

---

## 6. P1 — Quality tables

```sql
CREATE TABLE locations (
  id        BIGSERIAL PRIMARY KEY,
  city      TEXT,
  region    TEXT,                               -- state/province
  country   CHAR(2) NOT NULL,
  lat       DOUBLE PRECISION,
  lng       DOUBLE PRECISION,
  UNIQUE (country, region, city)
);

-- "BLR", "Bangalore", "Bengaluru, KA" -> one canonical location
CREATE TABLE city_aliases (
  alias        TEXT PRIMARY KEY,                -- lower-cased
  location_id  BIGINT NOT NULL REFERENCES locations(id)
);

CREATE TABLE job_locations (
  job_id       BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  location_id  BIGINT NOT NULL REFERENCES locations(id),
  PRIMARY KEY (job_id, location_id)
);

CREATE TABLE skills (
  id        BIGSERIAL PRIMARY KEY,
  name      TEXT NOT NULL UNIQUE,               -- canonical "PostgreSQL"
  aliases   TEXT[] NOT NULL DEFAULT '{}',       -- {postgres,psql,pg}
  category  TEXT                                -- language|framework|cloud|database|tool
);
CREATE INDEX skills_aliases_idx ON skills USING GIN (aliases);

CREATE TABLE job_skills (
  job_id       BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  skill_id     BIGINT NOT NULL REFERENCES skills(id),
  is_required  BOOLEAN NOT NULL DEFAULT true,
  PRIMARY KEY (job_id, skill_id)
);
CREATE INDEX job_skills_skill_idx ON job_skills (skill_id);
```

---

## 7. P2 — Product tables

```sql
-- Change history (title/description/salary edits). Keep small: only on real change.
CREATE TABLE posting_snapshots (
  id            BIGSERIAL PRIMARY KEY,
  posting_id    BIGINT NOT NULL REFERENCES job_postings(id) ON DELETE CASCADE,
  content_hash  TEXT NOT NULL,
  changes       JSONB NOT NULL,                 -- diff only, not the full body
  captured_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Event log powering hiring-velocity analytics and alerts
CREATE TABLE job_events (
  id         BIGSERIAL PRIMARY KEY,
  job_id     BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,                     -- opened|updated|closed|reopened
  at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  meta       JSONB
);
CREATE INDEX job_events_job_idx ON job_events (job_id, at DESC);

CREATE TABLE watchlists (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  company_id  BIGINT REFERENCES companies(id),
  keywords    TEXT[] NOT NULL DEFAULT '{}',
  skills      TEXT[] NOT NULL DEFAULT '{}',
  country     CHAR(2),
  city        TEXT,
  channel     TEXT NOT NULL DEFAULT 'telegram', -- telegram|email
  enabled     BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE notifications (
  id            BIGSERIAL PRIMARY KEY,
  watchlist_id  BIGINT NOT NULL REFERENCES watchlists(id) ON DELETE CASCADE,
  job_id        BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  sent_at       TIMESTAMPTZ,
  status        TEXT NOT NULL DEFAULT 'pending',
  UNIQUE (watchlist_id, job_id)                 -- never alert twice for same job
);
```

---

## 8. P3 — Advanced (only after P0–P2 are stable)

- `job_embeddings (job_id PK, embedding vector(N), model TEXT)` — semantic search & resume matching (`CREATE EXTENSION vector`). Watch size: 1536 dims × 4 B ≈ 6 KB/job, so prefer a smaller model/dimension on the free tier.
- `users`, `user_profiles` (skills, experience, preferred cities) — personalization.
- `company_daily_stats (company_id, day, open_jobs, new_jobs, closed_jobs)` — materialized rollups for trend charts.

---

## 9. Key queries the schema must serve (acceptance)

| Use case | Served by |
|---|---|
| Upsert a posting idempotently | `UNIQUE (source_id, external_id)` + content_hash compare |
| Find sources due for scraping | `sources` where `enabled` and `last_run_at + scrape_every < now()` |
| Mark disappeared postings | `UPDATE job_postings SET removed_at = now() WHERE source_id=$1 AND last_seen_at < $run_start` |
| Search jobs (keyword + city + exp) | `search_vector` GIN + `(country, city)` partial index |
| Fuzzy dedup candidate lookup | `fingerprint` exact, then `title_normalized % $1` (trgm) within same company |
| Top skills by city (trend) | `job_skills` ⨝ `jobs` GROUP BY |
| Hiring velocity per company | `job_events` grouped by week |

---

## 10. Migration & tooling plan
- Migrations via **goose**, one file per tier: `0001_p0_core.sql`, `0002_p1_quality.sql`, `0003_p2_product.sql`, `0004_p3_advanced.sql`.
- Queries in `internal/store/queries/*.sql`, compiled by **sqlc** into typed Go.
- Seed files: `seeds/skills.sql`, `seeds/india_locations.sql`, `seeds/sources.sql` (initial company boards).
- Neon branching: use a `dev` branch for experiments, `main` for the real data.


---

## 11. Changes made in phase 3 (as built)
- **Fingerprint includes a description hash**: `hash(company, normalized title, primary place, description)`.
  Measured on real data, 125 groups shared company + title + location, but only 67 of their 307 jobs had
  identical descriptions; the rest were different teams (e.g. nine "Staff Software Engineer, Bengaluru"
  openings with nine different descriptions). Matching on title + place alone would have merged distinct roles.
- **`jobs.norm_version`** records which rules produced a job's derived fields. `scraper renormalize`
  re-derives jobs below the current version from `posting_raw`, with no scraping.
- **`locations` uses `UNIQUE NULLS NOT DISTINCT (country, region, city)`** (Postgres 15+; Neon runs 18), so a
  country-only place such as ("IN", NULL, NULL) exists once. `city_aliases` and `job_locations` are as planned.
- **Postings attach to an existing job** with the same company and fingerprint instead of creating a new one.
  Only the oldest posting of a job may rewrite the job's fields.
