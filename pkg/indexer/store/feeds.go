package store

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type HotPost struct {
	SubjectURI       string
	ActorDID         string
	Rkey             string
	SubjectCreatedAt time.Time
	InsertedAt       time.Time
	Langs            []string
	HasEmbeddedMedia bool
	Score            float64
	LikeCount        uint64
}

func (s *Store) GetHotPosts(ctx context.Context, limit int, minScore float64) (posts []HotPost, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.Int("limit", limit),
		attribute.Float64("min.score", minScore))
	defer func() { done(attribute.Int("result.count", len(posts))) }()

	query := `
		SELECT
			subject_uri,
			actor_did,
			rkey,
			subject_created_at,
			inserted_at,
			langs,
			has_embedded_media,
			score,
			like_count
		FROM recent_posts_with_score
		WHERE score >= ?
		ORDER BY score DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, minScore, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query hot posts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var post HotPost
		if err := rows.Scan(
			&post.SubjectURI,
			&post.ActorDID,
			&post.Rkey,
			&post.SubjectCreatedAt,
			&post.InsertedAt,
			&post.Langs,
			&post.HasEmbeddedMedia,
			&post.Score,
			&post.LikeCount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		posts = append(posts, post)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return posts, nil
}

func (s *Store) GetTopPostsInWindow(ctx context.Context, hours int, limit int) (posts []HotPost, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.Int("hours", hours),
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(posts))) }()

	query := `
		SELECT
			p.uri AS subject_uri,
			p.did AS actor_did,
			p.rkey AS rkey,
			p.created_at AS subject_created_at,
			p.indexed_at AS inserted_at,
			p.langs AS langs,
			p.has_embedded_media AS has_embedded_media,
			like_count
		FROM posts AS p
		INNER JOIN (
			SELECT
				subject_uri,
				countIf(deleted = 0) AS like_count
			FROM likes
			WHERE created_at > now() - INTERVAL ? HOUR
			GROUP BY subject_uri
		) AS like_stats ON p.uri = like_stats.subject_uri
		WHERE p.deleted = 0
			AND p.created_at > now() - INTERVAL ? HOUR
			AND like_count > 10
		ORDER BY like_count DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, hours, hours, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query top posts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var post HotPost
		if err := rows.Scan(
			&post.SubjectURI,
			&post.ActorDID,
			&post.Rkey,
			&post.SubjectCreatedAt,
			&post.InsertedAt,
			&post.Langs,
			&post.HasEmbeddedMedia,
			&post.LikeCount,
		); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		posts = append(posts, post)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return posts, nil
}

type LabelPost struct {
	ActorDID  string
	Rkey      string
	CreatedAt time.Time
	IndexedAt time.Time
}

func (s *Store) ListMPLS(ctx context.Context, cursor time.Time, limit int) (posts []LabelPost, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("cursor", cursor.Format(time.RFC3339Nano)),
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(posts))) }()

	query := `
		SELECT m.actor_did, m.rkey, m.created_at, m.indexed_at
		FROM mpls AS m
		INNER JOIN (
			SELECT DISTINCT actor_did
			FROM actor_labels FINAL
			WHERE label = 'mpls' AND deleted = 0
		) AS al ON m.actor_did = al.actor_did
		WHERE m.indexed_at < ?
		ORDER BY m.indexed_at DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query mpls posts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var post LabelPost
		if err := rows.Scan(&post.ActorDID, &post.Rkey, &post.CreatedAt, &post.IndexedAt); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		posts = append(posts, post)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return posts, nil
}

func (s *Store) ListTQSP(ctx context.Context, cursor string, limit int) (posts []LabelPost, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.String("cursor", cursor),
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(posts))) }()

	query := `
		SELECT m.actor_did, m.rkey, m.created_at
		FROM tqsp AS m
		INNER JOIN (
			SELECT DISTINCT actor_did
			FROM actor_labels FINAL
			WHERE label = 'tqsp' AND deleted = 0
		) AS al ON m.actor_did = al.actor_did
		WHERE m.rkey < ?
		ORDER BY m.rkey DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query tqsp posts: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var post LabelPost
		if err := rows.Scan(&post.ActorDID, &post.Rkey, &post.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		posts = append(posts, post)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return posts, nil
}
