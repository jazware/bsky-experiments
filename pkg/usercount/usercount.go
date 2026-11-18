package usercount

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"

	"golang.org/x/time/rate"
)

type UserCount struct {
	CurrentUserCount int

	RedisClient *redis.Client
	Prefix      string

	// HostTimeout and Concurrency default to the constants when zero.
	HostTimeout time.Duration
	Concurrency int

	mu   sync.RWMutex // guards PDSs
	PDSs []*PDS
}

func NewUserCount(ctx context.Context, redisClient *redis.Client) *UserCount {
	prefix := "usercount"

	pdsList, err := redisClient.HGetAll(ctx, prefix+":pdslist").Result()
	if err != nil {
		if err != redis.Nil {
			log.Printf("error getting last pds from redis: %s\n", err)
		}
		pdsList = map[string]string{}
	}

	// pdsList is a map of host -> last cursor, last page size, last user count
	// We need to convert it to a slice of PDS structs
	pdsSlice := []*PDS{}
	for host, pdsString := range pdsList {
		pds := NewPDS(host, 25)
		_, err := fmt.Sscanf(pdsString, "%d|%d|%s", &pds.UserCount, &pds.LastPageSize, &pds.LastCursor)
		if err != nil {
			_, err := fmt.Sscanf(pdsString, "%d|%d|", &pds.UserCount, &pds.LastPageSize)
			if err != nil {
				log.Printf("error parsing pds string: %s\n", err)
				continue
			}
			pds.LastCursor = ""
		}
		pdsSlice = append(pdsSlice, pds)
	}

	// If there are no PDSs in redis, add the default list
	// Otherwise, merge any new entries from the default list
	if len(pdsSlice) == 0 {
		for _, host := range PDSHostList {
			pdsSlice = append(pdsSlice, NewPDS(host, 25))
		}
	} else {
		existing := make(map[string]struct{}, len(pdsSlice))
		for _, pds := range pdsSlice {
			existing[pds.Host] = struct{}{}
		}
		for _, host := range PDSHostList {
			if _, ok := existing[host]; !ok {
				slog.Info("adding new PDS from default list", "host", host)
				pdsSlice = append(pdsSlice, NewPDS(host, 25))
			}
		}
	}

	lastUserCount, err := redisClient.Get(ctx, prefix+":last_user_count").Int()
	if err != nil {
		if err != redis.Nil {
			log.Printf("error getting last user count from redis: %s\n", err)
		}
		lastUserCount = 0
	}

	return &UserCount{
		RedisClient:      redisClient,
		Prefix:           prefix,
		CurrentUserCount: lastUserCount,
		PDSs:             pdsSlice,
	}
}

type HostStatus struct {
	Host      string `json:"host"`
	UserCount int    `json:"user_count"`
}

func (uc *UserCount) Hosts() []HostStatus {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	hosts := make([]HostStatus, 0, len(uc.PDSs))
	for _, pds := range uc.PDSs {
		hosts = append(hosts, HostStatus{Host: pds.Host, UserCount: pds.UserCount})
	}
	return hosts
}

// AddPDS adds a host to the scrape list at runtime and persists it to redis so
// it survives restarts. It returns false if the host is already in the list.
// The host is picked up on the next GetUserCount refresh.
func (uc *UserCount) AddPDS(ctx context.Context, host string) (bool, error) {
	uc.mu.Lock()
	defer uc.mu.Unlock()

	for _, pds := range uc.PDSs {
		if pds.Host == host {
			return false, nil
		}
	}

	if err := uc.RedisClient.HSet(ctx, uc.Prefix+":pdslist", host, "0|0|").Err(); err != nil {
		return false, fmt.Errorf("persisting pds to redis: %w", err)
	}

	uc.PDSs = append(uc.PDSs, NewPDS(host, 25))
	slog.Info("added PDS to scrape list at runtime", "host", host)
	return true, nil
}

