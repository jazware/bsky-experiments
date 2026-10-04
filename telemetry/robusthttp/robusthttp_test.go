package robusthttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A server that answers each request with the next status in the list.
func statuses(t *testing.T, header http.Header, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		for k, v := range header {
			w.Header()[k] = v
		}
		w.Header().Set("X-UA", r.Header.Get("User-Agent"))
		w.WriteHeader(codes[min(i, len(codes)-1)])
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func fast(opts ...Option) *http.Client {
	return NewClient(append([]Option{WithBackoff(time.Millisecond, 5*time.Millisecond)}, opts...)...)
}

func TestRetriesSafeRequests(t *testing.T) {
	srv, n := statuses(t, nil, 503, 502, 200)
	resp, err := fast().Get(srv.URL)
	if err != nil || resp.StatusCode != 200 || n.Load() != 3 {
		t.Fatalf("status %v, err %v, attempts %d", resp, err, n.Load())
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	srv, n := statuses(t, nil, 500)
	resp, err := fast(WithMaxRetries(2)).Get(srv.URL)
	if err != nil || resp.StatusCode != 500 || n.Load() != 3 {
		t.Fatalf("status %v, err %v, attempts %d", resp.StatusCode, err, n.Load())
	}
}

func TestLeavesUnsafeRequestsAndClientErrors(t *testing.T) {
	srv, n := statuses(t, nil, 503, 200)
	resp, err := fast().Post(srv.URL, "text/plain", strings.NewReader("x"))
	if err != nil || resp.StatusCode != 503 || n.Load() != 1 {
		t.Fatalf("POST: status %d, err %v, attempts %d", resp.StatusCode, err, n.Load())
	}
	srv, n = statuses(t, nil, 404, 200)
	if resp, _ := fast().Get(srv.URL); resp.StatusCode != 404 || n.Load() != 1 {
		t.Fatalf("404: status %d, attempts %d", resp.StatusCode, n.Load())
	}
}

func TestRetryAfter(t *testing.T) {
	// Short: waited out and retried.
	srv, n := statuses(t, http.Header{"Retry-After": {"0"}}, 429, 200)
	if resp, _ := fast().Get(srv.URL); resp.StatusCode != 200 || n.Load() != 2 {
		t.Fatalf("short Retry-After: status %d, attempts %d", resp.StatusCode, n.Load())
	}
	// Longer than we'd wait: handed back.
	srv, n = statuses(t, http.Header{"Retry-After": {"3600"}}, 429, 200)
	if resp, _ := fast().Get(srv.URL); resp.StatusCode != 429 || n.Load() != 1 {
		t.Fatalf("long Retry-After: status %d, attempts %d", resp.StatusCode, n.Load())
	}
}

func TestRateLimitReset(t *testing.T) {
	// atproto's reset is a Unix time: now means go again.
	now := strconv.FormatInt(time.Now().Unix(), 10)
	srv, n := statuses(t, http.Header{"Ratelimit-Reset": {now}}, 429, 200)
	if resp, _ := fast().Get(srv.URL); resp.StatusCode != 200 || n.Load() != 2 {
		t.Fatalf("reset now: status %d, attempts %d", resp.StatusCode, n.Load())
	}
	later := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	srv, n = statuses(t, http.Header{"Ratelimit-Reset": {later}}, 429, 200)
	if resp, _ := fast().Get(srv.URL); resp.StatusCode != 429 || n.Load() != 1 {
		t.Fatalf("reset in an hour: status %d, attempts %d", resp.StatusCode, n.Load())
	}
}

func TestStopsWhenCancelled(t *testing.T) {
	srv, n := statuses(t, nil, 503)
	c := NewClient(WithBackoff(time.Hour, time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	start := time.Now()
	if _, err := c.Do(req); err == nil || time.Since(start) > 5*time.Second || n.Load() != 1 {
		t.Fatalf("err %v after %v, attempts %d", err, time.Since(start), n.Load())
	}
}

func TestUserAgent(t *testing.T) {
	srv, _ := statuses(t, nil, 200)
	resp, _ := fast(WithUserAgent("leadsheet/1")).Get(srv.URL)
	if got := resp.Header.Get("X-UA"); got != "leadsheet/1" {
		t.Fatalf("user agent = %q", got)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("User-Agent", "mine")
	resp, _ = fast(WithUserAgent("leadsheet/1")).Do(req)
	if got := resp.Header.Get("X-UA"); got != "mine" {
		t.Fatalf("caller's user agent = %q", got)
	}
}

func TestBackoffGrowsWithinBounds(t *testing.T) {
	r := &retrier{o: options([]Option{WithBackoff(100*time.Millisecond, time.Second)})}
	for attempt, want := range []time.Duration{100, 200, 400, 800, 1000, 1000} {
		want *= time.Millisecond
		for range 50 {
			if d := r.backoff(attempt); d < want/2 || d > want {
				t.Fatalf("attempt %d: %v outside [%v, %v]", attempt, d, want/2, want)
			}
		}
	}
}
