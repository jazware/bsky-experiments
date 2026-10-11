-- vlsync slot of the record's author: top 16 bits of sha256(repo),
-- big-endian (vlsync-store slots::slot_of). vlDB slice exports filter on
-- it. A column rather than an expression index, so queries say `slot < N`
-- instead of repeating the hash expression verbatim, and granules that are
-- read don't rehash every repo.
--
-- set(256), not minmax: a granule holds a median of 16 (crawl_records) to
-- 80 (repo_records) distinct slots, so a minmax range covers most of the
-- slot space and only skips for slices that start at slot 0.
--
-- Metadata-only. Parts written before this compute slot on read and have
-- no idx_slot, so they skip nothing until backfilled by hand, per partition:
--   ALTER TABLE <t> MATERIALIZE COLUMN slot IN PARTITION <p>, MATERIALIZE INDEX idx_slot IN PARTITION <p>
-- Both are needed: MATERIALIZE COLUMN alone doesn't build the index (26.8).
-- Neither rewrites the other columns (they're hardlinked).
ALTER TABLE crawl_records ADD COLUMN IF NOT EXISTS slot UInt16 MATERIALIZED reinterpretAsUInt16(reverse(substring(SHA256(repo), 1, 2)));
ALTER TABLE crawl_records ADD INDEX IF NOT EXISTS idx_slot slot TYPE set(256) GRANULARITY 1;
ALTER TABLE repo_records ADD COLUMN IF NOT EXISTS slot UInt16 MATERIALIZED reinterpretAsUInt16(reverse(substring(SHA256(repo), 1, 2)));
ALTER TABLE repo_records ADD INDEX IF NOT EXISTS idx_slot slot TYPE set(256) GRANULARITY 1;
