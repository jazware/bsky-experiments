package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHandler(t *testing.T) {
	reg := NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "ops_test_total", Help: "test"})
	reg.MustRegister(c)
	c.Add(3)
	h := Handler(reg)

	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	code, body := get("/metrics")
	if code != 200 || !strings.Contains(body, "ops_test_total 3") || !strings.Contains(body, "go_goroutines") {
		t.Fatalf("/metrics: %d\n%s", code, body)
	}
	if code, body := get("/debug/pprof/goroutine?debug=1"); code != 200 || !strings.Contains(body, "goroutine profile") {
		t.Fatalf("goroutine: %d %q", code, body)
	}
	if code, _ := get("/debug/pprof/heap"); code != 200 {
		t.Fatalf("heap: %d", code)
	}
	for _, p := range []string{"/", "/api/v1/config", "/debug/vars"} {
		if code, _ := get(p); code != 404 {
			t.Fatalf("%s: %d, want 404", p, code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics: %d", rec.Code)
	}
}

func TestServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr, err := Serve(ctx, "127.0.0.1:0", NewRegistry(), func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr.String() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "process_") && !strings.Contains(string(b), "go_") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if _, err := Serve(ctx, addr.String(), NewRegistry(), nil); err == nil {
		t.Fatal("a second listener on the same address should fail at once")
	}
}
