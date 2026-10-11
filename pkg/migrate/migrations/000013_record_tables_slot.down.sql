ALTER TABLE crawl_records DROP INDEX IF EXISTS idx_slot;
ALTER TABLE crawl_records DROP COLUMN IF EXISTS slot;
ALTER TABLE repo_records DROP INDEX IF EXISTS idx_slot;
ALTER TABLE repo_records DROP COLUMN IF EXISTS slot;
