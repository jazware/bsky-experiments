package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/jazware/bsky-experiments/pkg/auth"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/jazware/bsky-experiments/pkg/search"
	"github.com/jazware/bsky-experiments/pkg/search/appview"
	"github.com/jazware/bsky-experiments/pkg/search/endpoints"
	"github.com/jazware/bsky-experiments/pkg/search/postcard"
	"github.com/jazware/bsky-experiments/pkg/secretfile"
	"github.com/jazware/bsky-experiments/pkg/usercount"
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
		Name:    "search",
		Usage:   "bluesky search and stats service",
		Version: version.String(),
	}

	app.Flags = []cli.Flag{
		telemetry.CLIFlagDebug,
		telemetry.CLIFlagMetricsListenAddress,
		telemetry.CLIFlagServiceName,
		telemetry.CLIFlagTracingSampleRatio,
		telemetry.CLIFlagTracingRootSampleRatios,
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
			Name:    "magic-header-val",
			Usage:   "magic header value for protected endpoints",
			Value:   "",
			EnvVars: []string{"MAGIC_HEADER_VAL"},
		},
		&cli.DurationFlag{
			Name:    "stats-cache-ttl",
			Usage:   "duration to cache stats before refresh",
			Value:   30 * time.Second,
			EnvVars: []string{"STATS_CACHE_TTL"},
		},
		&cli.StringFlag{
			Name:    "bsky-pds-host",
			Usage:   "PDS entryway for authenticated AppView requests",
			Value:   "https://bsky.social",
			EnvVars: []string{"BSKY_PDS_HOST"},
		},
		&cli.StringFlag{
			Name:    "bsky-identifier",
			Usage:   "bluesky handle or DID for authenticated embed hydration (unauthenticated if empty)",
			Value:   "",
			EnvVars: []string{"BSKY_IDENTIFIER"},
		},
		&cli.StringFlag{
			Name:    "bsky-app-password",
			Usage:   "bluesky app password for authenticated embed hydration",
			Value:   "",
			EnvVars: []string{"BSKY_APP_PASSWORD"},
		},
		&cli.StringFlag{
			Name:    "public-url",
			Usage:   "externally-visible base URL of this service (e.g. https://bsky.jazco.dev); derived from requests if empty",
			Value:   "",
			EnvVars: []string{"PUBLIC_URL"},
		},
		&cli.DurationFlag{
			Name:    "embed-cache-ttl",
			Usage:   "duration to cache hydrated threads/profiles for embeds",
			Value:   5 * time.Minute,
			EnvVars: []string{"EMBED_CACHE_TTL"},
		},
		&cli.IntFlag{
			Name:    "card-render-concurrency",
			Usage:   "max concurrent headless-browser card renders",
			Value:   2,
			EnvVars: []string{"CARD_RENDER_CONCURRENCY"},
		},
		&cli.StringFlag{
			Name:    "chromium-path",
			Usage:   "path to the chromium/chrome binary for card rendering (searches PATH if empty)",
			Value:   "",
			EnvVars: []string{"CHROMIUM_PATH"},
		},
		&cli.StringSliceFlag{
			Name:    "cors-allowed-hosts",
			Usage:   "hostnames whose origins may call the API from a browser",
			Value:   cli.NewStringSlice("localhost"),
			EnvVars: []string{"CORS_ALLOWED_HOSTS"},
		},
	}

	app.Action = Search

	err := app.Run(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err.Error())
		os.Exit(1)
	}
}

