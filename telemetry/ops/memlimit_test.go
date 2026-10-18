package ops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHostMemoryLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	meminfo := write("meminfo", "MemTotal:        3071280 kB\nMemFree:          100 kB\n")
	missing := filepath.Join(dir, "missing")

	cases := []struct {
		name     string
		v2, v1   string
		meminfo  string
		expected int64
	}{
		{"guest RAM when the cgroup has no limit", write("v2max", "max\n"), missing, meminfo, 3071280 << 10},
		{"cgroup v2 limit under RAM", write("v2", "1073741824\n"), missing, meminfo, 1 << 30},
		{"cgroup v1 unlimited", missing, write("v1", "9223372036854771712\n"), meminfo, 3071280 << 10},
		{"nothing readable", missing, missing, missing, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hostMemoryLimit(c.v2, c.v1, c.meminfo); got != c.expected {
				t.Fatalf("got %d, want %d", got, c.expected)
			}
		})
	}
}

func TestSetMemoryLimitFromHostLeavesEnv(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "100MiB")
	if got := SetMemoryLimitFromHost(0.8); got != 0 {
		t.Fatalf("set %d with GOMEMLIMIT in the environment", got)
	}
}
