package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jazware/bsky-experiments/pkg/repoarchive"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

var tracer = otel.Tracer("crawler")

type Config struct {
	OutputDir     string
	Workers       int
	DefaultPDSRPS float64
	SegmentSize   int64
	ZstdLevel     int
	MaxRetries    int
	MaxRepoSizeMB int
	PageSize      int
	SkipPDS       []string

	// Direct mode: write directly to ClickHouse instead of .rca archives.
	Direct      bool
	DirectCHConn driver.Conn // Bulk-optimized connection for direct inserts.

	ClickHouseConn driver.Conn
	Logger         *slog.Logger
}

type Crawler struct {
	config     Config
	progress   *Progress
	dispatcher *Dispatcher
	writer     *repoarchive.SegmentWriter
	chWriter   *CHWriter
	logger     *slog.Logger

	repoQueue    chan *repoarchive.SerializedRepo
	crawledQueue chan *repoarchive.CrawledRepo
	cancel       context.CancelFunc
	wg           sync.WaitGroup
}

func NewCrawler(config Config) (*Crawler, error) {
	if config.Workers <= 0 {
		config.Workers = 50
	}
	if config.DefaultPDSRPS <= 0 {
		config.DefaultPDSRPS = 5
	}
	if config.SegmentSize <= 0 {
		config.SegmentSize = repoarchive.DefaultSegmentSize
	}
	if config.ZstdLevel <= 0 {
		config.ZstdLevel = repoarchive.DefaultZstdLevel
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = 3
	}
	if config.MaxRepoSizeMB <= 0 {
		config.MaxRepoSizeMB = 100
	}
	if config.PageSize <= 0 {
		config.PageSize = 50000
	}

	progress := NewProgress(config.Logger)

	c := &Crawler{
		config:   config,
		progress: progress,
		logger:   config.Logger,
	}

	if config.Direct {
		c.crawledQueue = make(chan *repoarchive.CrawledRepo, 100)
	} else {
		c.repoQueue = make(chan *repoarchive.SerializedRepo, 100)
	}

	return c, nil
}