func Search(cctx *cli.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handlers
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	// Initialize logger
	logger := telemetry.StartLogger(cctx)
	logger.Info("starting search service",
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

	secrets := map[string]string{}
	for flag, env := range map[string]string{
		"clickhouse-password": "CLICKHOUSE_PASSWORD",
		"magic-header-val":    "MAGIC_HEADER_VAL",
		"bsky-identifier":     "BSKY_IDENTIFIER",
		"bsky-app-password":   "BSKY_APP_PASSWORD",
	} {
		v, err := secretfile.Flag(cctx, flag, env)
		if err != nil {
			return err
		}
		secrets[flag] = v
	}

	// Connect to ClickHouse
	logger.Info("connecting to clickhouse", "address", cctx.String("clickhouse-address"))
	chStore, err := store.NewStore(
		cctx.String("clickhouse-address"),
		cctx.String("clickhouse-username"),
		secrets["clickhouse-password"],
	)
	if err != nil {
		return fmt.Errorf("failed to create ClickHouse store: %w", err)
	}
	defer chStore.Close()
	logger.Info("connected to clickhouse")

	// Connect to Redis
	logger.Info("connecting to redis", "address", cctx.String("redis-address"))
	redisClient := redis.NewClient(&redis.Options{
		Addr: cctx.String("redis-address"),
	})

	// Enable tracing instrumentation for Redis
	if err := redisotel.InstrumentTracing(redisClient); err != nil {
		return fmt.Errorf("failed to instrument redis with tracing: %w", err)
	}

	// Enable metrics instrumentation for Redis
	if err := redisotel.InstrumentMetrics(redisClient); err != nil {
		return fmt.Errorf("failed to instrument redis with metrics: %w", err)
	}

	// Test the connection to redis
	_, err = redisClient.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed to connect to redis: %w", err)
	}
	logger.Info("connected to redis")

	userCount := usercount.NewUserCount(ctx, redisClient)

	// Create search service
	searchService, err := search.NewSearchService(
		ctx,
		logger,
		chStore,
		userCount,
		redisClient,
		cctx.Duration("stats-cache-ttl"),
	)
	if err != nil {
		return fmt.Errorf("failed to create search service: %w", err)
	}

	// Create the AppView client for embed hydration
	appviewClient := appview.NewClient(
		logger,
		cctx.String("bsky-pds-host"),
		secrets["bsky-identifier"],
		secrets["bsky-app-password"],
		redisClient,
		cctx.Duration("embed-cache-ttl"),
	)
	if appviewClient.Authenticated() {
		logger.Info("appview client configured with credentials", "identifier", secrets["bsky-identifier"])
	} else {
		logger.Warn("no bluesky credentials configured; embeds will use the public appview and miss private-visibility profiles")
	}

	// Start the headless browser for post card rendering; embeds degrade to
	// static images if no browser is available (e.g. local dev)
	var renderer *postcard.Renderer
	renderer, err = postcard.NewRenderer(ctx, logger, cctx.Int("card-render-concurrency"), cctx.String("chromium-path"))
	if err != nil {
		logger.Warn("post card renderer unavailable, embeds will fall back to static images", "error", err)
		renderer = nil
	} else {
		defer renderer.Close()
	}

	// Create endpoints
	api, err := endpoints.NewAPI(
		logger,
		searchService,
		chStore,
		secrets["magic-header-val"],
		appviewClient,
		renderer,
		redisClient,
		cctx.String("public-url"),
	)
	if err != nil {
		return fmt.Errorf("failed to create API: %w", err)
	}

	// Start background workers
	logger.Info("starting stats refresh worker")
	go searchService.StartStatsRefreshWorker(ctx)

	logger.Info("starting cleanup daemon")
	go api.RunCleanupDaemon(ctx)

	// Setup Echo router
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// Recovery middleware
	e.Use(middleware.Recover())

	// Structured logging middleware
	e.Use(slogecho.NewWithFilters(
		logger,
		slogecho.IgnorePath("/metrics"),
		slogecho.IgnorePath("/healthz"),
	))

	// Serve static files from the public folder
	e.Static("/public", "./public")

	// OTEL Middleware
	e.Use(otelecho.Middleware(
		"search-api",
		otelecho.WithSkipper(func(c echo.Context) bool {
			return c.Request().URL.Path == "/metrics"
		}),
	))

	// CORS middleware
	corsHosts := cctx.StringSlice("cors-allowed-hosts")
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"https://bsky.jazco.dev"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{echo.HeaderOrigin, echo.HeaderContentLength, echo.HeaderContentType},
		AllowOriginFunc: func(origin string) (bool, error) {
			u, err := url.Parse(origin)
			if err != nil {
				return false, nil
			}
			return slices.Contains(corsHosts, u.Hostname()), nil
		},
	}))

	// Prometheus metrics endpoint
	e.GET("/metrics", echo.WrapHandler(promhttp.Handler()))

	// ClickHouse and Redis answered before the router existed, so serving at
	// all is what yeet's gate needs to know.
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })

	// Register routes
	e.GET("/stats", api.GetStats)
	e.GET("/redir", api.RedirectAtURI)
	e.GET("/repo/:did", api.GetRepoAsJSON)
	e.GET("/list/members", api.GetListMembers)
	e.GET("/repo/cleanup", api.GetCleanupStatus)
	e.POST("/repo/cleanup", api.CleanupOldRecords)
	e.DELETE("/repo/cleanup", api.CancelCleanupJob)
	e.GET("/repo/cleanup/stats", api.GetCleanupStats)

	// API-key auth (same api_keys table the feedgen admin dashboard uses)
	auther, err := auth.NewAuth(
		100,
		time.Hour,
		10,
		"did:web:bsky-search.jazco.io",
		auth.NewStoreProvider(chStore),
	)
	if err != nil {
		return fmt.Errorf("failed to create Auth: %w", err)
	}

	// Usercount PDS scrape list: inspect, and add official bsky hosts at runtime
	e.GET("/pds", api.GetPDSList)
	e.POST("/pds", api.AddPDSToList, auther.AuthenticateRequestViaAPIKey)

	// Link-proxy embeds: bsky.app-shaped URLs serve rich previews to
	// crawlers and redirect humans to bsky.app
	e.GET("/profile/:ident", api.GetProfileLinkProxy)
	e.GET("/profile/:ident/post/:rkey", api.GetPostLinkProxy)
	e.GET("/embed/post/:ident/:rkey/card.png", api.GetPostCardImage)
	e.GET("/oembed", api.GetOEmbed)

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

	// Cleanup search service
	if err := searchService.Shutdown(); err != nil {
		logger.Error("failed to shut down search service", "error", err)
		return err
	}

	logger.Info("shut down successfully")
	return nil
}
