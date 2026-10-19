package endpoints

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	comatproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	lexutil "github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/repo"
	"github.com/bluesky-social/indigo/xrpc"
	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	car "github.com/ipld/go-car"
	carutil "github.com/ipld/go-car/util"
	"github.com/multiformats/go-multihash"
)

type memBlockstore struct {
	blocks map[cid.Cid]blocks.Block
	order  []cid.Cid
}

func (m *memBlockstore) Get(_ context.Context, c cid.Cid) (blocks.Block, error) {
	b, ok := m.blocks[c]
	if !ok {
		return nil, &ipld.ErrNotFound{Cid: c}
	}
	return b, nil
}

func (m *memBlockstore) Put(_ context.Context, b blocks.Block) error {
	if _, ok := m.blocks[b.Cid()]; !ok {
		m.order = append(m.order, b.Cid())
	}
	m.blocks[b.Cid()] = b
	return nil
}

const testDID = "did:plc:testscanrepo"

// buildRepoCAR makes a signed repo with a mix of records and returns it as
// a CAR whose blocks come in shuffled order.
func buildRepoCAR(t *testing.T, posts int, textSize int) []byte {
	t.Helper()
	ctx := context.Background()
	bs := &memBlockstore{blocks: map[cid.Cid]blocks.Block{}}
	r := repo.NewRepo(ctx, testDID, bs)

	old := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Now().UTC()
	subject := &comatproto.RepoStrongRef{
		Uri: "at://did:plc:other/app.bsky.feed.post/3kaaaaaaaaaaa",
		Cid: "bafyreib2rxk3rybk3aobmv5cjuql3bm2twh4jo5uxgf5kpqcsgz7soyxu4",
	}
	imgCid, err := cid.Decode("bafkreibme22gw2h7y2h7tg2fhqotaqjucnbc24deqo72b6mkl2egezxhvy")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("x", textSize)

	put := func(path string, rec repo.CborMarshaler) {
		if _, err := r.PutRecord(ctx, path, rec); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	for i := range posts {
		createdAt := old
		if i%5 == 0 {
			createdAt = recent
		}
		post := &bsky.FeedPost{Text: fmt.Sprintf("%d %s", i, text), CreatedAt: createdAt.Format(time.RFC3339)}
		if i%3 == 0 {
			post.Embed = &bsky.FeedPost_Embed{EmbedImages: &bsky.EmbedImages{Images: []*bsky.EmbedImages_Image{{
				Alt:   "img",
				Image: &lexutil.LexBlob{Ref: lexutil.LexLink(imgCid), MimeType: "image/jpeg", Size: 10},
			}}}}
		}
		put(fmt.Sprintf("app.bsky.feed.post/3k%011d", i), post)
	}
	for i := range posts / 2 {
		put(fmt.Sprintf("app.bsky.feed.like/3l%011d", i), &bsky.FeedLike{CreatedAt: old.Format(time.RFC3339), Subject: subject})
		put(fmt.Sprintf("app.bsky.feed.repost/3r%011d", i), &bsky.FeedRepost{CreatedAt: recent.Format(time.RFC3339), Subject: subject})
		put(fmt.Sprintf("app.bsky.graph.follow/3f%011d", i), &bsky.GraphFollow{CreatedAt: old.Format(time.RFC3339), Subject: "did:plc:other"})
	}
	put("app.bsky.feed.threadgate/3k00000000000", &bsky.FeedThreadgate{CreatedAt: old.Format(time.RFC3339), Post: "at://x/app.bsky.feed.post/3k00000000000"})

	root, _, err := r.Commit(ctx, func(context.Context, string, []byte) ([]byte, error) { return []byte("sig"), nil })
	if err != nil {
		t.Fatal(err)
	}

	order := slices.Clone(bs.order)
	rand.New(rand.NewSource(1)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })

	var buf bytes.Buffer
	if err := car.WriteHeader(&car.CarHeader{Roots: []cid.Cid{root}, Version: 1}, &buf); err != nil {
		t.Fatal(err)
	}
	for _, c := range order {
		if err := carutil.LdWrite(&buf, c.Bytes(), bs.blocks[c].RawData()); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

// oldScan is how cleanupNextBatch found records before it streamed: the
// whole repo loaded, walked from "app.bsky.feed.", each record decoded.
func oldScan(t *testing.T, carBytes []byte, crit cleanupCriteria) []string {
	t.Helper()
	ctx := context.Background()
	rr, err := repo.ReadRepoFromCar(ctx, bytes.NewReader(carBytes))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	err = rr.ForEach(ctx, "app.bsky.feed.", func(path string, _ cid.Cid) error {
		if strings.Contains(path, "threadgate") {
			return nil
		}
		_, rec, err := rr.GetRecord(ctx, path)
		if err != nil {
			return nil
		}
		var buf bytes.Buffer
		if err := rec.MarshalCBOR(&buf); err != nil {
			t.Fatal(err)
		}
		tc, err := lexutil.CborTypeExtract(buf.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if crit.matches(tc, buf.Bytes()) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestScanRepoCARMatchesFullLoad(t *testing.T) {
	carBytes := buildRepoCAR(t, 300, 20)
	crits := []cleanupCriteria{
		{types: []string{"post", "post_with_media", "repost", "like"}, before: time.Now().AddDate(0, 0, -30)},
		{types: []string{"post"}, before: time.Now().AddDate(0, 0, -30)},
		{types: []string{"post_with_media", "like"}, before: time.Now().AddDate(0, 0, -30)},
		{types: []string{"repost"}, before: time.Now().Add(time.Hour)},
	}
	for _, crit := range crits {
		t.Run(strings.Join(crit.types, ","), func(t *testing.T) {
			want := oldScan(t, carBytes, crit)
			if len(want) == 0 {
				t.Fatal("fixture matches nothing")
			}
			got, err := scanRepoCAR(context.Background(), bytes.NewReader(carBytes), crit, 1<<30)
			if err != nil {
				t.Fatal(err)
			}
			if got.Total != len(want) || !slices.Equal(got.Paths, want) {
				t.Fatalf("scan found %d (%v...), full load %d (%v...)", got.Total, head(got.Paths), len(want), head(want))
			}

			capped, err := scanRepoCAR(context.Background(), bytes.NewReader(carBytes), crit, 7)
			if err != nil {
				t.Fatal(err)
			}
			if capped.Total != len(want) || !slices.Equal(capped.Paths, want[:min(7, len(want))]) {
				t.Fatalf("capped scan: total %d paths %v", capped.Total, capped.Paths)
			}
		})
	}
}

func head(s []string) []string { return s[:min(3, len(s))] }

// TestScanRepoCARHoldsOnlyTheTree is the bounded path: a scan keeps the MST
// nodes, not the records, however big they are.
func TestScanRepoCARHoldsOnlyTheTree(t *testing.T) {
	carBytes := buildRepoCAR(t, 2000, 2000)
	crit := cleanupCriteria{types: []string{"post", "post_with_media", "like"}, before: time.Now().AddDate(0, 0, -1)}
	scan, err := scanRepoCAR(context.Background(), bytes.NewReader(carBytes), crit, 10)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Total == 0 || len(scan.Paths) != 10 {
		t.Fatalf("total %d paths %d", scan.Total, len(scan.Paths))
	}
	t.Logf("CAR %d bytes, MST nodes held %d bytes, %d records matched", len(carBytes), scan.NodeBytes, scan.Total)
	if scan.NodeBytes*10 > len(carBytes) {
		t.Fatalf("scan held %d bytes of MST nodes for a %d byte CAR", scan.NodeBytes, len(carBytes))
	}
}

func TestScanRepoCARMissingCommit(t *testing.T) {
	carBytes := buildRepoCAR(t, 5, 5)
	// Cut the CAR to its header: the commit block is gone.
	hdr, err := car.ReadHeader(bufio.NewReader(bytes.NewReader(carBytes)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := car.WriteHeader(hdr, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := scanRepoCAR(context.Background(), &buf, cleanupCriteria{}, 0); err == nil {
		t.Fatal("scan of a CAR without its commit succeeded")
	}
}

func TestScanLimiterBoundsAndDedupes(t *testing.T) {
	l := newScanLimiter(2)
	ctx := context.Background()

	r1, err := l.acquire(ctx, "did:a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.acquire(ctx, "did:a"); !errors.Is(err, errScanInProgress) {
		t.Fatalf("second scan of the same repo: %v", err)
	}
	r2, err := l.acquire(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// Full: a third scan waits.
	got := make(chan func(), 1)
	go func() {
		r, err := l.acquire(ctx, "did:c")
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("third scan ran with two slots taken")
	case <-time.After(50 * time.Millisecond):
	}
	r1()
	r1() // release is idempotent
	var r3 func()
	select {
	case r3 = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("third scan never got the freed slot")
	}

	// did:a finished, so it may scan again; it waits for a slot until its
	// context ends, and then frees its key.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := l.acquire(waitCtx, "did:a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting scan: %v", err)
	}
	r2()
	r4, err := l.acquire(ctx, "did:a")
	if err != nil {
		t.Fatalf("did:a after its wait gave up: %v", err)
	}
	r3()
	r4()
}

func TestFetchRepoCARStreamsWithAuth(t *testing.T) {
	carBytes := buildRepoCAR(t, 20, 5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/com.atproto.sync.getRepo" || r.URL.Query().Get("did") != testDID {
			http.Error(w, `{"error":"RepoNotFound"}`, http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("x-ratelimit-bypass") != "magic" {
			http.Error(w, `{"error":"AuthRequired"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.ipld.car")
		_, _ = w.Write(carBytes)
	}))
	defer srv.Close()

	ua := "test"
	client := &xrpc.Client{
		Client:    srv.Client(),
		Host:      srv.URL,
		UserAgent: &ua,
		Headers:   map[string]string{"x-ratelimit-bypass": "magic"},
		Auth:      &xrpc.AuthInfo{AccessJwt: "access"},
	}
	crit := cleanupCriteria{types: []string{"post"}, before: time.Now().AddDate(0, 0, -1)}
	scan, err := scanRepo(context.Background(), client, testDID, crit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Total == 0 {
		t.Fatal("found no posts")
	}

	if _, err := scanRepo(context.Background(), client, "did:plc:nope", crit, 0); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("missing repo: %v", err)
	}
}

func TestCleanupMatchesVideoAndGalleryAsMedia(t *testing.T) {
	old := time.Now().AddDate(0, 0, -60).Format(time.RFC3339)
	mh, err := multihash.Sum([]byte("v"), multihash.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	embeds := map[string]*bsky.FeedPost_Embed{
		"video":   {EmbedVideo: &bsky.EmbedVideo{Video: &lexutil.LexBlob{Ref: lexutil.LexLink(cid.NewCidV1(cid.Raw, mh)), MimeType: "video/mp4"}}},
		"gallery": {EmbedGallery: &bsky.EmbedGallery{}},
	}
	for name, embed := range embeds {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := (&bsky.FeedPost{Text: "x", CreatedAt: old, Embed: embed}).MarshalCBOR(&buf); err != nil {
				t.Fatal(err)
			}
			before := time.Now().AddDate(0, 0, -30)
			if !(cleanupCriteria{types: []string{"post_with_media"}, before: before}).matches("app.bsky.feed.post", buf.Bytes()) {
				t.Fatal("post_with_media should match")
			}
			if (cleanupCriteria{types: []string{"post"}, before: before}).matches("app.bsky.feed.post", buf.Bytes()) {
				t.Fatal("post should not match")
			}
		})
	}
}