func (c *Crawler) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	defer cancel()

	if c.config.Direct {
		c.chWriter = NewCHWriter(c.config.DirectCHConn, c.crawledQueue, c.logger)
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.chWriter.Run(ctx)
		}()
	} else {
		segNum := c.progress.GetSegmentNum()
		writer, err := repoarchive.NewSegmentWriter(
			c.config.OutputDir,
			repoarchive.WithSegmentSize(c.config.SegmentSize),
			repoarchive.WithZstdLevel(c.config.ZstdLevel),
			repoarchive.WithStartSegment(segNum),
		)
		if err != nil {
			return fmt.Errorf("creating segment writer: %w", err)
		}
		c.writer = writer

		c.wg.Add(1)
		go c.writerLoop(ctx)
	}

	c.dispatcher = NewDispatcher(c.config.DefaultPDSRPS, c.logger)
	workerPool := NewWorkerPool(c.config.Workers, c.repoQueue, c.crawledQueue, c.progress, c.dispatcher, c.config, c.logger)
	dispatchErr := make(chan error, 1)
	go func() {
		dispatchErr <- c.dispatcher.Run(ctx, workerPool.WorkQueue())
	}()

	cursor := c.progress.GetCursor()
	if cursor != "" {
		c.logger.Info("resuming crawl from cursor", "cursor", cursor)
	}

	var totalPages int

	for {
		if ctx.Err() != nil {
			break
		}

		// Backpressure: wait until the dispatcher has drained enough before
		// fetching the next page to avoid unbounded memory growth.
		// Use 10x page size so we always have work from many PDSs even
		// when a few large ones drain slowly.
		for c.dispatcher.Remaining() > int64(c.config.PageSize)*10 {
			// When PDS diversity is low, fetch more pages even if remaining is high.
			// Cap at 10M DIDs to prevent OOM.
			if c.dispatcher.ActivePDSCount() < 50 &&
				c.dispatcher.Remaining() < 10_000_000 {
				break
			}
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				break
			}
		}
		if ctx.Err() != nil {
			break
		}

		pdsGroups, nextCursor, pageTotal, err := c.fetchPage(ctx, cursor)
		if err != nil {
			c.logger.Error("failed to fetch page", "error", err)
			break
		}
		totalPages++
		pagesFetchedTotal.Inc()

		c.logger.Info("fetched page from ClickHouse",
			"cursor", cursor, "dids", pageTotal, "pds_count", len(pdsGroups))

		filteredCount := c.progress.FilterPage(pdsGroups)
		if filteredCount > 0 {
			c.logger.Info("filtered completed DIDs from page", "skipped", filteredCount)
		}

		junkFiltered, dnsFiltered := c.filterJunkPDS(pdsGroups)
		if junkFiltered > 0 || dnsFiltered > 0 {
			c.logger.Info("filtered junk/unresolvable PDS entries",
				"junk_pds", junkFiltered, "dns_failed", dnsFiltered)
		}

		remaining := 0
		for _, dids := range pdsGroups {
			remaining += len(dids)
		}
		pageDIDs.Observe(float64(remaining))

		if remaining > 0 {
			c.logger.Info("feeding page to dispatcher", "remaining", remaining, "pds_count", len(pdsGroups))
			c.dispatcher.Feed(pdsGroups)
		}

		if nextCursor != "" {
			cursor = nextCursor
			c.progress.SetCursor(cursor)
		}

		if pageTotal < c.config.PageSize {
			c.logger.Info("reached end of DID data", "pages", totalPages)
			break
		}
	}

	c.logger.Info("waiting for dispatcher to drain", "remaining", c.dispatcher.Remaining())
	for c.dispatcher.Remaining() > 0 {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}

	c.dispatcher.Stop()
	<-dispatchErr

	workerPool.Stop()

	if c.config.Direct {
		close(c.crawledQueue)
		c.wg.Wait()

		c.progress.ClearCursor()

		c.logger.Info("crawl complete",
			"total_records", c.chWriter.Sent(),
			"pages", totalPages)
	} else {
		close(c.repoQueue)
		c.wg.Wait()

		if err := c.writer.Close(); err != nil {
			return fmt.Errorf("closing segment writer: %w", err)
		}

		c.progress.SetSegmentNum(c.writer.SegmentNum())
		c.progress.ClearCursor()

		c.logger.Info("crawl complete",
			"total_repos", c.writer.TotalRepos(),
			"bytes_written", c.writer.BytesWritten(),
			"segments", c.writer.SegmentNum(),
			"pages", totalPages)
	}

	return nil
}

func (c *Crawler) Shutdown() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// writerLoop receives pre-serialized repos and writes them to segments.
// Compression is already done by workers; this loop only does sequential I/O.
func (c *Crawler) writerLoop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	lastSegment := c.writer.SegmentNum()

	for {
		select {
		case sr, ok := <-c.repoQueue:
			if !ok {
				return
			}

			if err := c.writer.WriteSerializedRepo(sr); err != nil {
				c.logger.Error("failed to write repo", "did", sr.DID, "error", err)
				continue
			}

			bytesWrittenTotal.Add(float64(len(sr.BlockData)))
			repoCompressedBytes.Observe(float64(len(sr.BlockData)))

			currentSegment := c.writer.SegmentNum()
			if currentSegment != lastSegment {
				segmentsFinalizedTotal.Inc()
				lastSegment = currentSegment
			}

			c.progress.SetSegmentNum(currentSegment)
		case <-ticker.C:
			c.logger.Info("writer progress",
				"total_repos", c.writer.TotalRepos(),
				"bytes_written", c.writer.BytesWritten(),
				"segment", c.writer.SegmentNum())
		case <-ctx.Done():
			for sr := range c.repoQueue {
				if err := c.writer.WriteSerializedRepo(sr); err != nil {
					c.logger.Error("failed to write repo during drain", "did", sr.DID, "error", err)
				}
			}
			return
		}
	}
}

