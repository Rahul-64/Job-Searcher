-- +goose Up
-- Country is unknown until the Phase 3 normalizer runs. Defaulting to 'IN' would
-- mislabel non-India jobs, so make it nullable and drop the default.
ALTER TABLE jobs ALTER COLUMN country DROP DEFAULT;
ALTER TABLE jobs ALTER COLUMN country DROP NOT NULL;

-- +goose Down
UPDATE jobs SET country = 'IN' WHERE country IS NULL;
ALTER TABLE jobs ALTER COLUMN country SET DEFAULT 'IN';
ALTER TABLE jobs ALTER COLUMN country SET NOT NULL;
