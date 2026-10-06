package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	jetstreamclient "github.com/bluesky-social/jetstream/pkg/client"
	"github.com/bluesky-social/jetstream/pkg/client/schedulers/parallel"
	"github.com/gorilla/websocket"
	"github.com/jazware/bsky-experiments/pkg/indexer"
	"github.com/jazware/bsky-experiments/pkg/indexer/store"
	"github.com/jazware/bsky-experiments/pkg/plc"
	"github.com/jazware/bsky-experiments/pkg/profilehydrator"
	"github.com/jazware/bsky-experiments/pkg/secretfile"
	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/jazware/bsky-experiments/version"
	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/urfave/cli/v2"
)

// Jetstream delivers hundreds of events a second, so half a minute without
// one means the stream is stuck, well before the 60 s liveness kill.
const healthMaxEventAge = 30 * time.Second

func main() {
	app := cli.App{
		Name:    "indexer",
		Usage:   "atproto firehose indexer for ClickHouse",
		Version: version.String(),
	}

	app.Flags = []cli.Flag{
		telemetry.CLIFlagDebug,
		telemetry.CLIFlagMetricsListenAddress,
		telemetry.CLIFlagServiceName,
		telemetry.CLIFlagTracingSampleRatio,
		telemetry.CLIFlagTracingRootSampleRatios,
		&cli.StringFlag{
			Name:    "ws-url",
			Usage:   "full websocket path to the Jetstream subscription endpoint",
			Value:   "wss://jetstream.atproto.tools/subscribe",
			EnvVars: []string{"WS_URL"},
		},
		&cli.StringFlag{
			Name:    "redis-address",
			Usage:   "redis address for storing progress",
			Value:   "localhost:6379",
			EnvVars: []string{"REDIS_ADDRESS"},
		},
		&cli.StringFlag{
			Name:    "redis-prefix",
			Usage:   "redis prefix for storing progress",
			Value:   "indexer",
			EnvVars: []string{"REDIS_PREFIX"},
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
		&cli.BoolFlag{
			Name:    "enable-plc-exporter",
			Usage:   "enable the PLC directory exporter",
			Value:   true,
			EnvVars: []string{"ENABLE_PLC_EXPORTER"},
		},
		&cli.StringFlag{
			Name:    "plc-host",
			Usage:   "PLC directory host URL",
			Value:   "https://plc.directory",
			EnvVars: []string{"PLC_HOST"},
		},
		&cli.Float64Flag{
			Name:    "plc-rate-limit",
			Usage:   "PLC exporter rate limit (requests per second)",
			Value:   1.5,
			EnvVars: []string{"PLC_RATE_LIMIT"},
		},
		&cli.BoolFlag{
			Name:    "enable-profile-hydrator",
			Usage:   "enable the profile hydrator",
			Value:   false,
			EnvVars: []string{"ENABLE_PROFILE_HYDRATOR"},
		},
		&cli.StringFlag{
			Name:    "profile-api-host",
			Usage:   "Bluesky API host for profile fetching",
			Value:   "https://public.api.bsky.app",
			EnvVars: []string{"PROFILE_API_HOST"},
		},
		&cli.Float64Flag{
			Name:    "profile-rate-limit",
			Usage:   "Profile hydrator rate limit (requests per second)",
			Value:   5.0,
			EnvVars: []string{"PROFILE_RATE_LIMIT"},
		},
		&cli.IntFlag{
			Name:    "profile-batch-size",
			Usage:   "DIDs per API request (max 25)",
			Value:   25,
			EnvVars: []string{"PROFILE_BATCH_SIZE"},
		},
	}

	app.Action = Indexer

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

// Indexer is the main function for the indexer
func Indexer(cctx *cli.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create a channel that will be closed when we want to stop the application
	kill := make(chan struct{})

	// Trap SIGINT to trigger a shutdown
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	logger := telemetry.StartLogger(cctx)
	telemetry.StartMetrics(cctx)

	logger.Info("starting indexer",
		"version", version.Version,
		"commit", version.GitCommit)

	u, err := url.Parse(cctx.String("ws-url"))
	if err != nil {
		return fmt.Errorf("failed to parse ws-url: %+v", err)
	}

	// Registers a tracer Provider globally if the exporter endpoint is set
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		logger.Info("initializing tracer...")
		// A trace per firehose event (~350/s, all sub-millisecond) is mostly
		// noise: keep 1% of them. Other roots (cursor writes, batch inserts)
		// stay at tracing-sample-ratio.
		shutdown, err := telemetry.StartTracing(cctx, telemetry.WithRootSpanRatio("HandleStreamEvent", 0.01))
		if err != nil {
			return fmt.Errorf("failed to start tracing: %+v", err)
		}
		defer func() {
			if err := shutdown(ctx); err != nil {
				logger.Error("failed to shutdown tracer", "error", err)
			}
		}()
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     cctx.String("redis-address"),
		Password: "",
		DB:       0,
	})

	// Enable tracing instrumentation
	if err := redisotel.InstrumentTracing(redisClient); err != nil {
		return fmt.Errorf("failed to instrument redis with tracing: %+v", err)
	}

	// Enable metrics instrumentation
	if err := redisotel.InstrumentMetrics(redisClient); err != nil {
		return fmt.Errorf("failed to instrument redis with metrics: %+v", err)
	}

	// Test the connection to redis
	_, err = redisClient.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed to ping redis: %+v", err)
	}

	chPassword, err := secretfile.Flag(cctx, "clickhouse-password", "CLICKHOUSE_PASSWORD")
	if err != nil {
		return err
	}

	// Create a ClickHouse store
	clickhouseStore, err := store.NewStore(
		cctx.String("clickhouse-address"),
		cctx.String("clickhouse-username"),
		chPassword,
	)
	if err != nil {
		return fmt.Errorf("failed to create clickhouse store: %+v", err)
	}

	idx, err := indexer.NewIndexer(
		ctx,
		logger,
		redisClient,
		cctx.String("redis-prefix"),
		clickhouseStore,
		u.String(),
	)
	if err != nil {
		return fmt.Errorf("failed to create indexer: %+v", err)
	}

	// On the metrics listener (StartMetrics serves DefaultServeMux). The
	// cursor loaded from Redis carries the previous run's processed-at time,
	// so this stays 503 after a restart until a fresh event lands.
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		seq, processedAt := idx.Progress.Get()
		age := time.Since(processedAt)
		status := http.StatusOK
		if age > healthMaxEventAge {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"last_seq":         seq,
			"last_event_age_s": age.Seconds(),
		})
	})

	// Start a goroutine to manage the cursor, saving the current cursor every 5 seconds
	shutdownCursorManager := make(chan struct{})
	cursorManagerShutdown := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		logger := logger.With("component", "cursor_manager")

		for {
			select {
			case <-shutdownCursorManager:
				logger.Info("shutting down cursor manager")
				err := idx.WriteCursor(ctx)
				if err != nil {
					logger.Error("failed to write cursor during shutdown", "error", err)
				}
				logger.Info("cursor manager shut down successfully")
				close(cursorManagerShutdown)
				return
			case <-ticker.C:
				err := idx.WriteCursor(ctx)
				if err != nil {
					logger.Error("failed to write cursor", "error", err)
				}
			}
		}
	}()

	// Start a goroutine to manage the liveness checker
	shutdownLivenessChecker := make(chan struct{})
	livenessCheckerShutdown := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		lastSeq := int64(0)

		logger := logger.With("component", "liveness_checker")

		for {
			select {
			case <-shutdownLivenessChecker:
				logger.Info("shutting down liveness checker")
				close(livenessCheckerShutdown)
				return
			case <-ticker.C:
				seq, _ := idx.Progress.Get()
				if seq == lastSeq {
					logger.Error("no new events in last 60 seconds, shutting down for docker to restart me", "last_seq", seq)
					close(kill)
				} else {
					logger.Info("last event sequence", "seq", seq)
					lastSeq = seq
				}
			}
		}
	}()

	// Start PLC exporter if enabled
	var plcExporter *plc.Exporter
	shutdownPLCExporter := make(chan struct{})
	plcExporterShutdown := make(chan struct{})

	if cctx.Bool("enable-plc-exporter") {
		var err error
		plcExporter, err = plc.NewExporter(plc.ExporterConfig{
			PLCHost:     cctx.String("plc-host"),
			RateLimit:   cctx.Float64("plc-rate-limit"),
			RedisClient: redisClient,
			RedisPrefix: cctx.String("redis-prefix"),
			DB:          clickhouseStore.DB,
			Logger:      logger,
		})
		if err != nil {
			return fmt.Errorf("failed to create PLC exporter: %+v", err)
		}

		go func() {
			go plcExporter.Run()
			<-shutdownPLCExporter
			plcExporter.Shutdown()
			close(plcExporterShutdown)
		}()

		logger.Info("PLC exporter started", "host", cctx.String("plc-host"))
	}

	// Start profile hydrator if enabled
	var profHydrator *profilehydrator.Hydrator
	shutdownProfileHydrator := make(chan struct{})
	profileHydratorShutdown := make(chan struct{})

	if cctx.Bool("enable-profile-hydrator") {
		var err error
		profHydrator, err = profilehydrator.NewHydrator(profilehydrator.HydratorConfig{
			APIHost:     cctx.String("profile-api-host"),
			RateLimit:   cctx.Float64("profile-rate-limit"),
			BatchSize:   cctx.Int("profile-batch-size"),
			RedisClient: redisClient,
			RedisPrefix: cctx.String("redis-prefix"),
			DB:          clickhouseStore.DB,
			Logger:      logger,
		})
		if err != nil {
			return fmt.Errorf("failed to create profile hydrator: %+v", err)
		}

		go func() {
			go profHydrator.Run()
			<-shutdownProfileHydrator
			profHydrator.Shutdown()
			close(profileHydratorShutdown)
		}()

		logger.Info("profile hydrator started", "api_host", cctx.String("profile-api-host"))
	}

	logger.Info("connecting to websocket...", "url", u.String())
	con, _, err := websocket.DefaultDialer.Dial(u.String(), http.Header{
		"User-Agent": []string{"jaz-indexer/" + version.GitCommit},
	})
	if err != nil {
		logger.Error("failed to connect to websocket", "error", err)
		return err
	}
	defer con.Close()

	scheduler := parallel.NewScheduler(200, "jetstream-indexer", slog.Default(), idx.OnEvent)

	conf := jetstreamclient.DefaultClientConfig()
	conf.WebsocketURL = u.String()
	conf.Compress = true
	jetstreamClient, err := jetstreamclient.NewClient(conf, slog.Default(), scheduler)
	if err != nil {
		return fmt.Errorf("failed to create jetstream client: %+v", err)
	}

	shutdownRepoStream := make(chan struct{})
	repoStreamShutdown := make(chan struct{})
	go func() {
		var cursor *int64
		if idx.Progress.LastSeq > 0 {
			cursor = &idx.Progress.LastSeq
		}

		ctx := context.Background()
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			err = jetstreamClient.ConnectAndRead(ctx, cursor)
			if !errors.Is(err, context.Canceled) {
				logger.Error("HandleRepoStream returned error", "error", err)
				close(kill)
			} else {
				logger.Info("HandleRepoStream exited normally")
			}
			close(repoStreamShutdown)
		}()
		<-shutdownRepoStream
		cancel()
	}()

	select {
	case <-signals:
		logger.Info("shutting down on signal")
	case <-ctx.Done():
		logger.Info("shutting down on context done")
	case <-kill:
		logger.Info("shutting down on kill signal")
	}

	logger.Info("beginning shutdown...")

	// Force shutdown if we don't finish in 30 seconds
	go func() {
		<-time.After(30 * time.Second)
		log.Fatal("failed to shut down in time, forcing exit")
	}()

	close(shutdownRepoStream)
	close(shutdownLivenessChecker)
	close(shutdownCursorManager)

	// Shutdown PLC exporter if enabled
	if plcExporter != nil {
		close(shutdownPLCExporter)
		<-plcExporterShutdown
	}

	// Shutdown profile hydrator if enabled
	if profHydrator != nil {
		close(shutdownProfileHydrator)
		<-profileHydratorShutdown
	}

	<-repoStreamShutdown
	<-livenessCheckerShutdown
	<-cursorManagerShutdown

	err = idx.Shutdown()
	if err != nil {
		logger.Error("failed to shut down indexer", "error", err)
	}

	logger.Info("shut down successfully")

	return nil
}
