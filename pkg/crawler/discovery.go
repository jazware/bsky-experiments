package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/xrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/time/rate"
)

type DiscoveryConfig struct {
	RelayHost      string
	Workers        int
	DiscoveryRPS   float64
	RelayRPS       float64
	ListReposLimit int64
	SkipPDS        []string
	Logger         *slog.Logger
}

type HostInfo struct {
	Hostname     string
	AccountCount int64
	URL          string
}

type DiscoveredRepo struct {
	DID    string
	PDS    string
	Head   string
	Rev    string
	Active bool
	Status string
}

type Discoverer struct {
	config DiscoveryConfig
	logger *slog.Logger
}

func NewDiscoverer(config DiscoveryConfig) *Discoverer {
	if config.Workers <= 0 {
		config.Workers = 10
	}
	if config.DiscoveryRPS <= 0 {
		config.DiscoveryRPS = 2
	}
	if config.RelayRPS <= 0 {
		config.RelayRPS = 5
	}
	if config.ListReposLimit <= 0 {
		config.ListReposLimit = 1000
	}
	return &Discoverer{
		config: config,
		logger: config.Logger,
	}
}

func newXRPCClient(host string) *xrpc.Client {
	transport := otelhttp.NewTransport(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	})
	return &xrpc.Client{
		Client: &http.Client{Transport: transport},
		Host:   host,
	}
}

// ListHosts pages through the relay's listHosts endpoint and returns all
// non-junk PDS hosts, sorted by account count descending.
func (d *Discoverer) ListHosts(ctx context.Context) ([]HostInfo, error) {
	ctx, span := tracer.Start(ctx, "discoverer.listHosts")
	defer span.End()

	client := newXRPCClient(d.config.RelayHost)
	limiter := rate.NewLimiter(rate.Limit(d.config.RelayRPS), 1)

	skipSet := make(map[string]bool, len(d.config.SkipPDS))
	for _, s := range d.config.SkipPDS {
		skipSet[s] = true
	}

	var hosts []HostInfo
	cursor := ""

	for {
		if err := limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("relay rate limit: %w", err)
		}

		out, err := comatproto.SyncListHosts(ctx, client, cursor, 1000)
		if err != nil {
			return nil, fmt.Errorf("listHosts: %w", err)
		}

		for _, h := range out.Hosts {
			url := "https://" + h.Hostname
			if isJunkPDS(url) {
				continue
			}
			if skipSet[url] {
				continue
			}

			var accountCount int64
			if h.AccountCount != nil {
				accountCount = *h.AccountCount
			}

			hosts = append(hosts, HostInfo{
				Hostname:     h.Hostname,
				AccountCount: accountCount,
				URL:          url,
			})
		}

		if out.Cursor == nil || *out.Cursor == "" {
			break
		}
		cursor = *out.Cursor
	}

	sort.Slice(hosts, func(i, j int) bool {
		return hosts[i].AccountCount > hosts[j].AccountCount
	})

	hostsDiscoveredTotal.Add(float64(len(hosts)))

	d.logger.Info("discovered hosts from relay",
		"count", len(hosts), "relay", d.config.RelayHost)

	return hosts, nil
}

// EnumerateRepos concurrently pages through listRepos on each PDS and sends
// batches of discovered repos to the output channel. The channel is closed
// when all PDSs have been enumerated.
func (d *Discoverer) EnumerateRepos(ctx context.Context, hosts []HostInfo, output chan<- []DiscoveredRepo) error {
	ctx, span := tracer.Start(ctx, "discoverer.enumerateRepos")
	defer span.End()
	defer close(output)

	prepareHostsRemaining.Set(float64(len(hosts)))

	work := make(chan HostInfo, len(hosts))
	for _, h := range hosts {
		work <- h
	}
	close(work)

	var wg sync.WaitGroup
	errCount := int64(0)
	var mu sync.Mutex

	for i := 0; i < d.config.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for host := range work {
				if ctx.Err() != nil {
					return
				}
				count, err := d.enumeratePDS(ctx, host, output)
				prepareHostsRemaining.Dec()
				if err != nil {
					d.logger.Warn("failed to enumerate PDS",
						"pds", host.URL, "error", err)
					discoveryErrorsTotal.WithLabelValues("pds_enumerate").Inc()
					mu.Lock()
					errCount++
					mu.Unlock()
					continue
				}

				hostType := "self_hosted"
				if strings.HasSuffix(host.Hostname, ".host.bsky.network") {
					hostType = "bsky_infra"
				}
				discoveryReposTotal.WithLabelValues(hostType).Add(float64(count))

				d.logger.Info("enumerated PDS",
					"pds", host.URL, "repos", count)
			}
		}()
	}

	wg.Wait()

	d.logger.Info("repo enumeration complete",
		"hosts", len(hosts), "errors", errCount)

	return nil
}

// enumeratePDS pages through listRepos for a single PDS and sends batches to
// the output channel. Returns the total repo count.
func (d *Discoverer) enumeratePDS(ctx context.Context, host HostInfo, output chan<- []DiscoveredRepo) (int, error) {
	client := newXRPCClient(host.URL)
	limiter := rate.NewLimiter(rate.Limit(d.config.DiscoveryRPS), 1)

	cursor := ""
	total := 0

	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}

		if err := limiter.Wait(ctx); err != nil {
			return total, fmt.Errorf("rate limit: %w", err)
		}

		out, err := comatproto.SyncListRepos(ctx, client, cursor, d.config.ListReposLimit)
		if err != nil {
			return total, fmt.Errorf("listRepos %s: %w", host.URL, err)
		}

		discoveryPagesTotal.Inc()

		var batch []DiscoveredRepo
		for _, repo := range out.Repos {
			active := repo.Active == nil || *repo.Active
			status := ""
			if repo.Status != nil {
				status = *repo.Status
			}

			if !active && isPermanentStatus(status) {
				continue
			}

			batch = append(batch, DiscoveredRepo{
				DID:    repo.Did,
				PDS:    host.URL,
				Head:   repo.Head,
				Rev:    repo.Rev,
				Active: active,
				Status: status,
			})
		}

		if len(batch) > 0 {
			select {
			case output <- batch:
			case <-ctx.Done():
				return total, ctx.Err()
			}
			total += len(batch)
		}

		if out.Cursor == nil || *out.Cursor == "" {
			break
		}
		cursor = *out.Cursor
	}

	return total, nil
}

func isPermanentStatus(status string) bool {
	switch status {
	case "takendown", "suspended", "deleted":
		return true
	}
	return false
}
