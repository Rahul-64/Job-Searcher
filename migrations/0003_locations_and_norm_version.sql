-- +goose Up
-- Phase 3: normalized locations (P1 tier) and a normalizer version on jobs.

CREATE TABLE locations (
  id       BIGSERIAL PRIMARY KEY,
  city     TEXT,                         -- NULL for country- or region-level places
  region   TEXT,                         -- state / province, NULL when unknown
  country  CHAR(2) NOT NULL,             -- ISO 3166-1 alpha-2
  lat      DOUBLE PRECISION,
  lng      DOUBLE PRECISION,
  -- NULLs compare equal so ("IN", NULL, NULL) cannot be inserted twice.
  CONSTRAINT locations_place_key UNIQUE NULLS NOT DISTINCT (country, region, city)
);

-- "blr", "bangalore", "gurgaon" -> one canonical location. Lower-cased.
CREATE TABLE city_aliases (
  alias        TEXT PRIMARY KEY,
  location_id  BIGINT NOT NULL REFERENCES locations(id) ON DELETE CASCADE
);
CREATE INDEX city_aliases_location_idx ON city_aliases (location_id);

-- A job can be in several places ("Bengaluru; Gurugram; Mumbai").
CREATE TABLE job_locations (
  job_id       BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  location_id  BIGINT NOT NULL REFERENCES locations(id),
  PRIMARY KEY (job_id, location_id)
);
CREATE INDEX job_locations_location_idx ON job_locations (location_id);

-- Which version of the normalization rules produced a job's derived fields.
-- 0 = never normalized. `scraper renormalize` re-derives jobs below the current
-- version from stored raw payloads, with no re-scraping.
ALTER TABLE jobs ADD COLUMN norm_version SMALLINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE jobs DROP COLUMN norm_version;
DROP TABLE IF EXISTS job_locations;
DROP TABLE IF EXISTS city_aliases;
DROP TABLE IF EXISTS locations;
