package store

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type DailyStatsRow struct {
	Date                 time.Time
	LikesPerDay          uint64
	DailyActiveLikers    uint64
	DailyActivePosters   uint64
	PostsPerDay          uint64
	FollowsPerDay        uint64
	DailyActiveFollowers uint64
	BlocksPerDay         uint64
	DailyActiveBlockers  uint64
}

// GetDailyStats retrieves daily statistics summaries from the materialized view
// and merges with historical backfilled data
func (s *Store) GetDailyStats(ctx context.Context, limit int) (stats []DailyStatsRow, err error) {
	ctx, done := observe(ctx, "query", &err,
		attribute.Int("limit", limit))
	defer func() { done(attribute.Int("result.count", len(stats))) }()

	query := `
		SELECT
			date,
			sum(likes_per_day) AS likes_per_day,
			sum(daily_active_likers) AS daily_active_likers,
			sum(daily_active_posters) AS daily_active_posters,
			sum(posts_per_day) AS posts_per_day,
			sum(follows_per_day) AS follows_per_day,
			sum(daily_active_followers) AS daily_active_followers,
			sum(blocks_per_day) AS blocks_per_day,
			sum(daily_active_blockers) AS daily_active_blockers
		FROM (
			SELECT
				date,
				sumIf(count, metric_type = 'likes') AS likes_per_day,
				uniqMergeIf(unique_actors, metric_type = 'likes') AS daily_active_likers,
				uniqMergeIf(unique_actors, metric_type = 'posts') AS daily_active_posters,
				sumIf(count, metric_type = 'posts') AS posts_per_day,
				sumIf(count, metric_type = 'follows') AS follows_per_day,
				uniqMergeIf(unique_actors, metric_type = 'follows') AS daily_active_followers,
				sumIf(count, metric_type = 'blocks') AS blocks_per_day,
				uniqMergeIf(unique_actors, metric_type = 'blocks') AS daily_active_blockers
			FROM daily_stats
			GROUP BY date

			UNION ALL

			SELECT
				date,
				sumIf(count, metric_type = 'likes') AS likes_per_day,
				sumIf(unique_actors, metric_type = 'likes') AS daily_active_likers,
				sumIf(unique_actors, metric_type = 'posts') AS daily_active_posters,
				sumIf(count, metric_type = 'posts') AS posts_per_day,
				sumIf(count, metric_type = 'follows') AS follows_per_day,
				sumIf(unique_actors, metric_type = 'follows') AS daily_active_followers,
				sumIf(count, metric_type = 'blocks') AS blocks_per_day,
				sumIf(unique_actors, metric_type = 'blocks') AS daily_active_blockers
			FROM daily_stats_backfill
			GROUP BY date
		)
		GROUP BY date
		ORDER BY date DESC
		LIMIT ?
	`

	rows, err := s.DB.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row DailyStatsRow
		if err := rows.Scan(
			&row.Date,
			&row.LikesPerDay,
			&row.DailyActiveLikers,
			&row.DailyActivePosters,
			&row.PostsPerDay,
			&row.FollowsPerDay,
			&row.DailyActiveFollowers,
			&row.BlocksPerDay,
			&row.DailyActiveBlockers,
		); err != nil {
			return nil, fmt.Errorf("failed to scan daily stats row: %w", err)
		}
		stats = append(stats, row)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}
