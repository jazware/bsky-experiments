package search

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jazware/bsky-experiments/pkg/usercount"
)

// A PDS that never answers must not delay NewSearchService (and so the HTTP
// listener): stats are filled in the background.
func TestStartupDoesNotWaitForHungPDS(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)

	uc := &usercount.UserCount{
		PDSs:        []*usercount.PDS{usercount.NewPDS(hung.URL, 100)},
		HostTimeout: time.Minute,
	}

	start := time.Now()
	ss, err := NewSearchService(context.Background(), slog.Default(), nil, uc, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("NewSearchService blocked for %s", time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ss.StartStatsRefreshWorker(ctx)
		close(done)
	}()

	if _, err := ss.GetStats(context.Background()); !errors.Is(err, ErrStatsNotReady) {
		t.Fatalf("GetStats err = %v, want ErrStatsNotReady", err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop on cancel")
	}
}
