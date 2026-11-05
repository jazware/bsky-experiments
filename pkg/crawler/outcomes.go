package crawler

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Outcome is the final result for one DID in a crawl.
type Outcome struct {
	DID         string
	PDS         string
	Result      string // success, or a failure category
	Records     int
	Collections int
	CrawledAt   time.Time
	Attempts    int
	Status      int    // HTTP status of the last attempt, if it got one
	XRPCError   string // the response's XRPC error name
	Message     string
}

// OutcomeLog appends one tab-separated line per DID:
// did, pds, result, records, collections, crawled_at_us, attempts, status,
// xrpc_error, message.
// Every DID a crawl is given ends up in it exactly once, so a run can be
// audited and its failures fed back with --dids-from.
type OutcomeLog struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	stop chan struct{}
	done chan struct{}
}

func OpenOutcomeLog(path string) (*OutcomeLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening outcome log: %w", err)
	}
	l := &OutcomeLog{f: f, w: bufio.NewWriterSize(f, 1<<20), stop: make(chan struct{}), done: make(chan struct{})}
	go l.flushLoop()
	return l, nil
}

func (l *OutcomeLog) flushLoop() {
	defer close(l.done)
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			l.mu.Lock()
			_ = l.w.Flush()
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}

func (l *OutcomeLog) Record(o Outcome) {
	if l == nil {
		return
	}
	var at int64
	if !o.CrawledAt.IsZero() {
		at = o.CrawledAt.UnixMicro()
	}
	l.mu.Lock()
	fmt.Fprintf(l.w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
		o.DID, o.PDS, o.Result, o.Records, o.Collections, at, o.Attempts, o.Status,
		clean(o.XRPCError, 100), clean(o.Message, 300))
	l.mu.Unlock()
}

func clean(s string, n int) string {
	s = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func (l *OutcomeLog) Close() error {
	if l == nil {
		return nil
	}
	close(l.stop)
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.w.Flush(); err != nil {
		return err
	}
	return l.f.Close()
}
