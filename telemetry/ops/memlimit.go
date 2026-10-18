package ops

import (
	"bufio"
	"bytes"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// SetMemoryLimitFromHost sets Go's soft memory limit (GOMEMLIMIT) to
// fraction of the memory the process can have: its cgroup's limit, or the
// machine's RAM (a microVM guest's) when the cgroup has none. A GOMEMLIMIT in
// the environment wins and is left alone. It returns the limit it set, or 0.
//
// The GC then works harder as the heap nears the limit instead of letting it
// grow to twice the live heap, which is what turns a burst into an OOM kill.
// It can't save a live heap bigger than the box: keep fraction well under 1.
func SetMemoryLimitFromHost(fraction float64) int64 {
	if os.Getenv("GOMEMLIMIT") != "" || fraction <= 0 || fraction > 1 {
		return 0
	}
	total := hostMemoryLimit("/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes", "/proc/meminfo")
	if total <= 0 {
		return 0
	}
	limit := int64(float64(total) * fraction)
	debug.SetMemoryLimit(limit)
	return limit
}

// hostMemoryLimit is the smallest of the cgroup v2 limit, the cgroup v1
// limit and MemTotal that is set, in bytes; 0 when none is.
func hostMemoryLimit(cgroupV2, cgroupV1, meminfo string) int64 {
	var limit int64
	consider := func(n int64) {
		if n > 0 && (limit == 0 || n < limit) {
			limit = n
		}
	}
	consider(readLimitFile(cgroupV2))
	consider(readLimitFile(cgroupV1))
	consider(memTotal(meminfo))
	return limit
}

// readLimitFile reads a cgroup limit: "max" (v2) and v1's near-MaxInt64
// "unlimited" count as none.
func readLimitFile(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n <= 0 || n >= 1<<62 {
		return 0
	}
	return n
}

func memTotal(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0
			}
			return kb << 10
		}
	}
	return 0
}
