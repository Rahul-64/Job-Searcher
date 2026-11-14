-- +goose Up
-- Which kind of work a job is: software | ai_data | infra_security | qa. NULL means
-- anything else (sales, HR, finance, support, ...). Derived from the title and department
-- by the normalizer, so `scraper renormalize` recomputes it from the stored payloads.
ALTER TABLE jobs ADD COLUMN role_family TEXT;
CREATE INDEX jobs_role_family_idx ON jobs (role_family) WHERE closed_at IS NULL AND role_family IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS jobs_role_family_idx;
ALTER TABLE jobs DROP COLUMN role_family;
