-- Switch record_json to ZSTD(3) to match crawl_records.
-- Metadata-only change: existing parts keep LZ4 until rewritten
-- (OPTIMIZE TABLE repo_records PARTITION <p> FINAL), new parts use ZSTD.
ALTER TABLE repo_records MODIFY COLUMN record_json String CODEC(ZSTD(3));
