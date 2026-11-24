-- One-off: subtract the unlikes that post_like_counts never saw.
--
-- An unlike used to land in likes with subject_uri = '', so post_like_counts_mv
-- subtracted it from the '' row (about -133.8M on 2026-11-24) and left the
-- post's count high. This ties each such delete to its like by (actor_did,
-- rkey), subtracts it from the like's subject, and adds it back to ''.
--
-- Run after the indexer that resolves unlikes is deployed, so only unlikes
-- of likes older than a week (not in like_keys) keep arriving blank. Those
-- stay blank rows; run this again with the next window to take them.
--
-- Heavy: two passes over likes' actor_did and rkey, about 200 GiB
-- compressed read for the whole table (the deleted rows are 2% of rows but
-- spread over every granule). grace_hash keeps the 134M-row build side off
-- the heap. Run it with a few threads, off-peak:
--   clickhouse-client --max_threads 8 --multiquery \
--     --param_from='1970-01-01 00:00:00' --param_to='<indexer deploy time, UTC>' \
--     < scripts/post_like_counts_unlike_repair.sql
-- Each run takes the blank deletes inserted in [from, to); set the next
-- run's from to this run's to, so no delete is subtracted twice. To read less
-- and cover less, add `AND toYYYYMM(created_at) >= 202609` to the likes side
-- (about a third of the rows; older likes' unlikes stay uncounted).
--
-- Check it before the insert at the end: the tmp table's sum is how far '' is
-- brought back up, and its distinct subjects should be well below the
-- delete count (many likes of one post).

CREATE TABLE IF NOT EXISTS post_like_counts_unlike_repair (
    subject_uri String,
    unliked Int64
) ENGINE = MergeTree ORDER BY subject_uri;

TRUNCATE TABLE post_like_counts_unlike_repair;

INSERT INTO post_like_counts_unlike_repair
SELECT subject_uri, count() AS unliked
FROM (
    SELECT l.subject_uri AS subject_uri, d.actor_did AS actor_did, d.rkey AS rkey
    FROM (
        SELECT DISTINCT actor_did, rkey
        FROM likes
        WHERE deleted = 1
            AND subject_uri = ''
            AND indexed_at >= {from:DateTime64(6)}
            AND indexed_at < {to:DateTime64(6)}
    ) AS d
    INNER JOIN (
        SELECT actor_did, rkey, subject_uri
        FROM likes
        WHERE deleted = 0 AND subject_uri != ''
    ) AS l ON d.actor_did = l.actor_did AND d.rkey = l.rkey
    GROUP BY subject_uri, actor_did, rkey
)
GROUP BY subject_uri
SETTINGS join_algorithm = 'grace_hash', grace_hash_join_initial_buckets = 64;

SELECT count() AS subjects, sum(unliked) AS unlikes_resolved FROM post_like_counts_unlike_repair;

INSERT INTO post_like_counts
SELECT subject_uri, -unliked AS like_count FROM post_like_counts_unlike_repair;

INSERT INTO post_like_counts
SELECT '', sum(unliked) AS like_count FROM post_like_counts_unlike_repair;

DROP TABLE post_like_counts_unlike_repair;