var PDSHostList = []string{
	// us-east
	"https://amanita.us-east.host.bsky.network",
	"https://blusher.us-east.host.bsky.network",
	"https://coral.us-east.host.bsky.network",
	"https://earthstar.us-east.host.bsky.network",
	"https://elfcup.us-east.host.bsky.network",
	"https://enoki.us-east.host.bsky.network",
	"https://helvella.us-east.host.bsky.network",
	"https://inkcap.us-east.host.bsky.network",
	"https://jellybaby.us-east.host.bsky.network",
	"https://lionsmane.us-east.host.bsky.network",
	"https://lobster.us-east.host.bsky.network",
	"https://meadow.us-east.host.bsky.network",
	"https://morel.us-east.host.bsky.network",
	"https://oyster.us-east.host.bsky.network",
	"https://panthercap.us-east.host.bsky.network",
	"https://parasol.us-east.host.bsky.network",
	"https://porcini.us-east.host.bsky.network",
	"https://puffball.us-east.host.bsky.network",
	"https://reishi.us-east.host.bsky.network",
	"https://scarletina.us-east.host.bsky.network",
	"https://shiitake.us-east.host.bsky.network",
	"https://shimeji.us-east.host.bsky.network",
	"https://splitgill.us-east.host.bsky.network",
	"https://truffle.us-east.host.bsky.network",
	"https://velvetfoot.us-east.host.bsky.network",
	// us-west
	"https://agaric.us-west.host.bsky.network",
	"https://agrocybe.us-west.host.bsky.network",
	"https://auriporia.us-west.host.bsky.network",
	"https://bankera.us-west.host.bsky.network",
	"https://blewit.us-west.host.bsky.network",
	"https://boletus.us-west.host.bsky.network",
	"https://bracket.us-west.host.bsky.network",
	"https://brittlegill.us-west.host.bsky.network",
	"https://button.us-west.host.bsky.network",
	"https://calocybe.us-west.host.bsky.network",
	"https://chaga.us-west.host.bsky.network",
	"https://chalciporus.us-west.host.bsky.network",
	"https://chanterelle.us-west.host.bsky.network",
	"https://conocybe.us-west.host.bsky.network",
	"https://cordyceps.us-west.host.bsky.network",
	"https://cortinarius.us-west.host.bsky.network",
	"https://cremini.us-west.host.bsky.network",
	"https://dapperling.us-west.host.bsky.network",
	"https://discina.us-west.host.bsky.network",
	"https://entoloma.us-west.host.bsky.network",
	"https://fibercap.us-west.host.bsky.network",
	"https://fuzzyfoot.us-west.host.bsky.network",
	"https://ganoderma.us-west.host.bsky.network",
	"https://goldenear.us-west.host.bsky.network",
	"https://gomphidius.us-west.host.bsky.network",
	"https://gomphus.us-west.host.bsky.network",
	"https://grisette.us-west.host.bsky.network",
	"https://hebeloma.us-west.host.bsky.network",
	"https://hedgehog.us-west.host.bsky.network",
	"https://hollowfoot.us-west.host.bsky.network",
	"https://hydnum.us-west.host.bsky.network",
	"https://hygrophorus.us-west.host.bsky.network",
	"https://leccinum.us-west.host.bsky.network",
	"https://lepista.us-west.host.bsky.network",
	"https://magic.us-west.host.bsky.network",
	"https://maitake.us-west.host.bsky.network",
	"https://matsutake.us-west.host.bsky.network",
	"https://mazegill.us-west.host.bsky.network",
	"https://milkcap.us-west.host.bsky.network",
	"https://mottlegill.us-west.host.bsky.network",
	"https://mycena.us-west.host.bsky.network",
	"https://oysterling.us-west.host.bsky.network",
	"https://panus.us-west.host.bsky.network",
	"https://phellinus.us-west.host.bsky.network",
	"https://pholiota.us-west.host.bsky.network",
	"https://pioppino.us-west.host.bsky.network",
	"https://poisonpie.us-west.host.bsky.network",
	"https://polypore.us-west.host.bsky.network",
	"https://psathyrella.us-west.host.bsky.network",
	"https://rhizopogon.us-west.host.bsky.network",
	"https://rooter.us-west.host.bsky.network",
	"https://russula.us-west.host.bsky.network",
	"https://scalycap.us-west.host.bsky.network",
	"https://shaggymane.us-west.host.bsky.network",
	"https://stinkhorn.us-west.host.bsky.network",
	"https://stropharia.us-west.host.bsky.network",
	"https://suillus.us-west.host.bsky.network",
	"https://verpa.us-west.host.bsky.network",
	"https://waxcap.us-west.host.bsky.network",
	"https://witchesbutter.us-west.host.bsky.network",
	"https://woodear.us-west.host.bsky.network",
	"https://woodtuft.us-west.host.bsky.network",
	"https://yellowfoot.us-west.host.bsky.network",
}

