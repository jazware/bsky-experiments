package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jazware/bsky-experiments/pkg/crawler"
	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/urfave/cli/v2"
)

func crawlCommand() *cli.Command {
	flags := append(commonFlags(),
		&cli.StringFlag{
			Name:    "output-dir",
			Usage:   "Directory to write .rca segment files",
			Value:   "./data/rca",
			EnvVars: []string{"OUTPUT_DIR"},
		},
		&cli.IntFlag{
			Name:    "workers",
			Usage:   "Number of concurrent crawl workers",
			Value:   50,
			EnvVars: []string{"WORKERS"},
		},
		&cli.Float64Flag{
			Name:    "default-pds-rps",
			Usage:   "Default requests per second per PDS",
			Value:   5,
			EnvVars: []string{"DEFAULT_PDS_RPS"},
		},
		&cli.StringFlag{
			Name:    "segment-size",
			Usage:   "Target segment file size (e.g. 2GB, 500MB)",
			Value:   "2GB",
			EnvVars: []string{"SEGMENT_SIZE"},
		},
		&cli.IntFlag{
			Name:    "zstd-level",
			Usage:   "Zstd compression level (1-19)",
			Value:   3,
			EnvVars: []string{"ZSTD_LEVEL"},
		},
		&cli.IntFlag{
			Name:    "page-size",
			Usage:   "Number of DIDs to fetch per page from ClickHouse",
			Value:   50000,
			EnvVars: []string{"PAGE_SIZE"},
		},
		&cli.StringSliceFlag{
			Name:    "skip-pds",
			Usage:   "PDS URLs to skip (can be repeated)",
			EnvVars: []string{"SKIP_PDS"},
		},
		&cli.BoolFlag{
			Name:  "resume",
			Usage: "Resume an interrupted crawl",
		},
		&cli.BoolFlag{
			Name:    "direct",
			Usage:   "Write directly to ClickHouse instead of .rca archive files",
			EnvVars: []string{"DIRECT"},
		},
	)

	return &cli.Command{
		Name:  "crawl",
		Usage: "Crawl all ATProto repos from the network",
		Flags: flags,
		Action: func(cctx *cli.Context) error {
			return runCrawl(cctx)
		},
	}
}

func runCrawl(cctx *cli.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	logger := telemetry.StartLogger(cctx)
	telemetry.StartMetrics(cctx)

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		shutdown, err := telemetry.StartTracing(cctx)
		if err != nil {
			return fmt.Errorf("starting tracing: %w", err)
		}
		defer shutdown(ctx)
	}

	chConn, err := setupClickHouse(cctx)
	if err != nil {
		return fmt.Errorf("clickhouse setup: %w", err)
	}

	direct := cctx.Bool("direct")

	segmentSize, err := parseSize(cctx.String("segment-size"))
	if err != nil {
		return fmt.Errorf("invalid segment-size: %w", err)
	}

	cfg := crawler.Config{
		OutputDir:      cctx.String("output-dir"),
		Workers:        cctx.Int("workers"),
		DefaultPDSRPS:  cctx.Float64("default-pds-rps"),
		SegmentSize:    segmentSize,
		ZstdLevel:      cctx.Int("zstd-level"),
		PageSize:       cctx.Int("page-size"),
		SkipPDS:        cctx.StringSlice("skip-pds"),
		ClickHouseConn: chConn,
		Logger:         logger,
		Direct:         direct,
	}

	// In direct mode, set up a separate bulk-optimized ClickHouse connection for writes.
	if direct {
		bulkConn, err := crawler.SetupBulkClickHouse(
			cctx.String("clickhouse-address"),
			cctx.String("clickhouse-username"),
			cctx.String("clickhouse-password"),
		)
		if err != nil {
			return fmt.Errorf("bulk clickhouse setup: %w", err)
		}
		cfg.DirectCHConn = bulkConn

		if err := crawler.PrepareTableForBulkLoad(ctx, bulkConn, true, logger); err != nil {
			return fmt.Errorf("preparing table for bulk load: %w", err)
		}
		defer func() {
			logger.Info("finalizing table after bulk load...")
			bgCtx := context.Background()
			if err := crawler.FinalizeTableAfterBulkLoad(bgCtx, bulkConn, logger); err != nil {
				logger.Error("failed to finalize table", "error", err)
			}
		}()
	}

	c, err := crawler.NewCrawler(cfg)
	if err != nil {
		return fmt.Errorf("creating crawler: %w", err)
	}

	crawlDone := make(chan error, 1)
	go func() {
		crawlDone <- c.Run(ctx)
	}()

	select {
	case err := <-crawlDone:
		return err
	case <-signals:
		logger.Info("shutting down on signal...")
		cancel()
		go func() {
			<-time.After(30 * time.Second)
			log.Fatal("failed to shut down in time, forcing exit")
		}()
		c.Shutdown()
		return <-crawlDone
	}
}
