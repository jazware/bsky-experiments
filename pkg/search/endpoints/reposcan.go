package endpoints

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/araddon/dateparse"
	"github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/mst"
	"github.com/bluesky-social/indigo/repo"
	"github.com/bluesky-social/indigo/repo/carutil"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/ipfs/go-cid"
)

// maxConcurrentRepoScans bounds the whole-repo downloads running at once,
// across the cleanup handler, the cleanup daemon and the repo export
// endpoints. A scan holds a repo's MST nodes (about a quarter of its CAR);
// on 2026-10-18 overlapping scans of one 270k-record repo, each buffering the
// whole CAR, OOM-killed the service twice.
const maxConcurrentRepoScans = 2

var errScanInProgress = errors.New("a scan of this repo is already running, try again in a minute")

// scanLimiter bounds concurrent repo scans and refuses a second scan of a
// repo while one is queued or running, so a user retrying a slow request
// doesn't stack downloads of the same repo.
type scanLimiter struct {
	slots chan struct{}

	mu     sync.Mutex
	active map[string]struct{}
}

func newScanLimiter(n int) *scanLimiter {
	return &scanLimiter{slots: make(chan struct{}, n), active: map[string]struct{}{}}
}

// acquire waits for a slot. A non-empty key that is already queued or running
// fails at once with errScanInProgress. Call release when the scan is done.
func (l *scanLimiter) acquire(ctx context.Context, key string) (release func(), err error) {
	if key != "" {
		l.mu.Lock()
		if _, ok := l.active[key]; ok {
			l.mu.Unlock()
			return nil, errScanInProgress
		}
		l.active[key] = struct{}{}
		l.mu.Unlock()
	}
	unmark := func() {
		if key != "" {
			l.mu.Lock()
			delete(l.active, key)
			l.mu.Unlock()
		}
	}

	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		unmark()
		return nil, ctx.Err()
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.slots
			unmark()
		})
	}, nil
}

// cleanupCriteria is what a cleanup job deletes: records of the given
// cleanup types created before the cutoff.
type cleanupCriteria struct {
	types  []string // "post", "post_with_media", "repost", "like"
	before time.Time
}

// matches decodes only the record types the criteria can delete.
func (c cleanupCriteria) matches(recType string, data []byte) bool {
	switch recType {
	case "app.bsky.feed.post":
		if !slices.Contains(c.types, "post") && !slices.Contains(c.types, "post_with_media") {
			return false
		}
		var rec bsky.FeedPost
		if err := rec.UnmarshalCBOR(bytes.NewReader(data)); err != nil {
			return false
		}
		createdAt, err := dateparse.ParseAny(rec.CreatedAt)
		if err != nil || createdAt.After(c.before) {
			return false
		}
		hasMedia := rec.Embed != nil && ((rec.Embed.EmbedImages != nil && len(rec.Embed.EmbedImages.Images) > 0) ||
			rec.Embed.EmbedVideo != nil || rec.Embed.EmbedGallery != nil ||
			(rec.Embed.EmbedRecordWithMedia != nil && rec.Embed.EmbedRecordWithMedia.Media != nil))
		if hasMedia {
			return slices.Contains(c.types, "post_with_media")
		}
		return slices.Contains(c.types, "post")
	case "app.bsky.feed.repost":
		if !slices.Contains(c.types, "repost") {
			return false
		}
		var rec bsky.FeedRepost
		if err := rec.UnmarshalCBOR(bytes.NewReader(data)); err != nil {
			return false
		}
		createdAt, err := dateparse.ParseAny(rec.CreatedAt)
		return err == nil && createdAt.Before(c.before)
	case "app.bsky.feed.like":
		if !slices.Contains(c.types, "like") {
			return false
		}
		var rec bsky.FeedLike
		if err := rec.UnmarshalCBOR(bytes.NewReader(data)); err != nil {
			return false
		}
		createdAt, err := dateparse.ParseAny(rec.CreatedAt)
		return err == nil && createdAt.Before(c.before)
	}
	return false
}

type repoScan struct {
	// Total is how many records match the criteria.
	Total int
	// Paths are the first matching record paths in key order, at most the
	// maxPaths scanRepoCAR was given.
	Paths []string
	// NodeBytes is what the scan held: the MST nodes it kept to walk.
	NodeBytes int
}

const scanReadBufSize = 64 << 10