type PDS struct {
	Host         string
	UserCount    int
	LastCursor   string
	LastPageSize int
	Limiter      *rate.Limiter
	Client       *xrpc.Client
}

func NewPDS(host string, rps int) *PDS {
	instrumentedTransport := otelhttp.NewTransport(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	})

	client := xrpc.Client{
		Client: &http.Client{
			Transport: instrumentedTransport,
		},
		Host: host,
	}

	return &PDS{
		Host:    host,
		Client:  &client,
		Limiter: rate.NewLimiter(rate.Limit(rps), 1),
	}
}

const (
	// DefaultHostTimeout bounds each listRepos page request to one PDS.
	DefaultHostTimeout = 8 * time.Second
	// DefaultConcurrency bounds how many PDSs are scraped at once.
	DefaultConcurrency = 16
)

// GetUserCount sums the per-host counts. A host that errors or times out is
// skipped for this refresh and keeps its previous count (and cursor), so one
// hung PDS can't stall or fail the refresh.
func (uc *UserCount) GetUserCount(ctx context.Context) (int, error) {
	ctx, span := otel.Tracer("usercount").Start(ctx, "GetUserCount")
	defer span.End()
	// Snapshot the list so a concurrent AddPDS doesn't race the refresh; a
	// host added mid-refresh is picked up on the next cycle.
	uc.mu.RLock()
	pdss := make([]*PDS, len(uc.PDSs))
	copy(pdss, uc.PDSs)
	uc.mu.RUnlock()

	hostTimeout := uc.HostTimeout
	if hostTimeout <= 0 {
		hostTimeout = DefaultHostTimeout
	}
	concurrency := uc.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}

	slog.Info("refreshing user counts")

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var skipped atomic.Int64

	for _, pds := range pdss {
		wg.Add(1)
		go func(pds *PDS) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			if err := uc.scrapeHost(ctx, pds, hostTimeout); err != nil {
				skipped.Add(1)
				slog.Warn("skipping PDS in user count refresh, keeping previous count",
					"host", pds.Host, "count", pds.UserCount, "error", err)
				return
			}
			slog.Info("Finished counting users for PDS", "host", pds.Host, "count", pds.UserCount)
		}(pds)
	}

	wg.Wait()

	if err := ctx.Err(); err != nil {
		return -1, err
	}

	total := 0
	for _, pds := range pdss {
		total += pds.UserCount
	}
	uc.CurrentUserCount = total

	if skipped.Load() > 0 {
		slog.Warn("user count refresh skipped hosts", "skipped", skipped.Load(), "hosts", len(pdss))
	}

	if uc.RedisClient == nil {
		return total, nil
	}

	pdsList := map[string]any{}
	for _, pds := range pdss {
		pdsList[pds.Host] = fmt.Sprintf("%d|%d|%s", pds.UserCount, pds.LastPageSize, pds.LastCursor)
	}

	if err := uc.RedisClient.HSet(ctx, uc.Prefix+":pdslist", pdsList).Err(); err != nil {
		log.Printf("error setting pds list in redis: %s\n", err)
	}

	if err := uc.RedisClient.Set(ctx, uc.Prefix+":last_user_count", uc.CurrentUserCount, 0).Err(); err != nil {
		log.Printf("error setting last user count in redis: %s\n", err)
	}

	return total, nil
}

func (uc *UserCount) scrapeHost(ctx context.Context, pds *PDS, hostTimeout time.Duration) error {
	for {
		if err := pds.Limiter.Wait(ctx); err != nil {
			return fmt.Errorf("error waiting for rate limiter: %w", err)
		}

		reqCtx, cancel := context.WithTimeout(ctx, hostTimeout)
		repoOutput, err := comatproto.SyncListRepos(reqCtx, pds.Client, pds.LastCursor, 1000)
		cancel()
		if err != nil {
			return fmt.Errorf("error listing repos: %w", err)
		}

		numActive := 0
		for _, repo := range repoOutput.Repos {
			if repo.Active != nil && *repo.Active {
				numActive++
			}
		}

		pds.UserCount += numActive
		pds.LastPageSize = len(repoOutput.Repos)

		if repoOutput.Cursor == nil {
			return nil
		}

		pds.LastCursor = *repoOutput.Cursor
	}
}
