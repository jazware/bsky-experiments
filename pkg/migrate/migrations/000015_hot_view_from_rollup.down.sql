CREATE OR REPLACE VIEW recent_posts_with_score AS
SELECT
    p.uri AS subject_uri,
    p.did AS actor_did,
    p.rkey AS rkey,
    p.created_at AS subject_created_at,
    p.indexed_at AS inserted_at,
    p.langs AS langs,
    p.has_embedded_media AS has_embedded_media,
    toFloat64(
        (like_count - 1) /
        pow(dateDiff('hour', p.created_at, now()) + 2, 1.8)
    ) AS score,
    like_count
FROM posts AS p
LEFT JOIN (
    SELECT
        subject_uri,
        countIf(deleted = 0) AS like_count
    FROM likes
    WHERE created_at > now() - INTERVAL 24 HOUR
    GROUP BY subject_uri
) AS like_stats ON p.uri = like_stats.subject_uri
WHERE p.deleted = 0
    AND p.created_at > now() - INTERVAL 24 HOUR
    AND p.created_at < now() - INTERVAL 5 MINUTE
    AND p.parent_uri = ''
    AND p.root_uri = ''
    AND like_count >= 15
ORDER BY score DESC;
