-- +goose Up
-- P0 core tables (see plan/plan02-database-schema.md)

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE companies (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL,
  slug        TEXT NOT NULL UNIQUE,
  website     TEXT,
  hq_country  CHAR(2),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sources (
  id                   BIGSERIAL PRIMARY KEY,
  company_id           BIGINT REFERENCES companies(id),
  kind                 TEXT NOT NULL,
  board_token          TEXT NOT NULL,
  base_url             TEXT NOT NULL,
  scrape_every         INTERVAL NOT NULL DEFAULT '24 hours',
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
  status        TEXT NOT NULL CHECK (status IN ('running','ok','partial','failed')),
  jobs_found    INT NOT NULL DEFAULT 0,
  jobs_new      INT NOT NULL DEFAULT 0,
  jobs_updated  INT NOT NULL DEFAULT 0,
  jobs_closed   INT NOT NULL DEFAULT 0,
  error         TEXT
);
CREATE INDEX scrape_runs_source_idx ON scrape_runs (source_id, started_at DESC);

-- Canonical, deduplicated job: what the API and UI query.
CREATE TABLE jobs (
  id                BIGSERIAL PRIMARY KEY,
  company_id        BIGINT NOT NULL REFERENCES companies(id),
  title             TEXT NOT NULL,
  title_normalized  TEXT NOT NULL,
  description       TEXT,
  category          TEXT,
  seniority         TEXT,
  employment_type   TEXT,
  remote_type       TEXT,
  city              TEXT,
  region            TEXT,
  country           CHAR(2) NOT NULL DEFAULT 'IN',
  location_raw      TEXT,
  exp_min_years     SMALLINT,
  exp_max_years     SMALLINT,
  salary_min        NUMERIC(14,2),
  salary_max        NUMERIC(14,2),
  salary_currency   CHAR(3),
  salary_period     TEXT,
  fingerprint       TEXT NOT NULL,
  posted_at         TIMESTAMPTZ,
  first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  closed_at         TIMESTAMPTZ,
  search_vector     TSVECTOR GENERATED ALWAYS AS (
                      setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                      setweight(to_tsvector('english', coalesce(description, '')), 'B')
                    ) STORED,
  extra             JSONB NOT NULL DEFAULT '{}'
);
CREATE INDEX jobs_open_company_idx ON jobs (company_id) WHERE closed_at IS NULL;
CREATE INDEX jobs_country_city_idx ON jobs (country, city) WHERE closed_at IS NULL;
CREATE INDEX jobs_fingerprint_idx  ON jobs (fingerprint);
CREATE INDEX jobs_title_trgm_idx   ON jobs USING GIN (title_normalized gin_trgm_ops);
CREATE INDEX jobs_search_idx       ON jobs USING GIN (search_vector);

-- What one source says about one job (source-level truth).
CREATE TABLE job_postings (
  id              BIGSERIAL PRIMARY KEY,
  job_id          BIGINT REFERENCES jobs(id),
  source_id       BIGINT NOT NULL REFERENCES sources(id),
  external_id     TEXT NOT NULL,
  url             TEXT NOT NULL,
  title_raw       TEXT NOT NULL,
  location_raw    TEXT,
  description_raw TEXT,
  content_hash    TEXT NOT NULL,
  first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  removed_at      TIMESTAMPTZ,
  UNIQUE (source_id, external_id)
);
CREATE INDEX job_postings_job_idx    ON job_postings (job_id);
CREATE INDEX job_postings_active_idx ON job_postings (source_id) WHERE removed_at IS NULL;

-- Cold storage: latest original payload per posting, gzip-compressed.
CREATE TABLE posting_raw (
  posting_id    BIGINT PRIMARY KEY REFERENCES job_postings(id) ON DELETE CASCADE,
  content_hash  TEXT NOT NULL,
  encoding      TEXT NOT NULL DEFAULT 'gzip+json',
  body          BYTEA NOT NULL,
  fetched_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS posting_raw;
DROP TABLE IF EXISTS job_postings;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS scrape_runs;
DROP TABLE IF EXISTS sources;
DROP TABLE IF EXISTS companies;
