-- Hourly like counts per post for the hot and top feeds, which used to
-- rescan the month's partition of likes every minute. A row per post and hour,
-- it stays small and a query reads only the hours it needs.
--
-- Rows are net: a like adds 1 at its created_at hour and an unlike (once the
-- indexer resolves its subject, see like_keys) subtracts 1 at that same hour.
-- Unresolved unlikes have subject_uri = '' and are skipped.
CREATE TABLE IF NOT EXISTS likes_hourly (
    hour DateTime,
    subject_uri String,
    likes Int64
) ENGINE = SummingMergeTree
PARTITION BY toDate(hour)
ORDER BY (hour, subject_uri)
TTL hour + INTERVAL 7 DAY;

-- The created_at window drops backfilled history (it would arrive already
-- expired) and likes dated in the future, which the old query counted forever.
CREATE MATERIALIZED VIEW IF NOT EXISTS likes_hourly_mv
TO likes_hourly
AS
SELECT
    toStartOfHour(created_at) AS hour,
    subject_uri,
    sumIf(1, deleted = 0) - sumIf(1, deleted = 1) AS likes
FROM likes
WHERE subject_uri != ''
    AND created_at > now() - INTERVAL 7 DAY
    AND created_at < now() + INTERVAL 1 HOUR
GROUP BY hour, subject_uri;

-- Where an unlike finds its like. An unlike arrives with only (actor_did,
-- rkey), and likes is ordered by subject, so the indexer looks the like up
-- here by primary key. Seven days covers the rollup's retention, and an unlike of
-- an older like stays unresolved. TTL is on insert time so that rows from a
-- backfill or with a bad created_at still age out.
CREATE TABLE IF NOT EXISTS like_keys (
    actor_did String,
    rkey String,
    subject_uri String,
    subject_did String,
    subject_collection String,
    subject_rkey String,
    created_at DateTime64(6),
    inserted_at DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree
ORDER BY (actor_did, rkey)
TTL inserted_at + INTERVAL 7 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS like_keys_mv
TO like_keys
AS
SELECT
    actor_did,
    rkey,
    subject_uri,
    subject_did,
    subject_collection,
    subject_rkey,
    created_at
FROM likes
WHERE deleted = 0;
