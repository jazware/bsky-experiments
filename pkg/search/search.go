package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/jazware/bsky-experiments/pkg/usercount"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("search-service")

type SearchService struct {
	Logger        *slog.Logger
	Store         *store.Store
	UserCount     *usercount.UserCount
	RedisClient   *redis.Client
	StatsCacheTTL time.Duration

	statsCache    *StatsCache
	statsCacheMux sync.RWMutex
	shutdownChan  chan struct{}
	shutdownOnce  sync.Once
}

type StatsCache struct {
	Stats      SiteStats
	Expiration time.Time
	UpdatedAt  time.Time
}

type SiteStats struct {
	TotalUsers   int              `json:"total_users"`
	TotalPosts   uint64           `json:"total_posts"`
	TotalFollows uint64           `json:"total_follows"`
	TotalLikes   uint64           `json:"total_likes"`
	UpdatedAt    time.Time        `json:"updated_at"`
	DailyData    []DailyDatapoint `json:"daily_data"`
}

type DailyDatapoint struct {
	Date                 string `json:"date"`
	LikesPerDay          uint64 `json:"num_likes"`
	DailyActiveLikers    uint64 `json:"num_likers"`
	DailyActivePosters   uint64 `json:"num_posters"`
	PostsPerDay          uint64 `json:"num_posts"`
	FollowsPerDay        uint64 `json:"num_follows"`
	DailyActiveFollowers uint64 `json:"num_followers"`
	BlocksPerDay         uint64 `json:"num_blocks"`
	DailyActiveBlockers  uint64 `json:"num_blockers"`
}

func NewSearchService(
	ctx context.Context,
	logger *slog.Logger,
	store *store.Store,
	userCount *usercount.UserCount,
	redisClient *redis.Client,
	statsCacheTTL time.Duration,
) (*SearchService, error) {
	ss := &SearchService{
		Logger:        logger.With("component", "search_service"),
		Store:         store,
		UserCount:     userCount,
		RedisClient:   redisClient,
		StatsCacheTTL: statsCacheTTL,
		shutdownChan:  make(chan struct{}),
	}

	return ss, nil
}

// ErrStatsNotReady is returned until the first stats refresh has finished;
// the refresh runs in the background so the server can listen before it.
var ErrStatsNotReady = errors.New("stats not ready yet")

func (ss *SearchService) GetStats(ctx context.Context) (*SiteStats, error) {
	_, span := tracer.Start(ctx, "GetStats")
	defer span.End()

	ss.statsCacheMux.RLock()
	defer ss.statsCacheMux.RUnlock()

	if ss.statsCache == nil {
		return nil, ErrStatsNotReady
	}

	return &ss.statsCache.Stats, nil
}

// StartStatsRefreshWorker refreshes once right away, then every minute. A
// failed refresh keeps the last cache.
func (ss *SearchService) StartStatsRefreshWorker(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	logger := ss.Logger.With("worker", "stats_refresh")

	refresh := func() {
		refreshCtx, span := tracer.Start(ctx, "refreshStats")
		defer span.End()
		logger.Info("refreshing site stats")

		if err := ss.refreshStats(refreshCtx); err != nil {
			logger.Error("error refreshing site stats", "error", err)
		}
	}

	refresh()

	for {
		select {
		case <-ss.shutdownChan:
			logger.Info("shutting down stats refresh worker")
			return
		case <-ctx.Done():
			logger.Info("context cancelled, shutting down stats refresh worker")
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func (ss *SearchService) refreshStats(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "refreshStats")
	defer span.End()

	userCount, err := ss.UserCount.GetUserCount(ctx)
	if err != nil {
		ss.Logger.Error("error getting user count", "error", err)
		return fmt.Errorf("error getting user count: %w", err)
	}

	dailyStats, err := ss.Store.GetDailyStats(ctx, 10000)
	if err != nil {
		return fmt.Errorf("error getting daily stats: %w", err)
	}

	dailyDatapoints := []DailyDatapoint{}
	var totalPosts, totalLikes, totalFollows uint64

	minDate := time.Date(2023, 3, 1, 0, 0, 0, 0, time.UTC)
	maxDate := time.Now().AddDate(0, 0, 1)

	for _, datapoint := range dailyStats {
		if datapoint.Date.Before(minDate) || datapoint.Date.After(maxDate) {
			continue
		}

		dailyDatapoints = append(dailyDatapoints, DailyDatapoint{
			Date:                 datapoint.Date.UTC().Format("2006-01-02"),
			LikesPerDay:          datapoint.LikesPerDay,
			DailyActiveLikers:    datapoint.DailyActiveLikers,
			DailyActivePosters:   datapoint.DailyActivePosters,
			PostsPerDay:          datapoint.PostsPerDay,
			FollowsPerDay:        datapoint.FollowsPerDay,
			DailyActiveFollowers: datapoint.DailyActiveFollowers,
			BlocksPerDay:         datapoint.BlocksPerDay,
			DailyActiveBlockers:  datapoint.DailyActiveBlockers,
		})

		totalPosts += datapoint.PostsPerDay
		totalLikes += datapoint.LikesPerDay
		totalFollows += datapoint.FollowsPerDay
	}

	totalUsersGauge.Set(float64(userCount))
	totalPostsGauge.Set(float64(totalPosts))
	totalLikesGauge.Set(float64(totalLikes))
	totalFollowsGauge.Set(float64(totalFollows))

	newCache := &StatsCache{
		Stats: SiteStats{
			TotalUsers:   userCount,
			TotalPosts:   totalPosts,
			TotalFollows: totalFollows,
			TotalLikes:   totalLikes,
			DailyData:    dailyDatapoints,
			UpdatedAt:    time.Now(),
		},
		Expiration: time.Now().Add(ss.StatsCacheTTL),
		UpdatedAt:  time.Now(),
	}

	ss.statsCacheMux.Lock()
	ss.statsCache = newCache
	ss.statsCacheMux.Unlock()

	ss.Logger.Info("stats cache refreshed",
		"total_users", userCount,
		"total_posts", totalPosts,
		"total_likes", totalLikes,
		"total_follows", totalFollows,
		"daily_datapoints", len(dailyDatapoints),
	)

	return nil
}

func (ss *SearchService) Shutdown() error {
	ss.shutdownOnce.Do(func() {
		close(ss.shutdownChan)
	})
	return nil
}