// scanRepoCAR reads a repo CAR once, in whatever order its blocks come,
// keeping only the MST nodes and the CIDs of records that match, then walks
// the tree for their paths. Records are decoded and dropped as they stream
// past, so a scan holds the tree, not the repo.
func scanRepoCAR(ctx context.Context, r io.Reader, crit cleanupCriteria, maxPaths int) (*repoScan, error) {
	br, root, err := carutil.NewReader(bufio.NewReaderSize(r, scanReadBufSize))
	if err != nil {
		return nil, fmt.Errorf("opening CAR: %w", err)
	}

	var commit *repo.SignedCommit
	nodes := map[cid.Cid][]byte{}
	matched := map[cid.Cid]struct{}{}
	nodeBytes := 0
	buf := make([]byte, scanReadBufSize)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blk, _, err := br.NextBlockBuf(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CAR block: %w", err)
		}
		data := blk.RawData()

		if blk.Cid() == root {
			var sc repo.SignedCommit
			if err := sc.UnmarshalCBOR(bytes.NewReader(data)); err != nil {
				return nil, fmt.Errorf("decoding commit: %w", err)
			}
			commit = &sc
			continue
		}

		var tc lexutil.CborChecker
		if err := tc.UnmarshalCBOR(bytes.NewReader(data)); err != nil {
			continue
		}
		if tc.Type == "" {
			// No $type: an MST node. It may alias buf, so keep a copy.
			nodes[blk.Cid()] = bytes.Clone(data)
			nodeBytes += len(data)
			continue
		}
		if crit.matches(tc.Type, data) {
			matched[blk.Cid()] = struct{}{}
		}
	}

	if commit == nil {
		return nil, fmt.Errorf("CAR is missing its root commit %s", root)
	}
	if commit.Version != repo.ATP_REPO_VERSION && commit.Version != repo.ATP_REPO_VERSION_2 {
		return nil, fmt.Errorf("unsupported repo version: %d", commit.Version)
	}

	scan := &repoScan{NodeBytes: nodeBytes}
	err = walkMST(nodes, commit.Data, func(key string, val cid.Cid) {
		if _, ok := matched[val]; !ok {
			return
		}
		if !strings.HasPrefix(key, "app.bsky.feed.") || strings.Contains(key, "threadgate") {
			return
		}
		scan.Total++
		if len(scan.Paths) < maxPaths {
			scan.Paths = append(scan.Paths, key)
		}
	})
	if err != nil {
		return nil, err
	}
	return scan, nil
}

// walkMST visits every leaf of the tree rooted at root in key order.
func walkMST(nodes map[cid.Cid][]byte, root cid.Cid, visit func(key string, val cid.Cid)) error {
	raw, ok := nodes[root]
	if !ok {
		return fmt.Errorf("CAR is missing MST node %s", root)
	}
	var nd mst.NodeData
	if err := nd.UnmarshalCBOR(bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("decoding MST node %s: %w", root, err)
	}
	if nd.Left != nil {
		if err := walkMST(nodes, *nd.Left, visit); err != nil {
			return err
		}
	}
	var key []byte
	for _, e := range nd.Entries {
		if e.PrefixLen < 0 || e.PrefixLen > int64(len(key)) {
			return fmt.Errorf("MST node %s: prefix length %d out of range", root, e.PrefixLen)
		}
		key = append(key[:e.PrefixLen], e.KeySuffix...)
		visit(string(key), e.Val)
		if e.Tree != nil {
			if err := walkMST(nodes, *e.Tree, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

// fetchRepoCAR starts com.atproto.sync.getRepo on the client's host and
// returns the CAR body as a stream; xrpc's SyncGetRepo buffers all of it.
func fetchRepoCAR(ctx context.Context, client *xrpc.Client, did string) (io.ReadCloser, error) {
	u := strings.TrimRight(client.Host, "/") + "/xrpc/com.atproto.sync.getRepo?did=" + url.QueryEscape(did)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.ipld.car")
	if client.UserAgent != nil {
		req.Header.Set("User-Agent", *client.UserAgent)
	}
	for k, v := range client.Headers {
		req.Header.Set(k, v)
	}
	if client.Auth != nil && client.Auth.AccessJwt != "" {
		req.Header.Set("Authorization", "Bearer "+client.Auth.AccessJwt)
	}

	httpClient := client.Client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return nil, fmt.Errorf("getRepo: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

// scanRepo downloads a repo and scans it. Callers hold a repoScans slot.
func scanRepo(ctx context.Context, client *xrpc.Client, did string, crit cleanupCriteria, maxPaths int) (*repoScan, error) {
	body, err := fetchRepoCAR(ctx, client, did)
	if err != nil {
		return nil, fmt.Errorf("error getting repo from PDS: %w", err)
	}
	defer body.Close()

	scan, err := scanRepoCAR(ctx, body, crit, maxPaths)
	if err != nil {
		return nil, fmt.Errorf("error reading repo CAR: %w", err)
	}
	return scan, nil
}