// filterJunkPDS removes PDS entries that are obviously invalid (localhost, example
// domains, etc.) and performs DNS resolution on the rest, removing entries whose
// hostnames don't resolve. It modifies pdsGroups in place and returns the number
// of PDS entries removed by each filter.
func (c *Crawler) filterJunkPDS(pdsGroups map[string][]string) (junkCount, dnsCount int) {
	for pdsURL := range pdsGroups {
		if isJunkPDS(pdsURL) {
			junkCount++
			delete(pdsGroups, pdsURL)
		}
	}

	// Skip DNS checks for known-good infrastructure to avoid unnecessary lookups.
	resolver := &net.Resolver{PreferGo: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for pdsURL := range pdsGroups {
		parsed, err := url.Parse(pdsURL)
		if err != nil {
			dnsCount++
			delete(pdsGroups, pdsURL)
			continue
		}

		host := parsed.Hostname()

		if strings.HasSuffix(host, ".host.bsky.network") ||
			strings.HasSuffix(host, ".bsky.network") {
			continue
		}

		addrs, err := resolver.LookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			dnsCount++
			delete(pdsGroups, pdsURL)
		}
	}

	return junkCount, dnsCount
}

// isJunkPDS returns true for PDS URLs that are obviously invalid and should
// never be crawled (localhost, example domains, private IPs, etc.).
func isJunkPDS(pdsURL string) bool {
	parsed, err := url.Parse(pdsURL)
	if err != nil {
		return true
	}

	host := parsed.Hostname()

	if host == "" {
		return true
	}

	// ATProto PDS must have a DNS name, not a raw IP address.
	if net.ParseIP(host) != nil {
		return true
	}

	if host == "localhost" || host == "localhost.localdomain" {
		return true
	}

	switch host {
	case "example.com", "example.test", "example.org", "example.net",
		"test.com", "uwu":
		return true
	}

	// Ngrok and other tunnel services with random subdomains.
	if strings.HasSuffix(host, ".ngrok-free.app") ||
		strings.HasSuffix(host, ".ngrok.io") ||
		strings.HasSuffix(host, ".ngrok.app") {
		return true
	}

	return false
}

// fetchPage queries ClickHouse for one page of DIDs using cursor-based pagination.
// Returns pdsGroups, the last DID seen (next cursor), and total DIDs in the page.
func (c *Crawler) fetchPage(ctx context.Context, cursor string) (map[string][]string, string, int, error) {
	ctx, span := tracer.Start(ctx, "crawler.fetchPage")
	defer span.End()

	span.SetAttributes(
		attribute.String("cursor", cursor),
		attribute.Int("page.size", c.config.PageSize),
	)

	query := "SELECT did, pds FROM crawl_repos FINAL WHERE pds != '' AND did > ?"
	args := []any{cursor}
	if len(c.config.SkipPDS) > 0 {
		query += " AND pds NOT IN (?)"
		args = append(args, c.config.SkipPDS)
	}
	query += " ORDER BY did LIMIT ?"
	args = append(args, c.config.PageSize)

	rows, err := c.config.ClickHouseConn.Query(ctx, query, args...)
	if err != nil {
		return nil, "", 0, fmt.Errorf("querying crawl_repos: %w", err)
	}
	defer rows.Close()

	pdsGroups := make(map[string][]string)
	total := 0
	var lastDID string
	for rows.Next() {
		var did, pds string
		if err := rows.Scan(&did, &pds); err != nil {
			return nil, "", 0, fmt.Errorf("scanning row: %w", err)
		}
		pdsGroups[pds] = append(pdsGroups[pds], did)
		lastDID = did
		total++
	}
	if err := rows.Err(); err != nil {
		return nil, "", 0, fmt.Errorf("row iteration: %w", err)
	}

	span.SetAttributes(
		attribute.Int("pds.count", len(pdsGroups)),
		attribute.String("page.last_did", lastDID),
	)

	return pdsGroups, lastDID, total, nil
}
