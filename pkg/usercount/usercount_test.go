package usercount

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func listReposServer(count int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repos := ""
		for i := 0; i < count; i++ {
			if i > 0 {
				repos += ","
			}
			repos += fmt.Sprintf(`{"did":"did:plc:x%d","head":"bafy","rev":"r","active":true}`, i)
		}
		fmt.Fprintf(w, `{"repos":[%s]}`, repos)
	}))
}

func TestGetUserCountSkipsHungHost(t *testing.T) {
	good := listReposServer(3)
	defer good.Close()

	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hung.Close()
	defer close(release)

	goodPDS := NewPDS(good.URL, 100)
	hungPDS := NewPDS(hung.URL, 100)
	hungPDS.UserCount = 7 // previous count is kept

	uc := &UserCount{
		PDSs:        []*PDS{goodPDS, hungPDS},
		HostTimeout: 200 * time.Millisecond,
		Concurrency: 1,
	}

	start := time.Now()
	total, err := uc.GetUserCount(context.Background())
	if err != nil {
		t.Fatalf("GetUserCount: %v", err)
	}
	if total != 10 {
		t.Fatalf("total = %d, want 10", total)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("refresh took %s with a hung host", elapsed)
	}
}
