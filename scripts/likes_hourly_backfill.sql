-- One-off: fill likes_hourly and like_keys with what likes held before their
-- materialized views existed (migration 000014). Run once, after 000014 and
-- before 000015 (which points the hot feed at likes_hourly).
--
-- Heavy: it reads the current month's partition of likes, about one of the hot
-- feed's old per-minute queries. Run it with a few threads:
--   clickhouse-client --max_threads 8 --multiquery < scripts/likes_hourly_backfill.sql
--
-- t0 is when the views were created. A row inserted before it is not in them
-- and is counted here; one inserted after is counted by them and not here,
-- so rerunning doubles the hours (truncate likes_hourly first if it must).
-- The creation time has 1 s resolution, so the boundary can drop or repeat
-- up to a second of likes.
--
-- 25 hours, because the feeds read the hour containing now() - 24h. Likes
-- unliked before t0 are left out, whether the unlike has a subject or not:
-- matching on (actor, rkey) is how the unlike is tied to its like.

INSERT INTO likes_hourly
WITH (
    SELECT metadata_modification_time FROM system.tables
    WHERE database = currentDatabase() AND name = 'likes_hourly_mv'
) AS t0
SELECT
    toStartOfHour(created_at) AS hour,
    subject_uri,
    count() AS likes
FROM likes
WHERE deleted = 0
    AND subject_uri != ''
    AND indexed_at < t0
    AND created_at >= toStartOfHour(now() - INTERVAL 25 HOUR)
    AND created_at < now() + INTERVAL 1 HOUR
    AND (actor_did, rkey) NOT IN (
        SELECT actor_did, rkey FROM likes
        WHERE deleted = 1
            AND indexed_at < t0
            AND created_at >= toStartOfHour(now() - INTERVAL 25 HOUR)
    )
GROUP BY hour, subject_uri;

-- Seven days of like keys, so unlikes of the last week's likes resolve from
-- the first minute. Duplicates with the view's rows collapse on merge.
INSERT INTO like_keys (actor_did, rkey, subject_uri, subject_did, subject_collection, subject_rkey, created_at)
SELECT actor_did, rkey, subject_uri, subject_did, subject_collection, subject_rkey, created_at
FROM likes
WHERE deleted = 0
    AND subject_uri != ''
    AND created_at > now() - INTERVAL 7 DAY
    AND created_at < now() + INTERVAL 1 HOUR;
