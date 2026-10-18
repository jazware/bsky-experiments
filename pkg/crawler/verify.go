package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"golang.org/x/time/rate"
)

type VerifiedRepo struct {
	DiscoveredRepo
	Verified bool
}

type Verifier struct {
	chConn     driver.Conn
	httpClient *http.Client
	webLimiter *rate.Limiter
	logger     *slog.Logger
}

func NewVerifier(chConn driver.Conn, logger *slog.Logger) *Verifier {
	return &Verifier{
		chConn: chConn,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		webLimiter: rate.NewLimiter(rate.Limit(5), 1),
		logger:     logger,
	}
}

// VerifyBatch verifies a batch of discovered repos by checking their PDS claims.
// For did:plc DIDs, it checks against plc_did_state in ClickHouse.
// For did:web DIDs, it resolves the DID document over HTTP.
// Returns only repos that pass verification (or are unverified but kept).
func (v *Verifier) VerifyBatch(ctx context.Context, repos []DiscoveredRepo) []VerifiedRepo {
	if len(repos) == 0 {
		return nil
	}

	var plcDIDs []string
	plcRepos := make(map[string][]DiscoveredRepo)
	var webRepos []DiscoveredRepo

	for _, r := range repos {
		switch {
		case strings.HasPrefix(r.DID, "did:plc:"):
			plcDIDs = append(plcDIDs, r.DID)
			plcRepos[r.DID] = append(plcRepos[r.DID], r)
		case strings.HasPrefix(r.DID, "did:web:"):
			webRepos = append(webRepos, r)
		default:
			v.logger.Debug("skipping unknown DID method", "did", r.DID)
		}
	}

	var results []VerifiedRepo

	if len(plcDIDs) > 0 {
		plcPDS, err := v.lookupPLCState(ctx, plcDIDs)
		if err != nil {
			v.logger.Warn("failed to query plc_did_state, keeping all as unverified", "error", err)
			for _, r := range repos {
				if strings.HasPrefix(r.DID, "did:plc:") {
					results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: false})
					verificationTotal.WithLabelValues("plc_error").Inc()
				}
			}
		} else {
			for did, repoList := range plcRepos {
				plcEndpoint, found := plcPDS[did]
				for _, r := range repoList {
					if !found {
						// Not in PLC — keep as unverified (could be a new account).
						results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: false})
						verificationTotal.WithLabelValues("plc_not_found").Inc()
					} else if normalizePDS(plcEndpoint) == normalizePDS(r.PDS) {
						results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: true})
						verificationTotal.WithLabelValues("plc_match").Inc()
					} else {
						// PDS mismatch — exclude.
						verificationTotal.WithLabelValues("plc_mismatch").Inc()
					}
				}
			}
		}
	}

	for _, r := range webRepos {
		if ctx.Err() != nil {
			break
		}

		pdsEndpoint, err := v.resolveDIDWeb(ctx, r.DID)
		if err != nil {
			v.logger.Debug("did:web resolution failed, excluding",
				"did", r.DID, "error", err)
			verificationTotal.WithLabelValues("web_error").Inc()
			continue
		}

		if normalizePDS(pdsEndpoint) == normalizePDS(r.PDS) {
			results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: true})
			verificationTotal.WithLabelValues("web_match").Inc()
		} else {
			verificationTotal.WithLabelValues("web_mismatch").Inc()
		}
	}

	return results
}

func (v *Verifier) lookupPLCState(ctx context.Context, dids []string) (map[string]string, error) {
	rows, err := v.chConn.Query(ctx,
		"SELECT did, pds FROM plc_did_state FINAL WHERE did IN (?)", dids)
	if err != nil {
		return nil, fmt.Errorf("querying plc_did_state: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string, len(dids))
	for rows.Next() {
		var did, pds string
		if err := rows.Scan(&did, &pds); err != nil {
			return nil, fmt.Errorf("scanning plc row: %w", err)
		}
		result[did] = pds
	}
	return result, rows.Err()
}

// didDocument is the minimal structure of a DID document needed for
// extracting the PDS service endpoint.
type didDocument struct {
	Service []didService `json:"service"`
}

type didService struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

func (v *Verifier) resolveDIDWeb(ctx context.Context, did string) (string, error) {
	if err := v.webLimiter.Wait(ctx); err != nil {
		return "", fmt.Errorf("rate limit: %w", err)
	}

	domain := strings.TrimPrefix(did, "did:web:")
	if domain == "" {
		return "", fmt.Errorf("empty did:web domain")
	}
	domain = strings.ReplaceAll(domain, ":", "/")

	url := "https://" + domain + "/.well-known/did.json"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching DID doc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DID doc HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("reading DID doc: %w", err)
	}

	var doc didDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("parsing DID doc: %w", err)
	}

	for _, svc := range doc.Service {
		if svc.ID == "#atproto_pds" && svc.Type == "AtprotoPersonalDataServer" {
			return svc.ServiceEndpoint, nil
		}
	}

	return "", fmt.Errorf("no #atproto_pds service in DID doc")
}

// PreloadPLCState loads the full DID→PDS mapping from plc_did_state into memory.
// Uses argMax instead of FINAL for better performance on large ReplacingMergeTree tables.
// PDS URL strings are interned to reduce memory since most accounts share a few PDS hosts.
func PreloadPLCState(ctx context.Context, conn driver.Conn, logger *slog.Logger) (map[string]string, error) {
	start := time.Now()
	logger.Info("pre-loading PLC state from ClickHouse")

	rows, err := conn.Query(ctx,
		"SELECT did, argMax(pds, time_us) AS pds FROM plc_did_state GROUP BY did")
	if err != nil {
		return nil, fmt.Errorf("querying plc_did_state: %w", err)
	}
	defer rows.Close()

	// Intern PDS strings to reduce memory — most accounts share a few PDS URLs.
	pdsIntern := make(map[string]string)
	intern := func(s string) string {
		if v, ok := pdsIntern[s]; ok {
			return v
		}
		pdsIntern[s] = s
		return s
	}

	result := make(map[string]string, 50_000_000)
	for rows.Next() {
		var did, pds string
		if err := rows.Scan(&did, &pds); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		result[did] = intern(pds)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration: %w", err)
	}

	logger.Info("PLC state loaded",
		"entries", len(result),
		"unique_pds", len(pdsIntern),
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}

// did:plc repos are checked via map lookup; did:web repos are included as unverified.
func VerifyInMemory(repos []DiscoveredRepo, plcState map[string]string) []VerifiedRepo {
	results := make([]VerifiedRepo, 0, len(repos))
	for _, r := range repos {
		switch {
		case strings.HasPrefix(r.DID, "did:plc:"):
			plcEndpoint, found := plcState[r.DID]
			if !found {
				results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: false})
				verificationTotal.WithLabelValues("plc_not_found").Inc()
			} else if normalizePDS(plcEndpoint) == normalizePDS(r.PDS) {
				results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: true})
				verificationTotal.WithLabelValues("plc_match").Inc()
			} else {
				verificationTotal.WithLabelValues("plc_mismatch").Inc()
			}
		case strings.HasPrefix(r.DID, "did:web:"):
			// Include did:web as unverified — too rare to warrant HTTP resolution
			// during prepare. The crawl step can verify individually.
			results = append(results, VerifiedRepo{DiscoveredRepo: r, Verified: false})
			verificationTotal.WithLabelValues("web_skipped").Inc()
		}
	}
	return results
}

func normalizePDS(pds string) string {
	return strings.TrimRight(pds, "/")
}
