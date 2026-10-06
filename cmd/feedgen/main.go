package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jazware/bsky-experiments/pkg/auth"
	feedgenerator "github.com/jazware/bsky-experiments/pkg/feed-generator"
	"github.com/jazware/bsky-experiments/pkg/feed-generator/endpoints"
	"github.com/jazware/bsky-experiments/pkg/feeds/authorlabel"
	"github.com/jazware/bsky-experiments/pkg/feeds/hot"
	"github.com/jazware/bsky-experiments/pkg/feeds/static"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/jazware/bsky-experiments/pkg/secretfile"
	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/jazware/bsky-experiments/version"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	slogecho "github.com/samber/slog-echo"
	"github.com/urfave/cli/v2"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
)

func main() {
	app := cli.App{
		Name:    "feed-generator",
		Usage:   "bluesky feed generator",
		Version: version.String(),
	}

	app.Flags = []cli.Flag{
		telemetry.CLIFlagDebug,
		telemetry.CLIFlagMetricsListenAddress,
		telemetry.CLIFlagServiceName,
		telemetry.CLIFlagTracingSampleRatio,
		&cli.StringFlag{
			Name:    "listen-address",
			Usage:   "listen address for HTTP server",
			Value:   "0.0.0.0:8080",
			EnvVars: []string{"LISTEN_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "redis-address",
			Usage:   "redis address for caching",
			Value:   "localhost:6379",
			EnvVars: []string{"REDIS_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "clickhouse-address",
			Usage:   "clickhouse address for storing records",
			Value:   "localhost:9000",
			EnvVars: []string{"CLICKHOUSE_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "clickhouse-username",
			Usage:   "clickhouse username",
			Value:   "default",
			EnvVars: []string{"CLICKHOUSE_USERNAME"},
		},
		&cli.StringFlag{
			Name:    "clickhouse-password",
			Usage:   "clickhouse password",
			Value:   "",
			EnvVars: []string{"CLICKHOUSE_PASSWORD"},
		},
		&cli.StringFlag{
			Name:     "service-endpoint",
			Usage:    "URL that the feed generator will be available at",
			Required: true,
			EnvVars:  []string{"SERVICE_ENDPOINT"},
		},
		&cli.StringFlag{
			Name:     "feed-actor-did",
			Usage:    "DID of the feed actor",
			Required: true,
			EnvVars:  []string{"FEED_ACTOR_DID"},
		},
	}

	app.Action = FeedGenerator

	err := app.Run(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
		os.Exit(1)
	}
}

func FeedGenerator(cctx *cli.Context) error {
	ctx, cancel := context.WithCancel(cctx.Context)
	defer cancel()

	// Setup signal handlers
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	// Initialize logger
	logger := telemetry.StartLogger(cctx)
	logger.Info("starting feed generator",
		"version", version.Version,
		"commit", version.GitCommit)

	// Initialize metrics
	telemetry.StartMetrics(cctx)

	// Initialize tracing if configured
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		logger.Info("initializing tracer")
		shutdown, err := telemetry.StartTracing(cctx)
		if err != nil {
			return fmt.Errorf("failed to start tracing: %w", err)
		}
		defer func() {
			if err := shutdown(ctx); err != nil {
				logger.Error("failed to shutdown tracer", "error", err)
			}
		}()
	}

	chPassword, err := secretfile.Flag(cctx, "clickhouse-password", "CLICKHOUSE_PASSWORD")
	if err != nil {
		return err
	}

	// Connect to ClickHouse
	logger.Info("connecting to clickhouse", "address", cctx.String("clickhouse-address"))
	chStore, err := store.NewStore(
		cctx.String("clickhouse-address"),
		cctx.String("clickhouse-username"),
		chPassword,
	)
	if err != nil {
		return fmt.Errorf("failed to create ClickHouse store: %w", err)
	}
	defer chStore.Close()
	logger.Info("connected to clickhouse")

	// Create batch inserter for analytics
	logger.Info("creating batch inserter")
	batchInserter := store.NewBatchInserter(chStore, logger)
	defer batchInserter.Shutdown()

	// Connect to Redis
	logger.Info("connecting to redis", "address", cctx.String("redis-address"))
	redisClient := redis.NewClient(&redis.Options{
		Addr: cctx.String("redis-address"),
	})

	// Enable tracing instrumentation for Redis
	if err := redisotel.InstrumentTracing(redisClient); err != nil {
		return fmt.Errorf("failed to instrument redis with tracing: %w", err)
	}

	// Test the connection to redis
	_, err = redisClient.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed to connect to redis: %w", err)
	}
	logger.Info("connected to redis")

	feedActorDID := cctx.String("feed-actor-did")

	// Set the acceptable DIDs for the feed generator to respond to
	serviceURL, err := url.Parse(cctx.String("service-endpoint"))
	if err != nil {
		return fmt.Errorf("error parsing service endpoint: %w", err)
	}

	serviceWebDID := "did:web:" + serviceURL.Hostname()
	logger.Info("service DIDs configured", "feed_actor_did", feedActorDID, "service_web_did", serviceWebDID)

	acceptableDIDs := []string{feedActorDID, serviceWebDID}

	feedGenerator, err := feedgenerator.NewFeedGenerator(ctx, feedActorDID, serviceWebDID, acceptableDIDs, cctx.String("service-endpoint"))
	if err != nil {
		return fmt.Errorf("failed to create FeedGenerator: %w", err)
	}

	endpoints, err := endpoints.NewEndpoints(feedGenerator, chStore, batchInserter)
	if err != nil {
		return fmt.Errorf("failed to create Endpoints: %w", err)
	}

	// Create feeds
	logger.Info("initializing hot feed")
	hotFeed, hotFeedAliases, err := hot.NewFeed(ctx, feedActorDID, chStore, redisClient)
	if err != nil {
		return fmt.Errorf("failed to create HotFeed: %w", err)
	}
	feedGenerator.AddFeed(hotFeedAliases, hotFeed)

	logger.Info("initializing authorlabel feed")
	authorLabelFeed, authorLabelFeedAliases, err := authorlabel.NewFeed(ctx, feedActorDID, chStore)
	if err != nil {
		return fmt.Errorf("failed to create AuthorLabelFeed: %w", err)
	}
	feedGenerator.AddFeed(authorLabelFeedAliases, authorLabelFeed)

	logger.Info("initializing static feed")
	staticFeed, staticFeedAliases, err := static.NewFeed(ctx, feedActorDID)
	if err != nil {
		return fmt.Errorf("failed to create StaticFeed: %w", err)
	}
	feedGenerator.AddFeed(staticFeedAliases, staticFeed)

	// Setup Echo router
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Recovery middleware
	e.Use(middleware.Recover())

	// Structured logging middleware with custom attributes
	e.Use(slogecho.NewWithConfig(
		logger,
		slogecho.Config{
			WithRequestBody:  false,
			WithResponseBody: false,
			Filters: []slogecho.Filter{
				slogecho.IgnorePath("/metrics"),
				slogecho.IgnorePath("/healthz"),
			},
			DefaultLevel:     slog.LevelInfo,
			ClientErrorLevel: slog.LevelWarn,
			ServerErrorLevel: slog.LevelError,
		},
	))

	// Serve static files from the public folder
	e.Static("/public", "./public")
	e.Static("/assets", "./public/assets")

	// OTEL Middleware
	e.Use(otelecho.Middleware(
		"feed-generator",
		otelecho.WithSkipper(func(c echo.Context) bool {
			return c.Request().URL.Path == "/metrics"
		}),
	))

	// CORS middleware
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"https://bsky.jazco.dev"},
		AllowMethods: []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentLength, echo.HeaderContentType, "X-API-Key"},
		AllowOriginFunc: func(origin string) (bool, error) {
			u, err := url.Parse(origin)
			if err != nil {
				return false, nil
			}
			return u.Hostname() == "localhost" || u.Hostname() == "10.0.6.32", nil
		},
	}))

	// Prometheus metrics endpoint
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	// ClickHouse and Redis answered before the router existed, so serving at
	// all is what yeet's gate needs to know.
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	// Init auth provider
	storeProvider := auth.NewStoreProvider(chStore)

	auther, err := auth.NewAuth(
		500_000,
		time.Hour*12,
		40,
		"did:web:feedsky.jazco.io",
		storeProvider,
	)
	if err != nil {
		return fmt.Errorf("failed to create Auth: %w", err)
	}

	// Public routes (no auth)
	e.GET("/.well-known/did.json", endpoints.GetWellKnownDID)

	// JWT Auth middleware for feed routes
	jwtGroup := e.Group("")
	jwtGroup.Use(auther.AuthenticateRequestViaJWT)
	jwtGroup.GET("/xrpc/app.bsky.feed.getFeedSkeleton", endpoints.GetFeedSkeleton)
	jwtGroup.GET("/xrpc/app.bsky.feed.describeFeedGenerator", endpoints.DescribeFeedGenerator)

	// Admin API routes (protected by API Key) - new paths for dashboard
	adminAPIGroup := e.Group("/api/admin")
	adminAPIGroup.Use(auther.AuthenticateRequestViaAPIKey)
	adminAPIGroup.GET("/feeds", endpoints.GetFeeds)
	adminAPIGroup.GET("/feed_members", endpoints.GetFeedMembersPaginated)
	adminAPIGroup.PUT("/feed_members", endpoints.AssignUserToFeed)
	adminAPIGroup.DELETE("/feed_members", endpoints.UnassignUserFromFeed)

	// Legacy API Key Auth routes (keep for backwards compatibility)
	legacyAPIKeyGroup := e.Group("")
	legacyAPIKeyGroup.Use(auther.AuthenticateRequestViaAPIKey)
	legacyAPIKeyGroup.PUT("/assign_user_to_feed", endpoints.AssignUserToFeed)
	legacyAPIKeyGroup.PUT("/unassign_user_from_feed", endpoints.UnassignUserFromFeed)
	legacyAPIKeyGroup.GET("/feed_members", endpoints.GetFeedMembers)

	// Dashboard routes (embedded SPA)
	dashboardHandler := feedgenerator.NewDashboardHandler()
	e.GET("/dashboard", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/dashboard/")
	})
	e.GET("/dashboard/*", dashboardHandler.ServeHTTP)

	// Start HTTP server in a goroutine
	serverErr := make(chan error, 1)

	go func() {
		logger.Info("starting HTTP server", "listen_address", cctx.String("listen-address"))
		if err := e.Start(cctx.String("listen-address")); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Wait for shutdown signal or error
	select {
	case <-signals:
		logger.Info("shutting down on signal")
	case <-ctx.Done():
		logger.Info("shutting down on context done")
	case err := <-serverErr:
		logger.Error("server error", "error", err)
		return err
	}

	logger.Info("beginning graceful shutdown")

	// Force shutdown after timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shut down HTTP server gracefully", "error", err)
		return err
	}

	logger.Info("shut down successfully")
	return nil
}
