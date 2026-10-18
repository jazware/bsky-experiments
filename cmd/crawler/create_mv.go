package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jazware/bsky-experiments/telemetry"
	"github.com/urfave/cli/v2"
)

func createMVCommand() *cli.Command {
	return &cli.Command{
		Name:  "create-mv",
		Usage: "Create crawl_records materialized views and backfill from existing data",
		Flags: commonFlags(),
		Action: func(cctx *cli.Context) error {
			return runCreateMV(cctx)
		},
	}
}

func runCreateMV(cctx *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signals
		cancel()
	}()

	logger := telemetry.StartLogger(cctx)

	chConn, err := setupClickHouse(cctx)
	if err != nil {
		return fmt.Errorf("clickhouse setup: %w", err)
	}

	ddl := []struct {
		name string
		stmt string
	}{
		{"crawl_records_global_stats table", `
			CREATE TABLE IF NOT EXISTS crawl_records_global_stats (
				key UInt8 DEFAULT 1,
				records AggregateFunction(count),
				repos AggregateFunction(uniqExact, String),
				collections AggregateFunction(uniqExact, String),
				min_crawled_at SimpleAggregateFunction(min, DateTime64(6)),
				max_crawled_at SimpleAggregateFunction(max, DateTime64(6)),
				max_indexed_at SimpleAggregateFunction(max, DateTime64(6))
			) ENGINE = AggregatingMergeTree()
			ORDER BY key`},
		{"crawl_records_global_stats_mv", `
			CREATE MATERIALIZED VIEW IF NOT EXISTS crawl_records_global_stats_mv
			TO crawl_records_global_stats AS
			SELECT
				1 AS key,
				countState() AS records,
				uniqExactState(repo) AS repos,
				uniqExactState(collection) AS collections,
				min(crawled_at) AS min_crawled_at,
				max(crawled_at) AS max_crawled_at,
				max(indexed_at) AS max_indexed_at
			FROM crawl_records`},
		{"crawl_records_by_collection table", `
			CREATE TABLE IF NOT EXISTS crawl_records_by_collection (
				collection String,
				records AggregateFunction(count),
				repos AggregateFunction(uniqExact, String)
			) ENGINE = AggregatingMergeTree()
			ORDER BY collection`},
		{"crawl_records_by_collection_mv", `
			CREATE MATERIALIZED VIEW IF NOT EXISTS crawl_records_by_collection_mv
			TO crawl_records_by_collection AS
			SELECT
				collection,
				countState() AS records,
				uniqExactState(repo) AS repos
			FROM crawl_records
			GROUP BY collection`},
		{"crawl_records_by_repo table", `
			CREATE TABLE IF NOT EXISTS crawl_records_by_repo (
				repo String,
				records AggregateFunction(count),
				collections AggregateFunction(uniqExact, String)
			) ENGINE = AggregatingMergeTree()
			ORDER BY repo`},
		{"crawl_records_by_repo_mv", `
			CREATE MATERIALIZED VIEW IF NOT EXISTS crawl_records_by_repo_mv
			TO crawl_records_by_repo AS
			SELECT
				repo,
				countState() AS records,
				uniqExactState(collection) AS collections
			FROM crawl_records
			GROUP BY repo`},
		{"crawl_records_by_session table", `
			CREATE TABLE IF NOT EXISTS crawl_records_by_session (
				session DateTime,
				records AggregateFunction(count),
				repos AggregateFunction(uniqExact, String),
				collections AggregateFunction(uniqExact, String),
				min_indexed_at SimpleAggregateFunction(min, DateTime64(6)),
				max_indexed_at SimpleAggregateFunction(max, DateTime64(6))
			) ENGINE = AggregatingMergeTree()
			ORDER BY session`},
		{"crawl_records_by_session_mv", `
			CREATE MATERIALIZED VIEW IF NOT EXISTS crawl_records_by_session_mv
			TO crawl_records_by_session AS
			SELECT
				toStartOfFiveMinutes(indexed_at) AS session,
				countState() AS records,
				uniqExactState(repo) AS repos,
				uniqExactState(collection) AS collections,
				min(indexed_at) AS min_indexed_at,
				max(indexed_at) AS max_indexed_at
			FROM crawl_records
			GROUP BY session`},
	}

	for _, d := range ddl {
		logger.Info("creating", "object", d.name)
		if err := chConn.Exec(ctx, d.stmt); err != nil {
			return fmt.Errorf("creating %s: %w", d.name, err)
		}
	}

	backfills := []struct {
		name string
		stmt string
	}{
		{"global stats", `
			INSERT INTO crawl_records_global_stats
			SELECT 1 AS key, countState() AS records, uniqExactState(repo) AS repos,
				uniqExactState(collection) AS collections, min(crawled_at) AS min_crawled_at,
				max(crawled_at) AS max_crawled_at, max(indexed_at) AS max_indexed_at
			FROM crawl_records`},
		{"per-collection stats", `
			INSERT INTO crawl_records_by_collection
			SELECT collection, countState() AS records, uniqExactState(repo) AS repos
			FROM crawl_records GROUP BY collection`},
		{"per-repo stats", `
			INSERT INTO crawl_records_by_repo
			SELECT repo, countState() AS records, uniqExactState(collection) AS collections
			FROM crawl_records GROUP BY repo`},
		{"replay sessions", `
			INSERT INTO crawl_records_by_session
			SELECT toStartOfFiveMinutes(indexed_at) AS session, countState() AS records,
				uniqExactState(repo) AS repos, uniqExactState(collection) AS collections,
				min(indexed_at) AS min_indexed_at, max(indexed_at) AS max_indexed_at
			FROM crawl_records GROUP BY session`},
	}

	for _, b := range backfills {
		logger.Info("backfilling", "target", b.name)
		start := time.Now()
		if err := chConn.Exec(ctx, b.stmt); err != nil {
			return fmt.Errorf("backfilling %s: %w", b.name, err)
		}
		logger.Info("backfill complete", "target", b.name, "duration", time.Since(start).Round(time.Second))
	}

	logger.Info("all materialized views created and backfilled")
	return nil
}
