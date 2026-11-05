package crawler

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ipfs/go-cid"
	carv1 "github.com/ipld/go-car"
)

func testDispatcher() *Dispatcher {
	return NewDispatcher(5, 5, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestPDSRateCap(t *testing.T) {
	cases := []struct {
		url        string
		n          int
		max, wantR float64
	}{
		{"https://morel.us-east.host.bsky.network", 400000, 0, 10},
		{"https://morel.us-east.host.bsky.network", 400000, 5, 5},
		{"https://big.example", 5000, 5, 5},
		{"https://small.example", 3, 5, 2},
		{"https://mid.example", 500, 0, 5},
	}
	for _, c := range cases {
		if got := pdsRate(c.url, c.n, 5, c.max); got != c.wantR {
			t.Errorf("pdsRate(%s, %d, max %v) = %v, want %v", c.url, c.n, c.max, got, c.wantR)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("", now); got != defaultRetryAfter {
		t.Errorf("empty: %v", got)
	}
	if got := parseRetryAfter("7", now); got != 7*time.Second {
		t.Errorf("seconds: %v", got)
	}
	if got := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90*time.Second {
		t.Errorf("date: %v", got)
	}
}

func TestStatusError(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
		xrpc   string
	}{
		{400, `{"error":"RepoNotFound","message":"Could not find repo"}`, "not_found", "RepoNotFound"},
		{400, `{"error":"RepoDeactivated","message":"Repo has been deactivated"}`, "deactivated", "RepoDeactivated"},
		{400, `{"error":"RepoTakendown","message":"Repo has been takendown"}`, "takendown", "RepoTakendown"},
		{404, `not json`, "not_found", ""},
		{500, `{"error":"InternalServerError","message":"Internal Server Error"}`, "unavailable", "InternalServerError"},
		{502, ``, "unavailable", ""},
		{429, `{"error":"RateLimitExceeded"}`, "rate_limited", "RateLimitExceeded"},
		{401, `{"error":"AuthRequired"}`, "http_error", "AuthRequired"},
	}
	for _, c := range cases {
		resp := &http.Response{StatusCode: c.status, Status: fmt.Sprint(c.status), Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(c.body))}
		e := statusError(resp, &CrawlTask{DID: "did:plc:x", PDS: "https://p"}, time.Now())
		if e.Category != c.want || e.XRPCError != c.xrpc || e.Code != c.status {
			t.Errorf("%d %s: got %s/%q/%d, want %s/%q", c.status, c.body, e.Category, e.XRPCError, e.Code, c.want, c.xrpc)
		}
	}
}

func feedOne(d *Dispatcher, pds string, n int) {
	dids := make([]string, n)
	for i := range dids {
		dids[i] = fmt.Sprintf("did:plc:%024d", i)
	}
	var active []*PDSState
	d.remaining.Add(int64(n))
	d.ingest(map[string][]string{pds: dids}, &active)
}

func TestRepo5xxNeverBlocksServedOrBskyHost(t *testing.T) {
	d := testDispatcher()
	feedOne(d, "https://served.example", 100)
	feedOne(d, "https://shard.us-east.host.bsky.network", 100)
	d.handleReport(pdsReport{PDS: "https://served.example", Kind: reportSuccess})
	for range 50 {
		d.handleReport(pdsReport{PDS: "https://served.example", Kind: reportRepo5xx})
		d.handleReport(pdsReport{PDS: "https://shard.us-east.host.bsky.network", Kind: reportRepo5xx})
	}
	for _, u := range []string{"https://served.example", "https://shard.us-east.host.bsky.network"} {
		p := d.pdsIndex[u]
		if p.Blocked || !p.PausedUntil.IsZero() || float64(p.Limiter.Limit()) != p.BaseRPS {
			t.Errorf("%s: blocked=%v paused=%v rps=%v after 5xx", u, p.Blocked, p.PausedUntil, p.Limiter.Limit())
		}
	}
}

func TestNeverServedHostBlockedAfterDistinct5xxRepos(t *testing.T) {
	d := testDispatcher()
	var mu sync.Mutex
	var skipped []string
	d.onBlocked = func(task *CrawlTask, reason string) {
		mu.Lock()
		defer mu.Unlock()
		if !strings.HasPrefix(reason, "host never served a repo") {
			t.Errorf("reason %q", reason)
		}
		skipped = append(skipped, task.DID)
	}
	u := "https://dead.example"
	feedOne(d, u, 30)
	p := d.pdsIndex[u]
	for i := range neverServed5xxRepos - 1 {
		p.next(time.Now())
		d.remaining.Add(-1)
		d.handleReport(pdsReport{PDS: u, Kind: reportRepo5xx, Err: fmt.Sprint(i)})
	}
	if p.Blocked {
		t.Fatal("blocked before the limit")
	}
	// A success in between resets the streak for good.
	d.handleReport(pdsReport{PDS: u, Kind: reportSuccess})
	d.handleReport(pdsReport{PDS: u, Kind: reportRepo5xx})
	if p.Blocked {
		t.Fatal("blocked after serving a repo")
	}

	d2 := testDispatcher()
	d2.onBlocked = d.onBlocked
	feedOne(d2, u, 30)
	p2 := d2.pdsIndex[u]
	for range neverServed5xxRepos {
		p2.next(time.Now())
		d2.remaining.Add(-1)
		d2.handleReport(pdsReport{PDS: u, Kind: reportRepo5xx})
	}
	if !p2.Blocked {
		t.Fatal("not blocked")
	}
	d2.skipBlockedPDS(p2)
	if len(skipped) != 30-neverServed5xxRepos || d2.Remaining() != 0 {
		t.Errorf("skipped %d, remaining %d", len(skipped), d2.Remaining())
	}
}

func TestHostErrorsPauseWithoutDropping(t *testing.T) {
	d := testDispatcher()
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	u := "https://flaky.example"
	feedOne(d, u, 50)
	p := d.pdsIndex[u]
	d.handleReport(pdsReport{PDS: u, Kind: reportSuccess})
	for range hostErrorPauseThreshold {
		d.handleReport(pdsReport{PDS: u, Kind: reportHostError})
	}
	if !p.PausedUntil.Equal(now.Add(hostPauseBase)) {
		t.Fatalf("paused until %v", p.PausedUntil)
	}
	// Errors from fetches in flight when the pause began don't escalate it.
	d.handleReport(pdsReport{PDS: u, Kind: reportHostError})
	if p.Pauses != 1 {
		t.Fatalf("pauses %d", p.Pauses)
	}
	// After the pause one failure pauses again, for longer.
	now = now.Add(2 * time.Minute)
	d.handleReport(pdsReport{PDS: u, Kind: reportHostError})
	if p.Pauses != 2 || !p.PausedUntil.Equal(now.Add(2*hostPauseBase)) {
		t.Fatalf("pauses %d until %v", p.Pauses, p.PausedUntil)
	}
	// A host that has served a repo is never given up on.
	now = now.Add(10 * time.Hour)
	d.handleReport(pdsReport{PDS: u, Kind: reportHostError})
	if p.Blocked || p.remaining() != 50 {
		t.Fatalf("blocked=%v remaining=%d", p.Blocked, p.remaining())
	}
	if p.Pauses != 3 || p.PausedUntil.Sub(now) > hostPauseMax {
		t.Fatalf("pauses %d until %v", p.Pauses, p.PausedUntil)
	}
}

func TestRateLimitPausesForRetryAfter(t *testing.T) {
	d := testDispatcher()
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	u := "https://busy.example"
	feedOne(d, u, 10)
	p := d.pdsIndex[u]
	d.handleReport(pdsReport{PDS: u, Kind: reportRateLimited, RetryAfter: 30 * time.Second})
	if !p.PausedUntil.Equal(now.Add(30*time.Second)) || float64(p.Limiter.Limit()) != p.BaseRPS/2 {
		t.Fatalf("paused until %v rps %v", p.PausedUntil, p.Limiter.Limit())
	}
	d.handleReport(pdsReport{PDS: u, Kind: reportSuccess})
	if float64(p.Limiter.Limit()) != p.BaseRPS {
		t.Fatalf("rps not restored: %v", p.Limiter.Limit())
	}
}

func TestRetriesWaitForReadyAt(t *testing.T) {
	p := &PDSState{URL: "u", DIDs: []string{"a"}}
	now := time.Now()
	p.delay(&CrawlTask{DID: "late", Attempts: 1, ReadyAt: now.Add(time.Minute)})
	p.delay(&CrawlTask{DID: "soon", Attempts: 1, ReadyAt: now.Add(-time.Second)})
	if got := p.next(now); got.DID != "soon" {
		t.Fatalf("first %s", got.DID)
	}
	if got := p.next(now); got.DID != "a" {
		t.Fatalf("second %s", got.DID)
	}
	if p.ready(now) || p.remaining() != 1 {
		t.Fatalf("ready with only a future retry left")
	}
	if got := p.next(now.Add(2 * time.Minute)); got.DID != "late" {
		t.Fatalf("third %s", got.DID)
	}
}

// End to end through the crawler (at 2 rps, the small-PDS rate): every DID in the file gets exactly one
// outcome, 5xx repos are retried and recorded with their XRPC error, and a
// host that never serves a repo is blocked with its rest recorded.
func TestCrawlDIDsFromOutcomes(t *testing.T) {
	repo5xxBackoff, hostErrorBackoff = 10*time.Millisecond, 10*time.Millisecond
	defer func() { repo5xxBackoff, hostErrorBackoff = 30*time.Second, 10*time.Second }()

	var mu sync.Mutex
	hits := map[string]int{}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		did := r.URL.Query().Get("did")
		mu.Lock()
		hits[did]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch did {
		case "did:plc:broken":
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"InternalServerError","message":"Internal Server Error"}`)
		case "did:plc:gone":
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"RepoDeactivated","message":"Repo has been deactivated: did:plc:gone"}`)
		default:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"RepoNotFound","message":"Could not find repo"}`)
		}
	}))
	defer ok.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
	}))
	defer dead.Close()

	dir := t.TempDir()
	var lines []string
	lines = append(lines, "did:plc:broken\t"+ok.URL, "did:plc:gone\t"+ok.URL, "did:plc:missing\t"+ok.URL+"/",
		"did:plc:broken\t"+ok.URL) // a duplicate
	for i := range 14 {
		lines = append(lines, fmt.Sprintf("did:plc:dead%02d\t%s", i, dead.URL))
	}
	didsFile := filepath.Join(dir, "dids.tsv")
	if err := os.WriteFile(didsFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := NewCrawler(Config{
		OutputDir: filepath.Join(dir, "rca"), Workers: 4, DefaultPDSRPS: 100, MaxPDSRPS: 100,
		DIDsFrom: didsFile, OutcomesPath: filepath.Join(dir, "outcomes.tsv"), allowAnyHost: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "rca"), 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Run(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("crawl didn't finish")
	}

	f, err := os.Open(filepath.Join(dir, "outcomes.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string][]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cols := strings.Split(sc.Text(), "\t")
		if len(cols) != 10 {
			t.Fatalf("line %q has %d columns", sc.Text(), len(cols))
		}
		if _, dup := got[cols[0]]; dup {
			t.Fatalf("two outcomes for %s", cols[0])
		}
		got[cols[0]] = cols
	}
	if len(got) != 17 {
		t.Fatalf("%d outcomes, want 17", len(got))
	}
	if b := got["did:plc:broken"]; b[2] != "unavailable" || b[6] != "3" || b[7] != "500" || b[8] != "InternalServerError" {
		t.Errorf("broken: %v", b)
	}
	if hits["did:plc:broken"] != 3 {
		t.Errorf("broken fetched %d times", hits["did:plc:broken"])
	}
	if g := got["did:plc:gone"]; g[2] != "deactivated" || g[8] != "RepoDeactivated" || g[6] != "1" {
		t.Errorf("gone: %v", g)
	}
	if m := got["did:plc:missing"]; m[2] != "not_found" || m[1] != ok.URL {
		t.Errorf("missing: %v", m)
	}
	failed, blocked := 0, 0
	for i := range 14 {
		o := got[fmt.Sprintf("did:plc:dead%02d", i)]
		switch o[2] {
		case "unavailable":
			failed++
		case "pds_blocked":
			blocked++
			if !strings.HasPrefix(o[9], "host never served a repo") {
				t.Errorf("blocked reason %q", o[9])
			}
		default:
			t.Errorf("dead: %v", o)
		}
	}
	if failed < neverServed5xxRepos || blocked == 0 || failed+blocked != 14 {
		t.Errorf("dead host: %d failed, %d blocked", failed, blocked)
	}
}

func TestResolveHostsEachGetsItsOwnTimeout(t *testing.T) {
	var urls []string
	for i := range 300 {
		urls = append(urls, fmt.Sprintf("http://localhost:%d", 1000+i))
	}
	urls = append(urls, "https://does-not-exist.invalid")
	failed := resolveHosts(urls)
	if len(failed) != 1 || failed["https://does-not-exist.invalid"] == "" {
		t.Fatalf("failed: %v", failed)
	}
}

func TestInFlightCapPerHost(t *testing.T) {
	d := testDispatcher()
	d.maxRPS = 0
	work := make(chan *CrawlTask, 1000)
	done := make(chan error, 1)
	go func() { done <- d.Run(t.Context(), work) }()
	dids := make([]string, 2000)
	for i := range dids {
		dids[i] = fmt.Sprintf("did:plc:%024d", i)
	}
	d.Feed(map[string][]string{"https://hung.example": dids})
	time.Sleep(3 * time.Second) // 10 rps would allow ~30
	if n, cap := len(work), int(maxInFlight(10)); n != cap {
		t.Fatalf("%d in flight, want the cap %d", n, cap)
	}
	t1 := <-work
	d.Done(t1)
	time.Sleep(300 * time.Millisecond)
	if n := len(work); n != int(maxInFlight(10)) {
		t.Fatalf("after one Done: %d in flight", n)
	}
	d.Stop()
	<-done
}

func TestTooLargeRepo(t *testing.T) {
	root, err := cid.Prefix{Version: 1, Codec: cid.DagCBOR, MhType: 0x12 /* sha2-256 */, MhLength: -1}.Sum([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	var car bytes.Buffer
	if err := carv1.WriteHeader(&carv1.CarHeader{Roots: []cid.Cid{root}, Version: 1}, &car); err != nil {
		t.Fatal(err)
	}
	car.Write(binary.AppendUvarint(nil, 2<<20)) // one 2 MiB block
	car.Write(make([]byte, 2<<20))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(car.Bytes())
	}))
	defer srv.Close()
	wp := &WorkerPool{client: srv.Client(), config: Config{MaxRepoSizeMB: 1}}
	_, _, _, err = wp.fetchAndParseRepo(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		&CrawlTask{DID: "did:plc:big", PDS: srv.URL})
	if re, ok := err.(*RepoError); !ok || re.Category != "too_large" {
		t.Fatalf("got %v", err)
	}
}
