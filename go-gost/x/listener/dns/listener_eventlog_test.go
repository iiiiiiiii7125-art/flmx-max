package dns

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
)

// captureLogger records Warn lines for the flood test.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *captureLogger) Trace(args ...any)                       {}
func (l *captureLogger) Tracef(string, ...any)                   {}
func (l *captureLogger) Debug(args ...any)                       {}
func (l *captureLogger) Debugf(string, ...any)                   {}
func (l *captureLogger) Info(args ...any)                        {}
func (l *captureLogger) Infof(string, ...any)                    {}
func (l *captureLogger) Warn(args ...any)                        {}
func (l *captureLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, format)
	l.mu.Unlock()
}
func (l *captureLogger) Error(args ...any)                   {}
func (l *captureLogger) Errorf(string, ...any)               {}
func (l *captureLogger) Fatal(args ...any)                   {}
func (l *captureLogger) Fatalf(string, ...any)               {}
func (l *captureLogger) GetLevel() logger.LogLevel           { return logger.InfoLevel }
func (l *captureLogger) IsLevelEnabled(logger.LogLevel) bool { return true }

func (l *captureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

type fakeResponseWriter struct{ raddr net.Addr }

func (w *fakeResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *fakeResponseWriter) RemoteAddr() net.Addr        { return w.raddr }

// TestServeQueueFullFloodAggregated drives the converted admission
// point with a flood of requests against a queue of depth 1: the
// pre-aggregation code emitted one Warn line per discarded
// request; now the flood must produce no lines inside the window
// and exactly one summary line on flush, carrying the full count.
//
// serve() blocks in conn.Wait() after a successful enqueue until
// the queued connection is consumed and closed (in production the
// service accept loop does that), so the test first waits until
// the initial request provably occupies the single slot; only then
// is every flood request deterministically discarded, and the
// queued connection is drained at the end to release the waiter.
func TestServeQueueFullFloodAggregated(t *testing.T) {
	cl := &captureLogger{}
	events := eventlog.New(cl, eventlog.DefaultInterval)
	l := &dnsListener{
		addr:   &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53},
		cqueue: make(chan net.Conn, 1),
		events: events,
		queueFull: events.DetailCounter(
			"dns listener: %d connection(s) discarded (connection queue is full), last client %s, %d total"),
		reqErrs: events.DetailCounter(
			"dns listener: %d request error(s), last: %s, %d total"),
	}

	w := &fakeResponseWriter{raddr: &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51000}}

	// First request occupies the single queue slot; serve blocks in
	// Wait until the queued connection is consumed and closed (in
	// production the service accept loop does that).
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = l.serve(w, []byte{0})
	}()

	// Wait until the first request provably holds the slot, so every
	// flood request below is deterministically discarded.
	deadline := time.Now().Add(10 * time.Second)
	for len(l.cqueue) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first request did not occupy the queue slot")
		}
		time.Sleep(time.Millisecond)
	}

	// Flood: every request is discarded at admission.
	const flood = 500
	for i := 0; i < flood; i++ {
		if err := l.serve(w, []byte{0}); err == nil {
			t.Fatalf("flood request %d: serve succeeded, want queue-full error", i)
		}
	}

	if got := cl.count(); got != 0 {
		t.Fatalf("within window: got %d log lines, want 0", got)
	}
	if got := l.queueFull.Total(); got != flood {
		t.Fatalf("queue-full total = %d, want %d", got, flood)
	}

	// Close-time flush: exactly one summary line for the flood.
	l.events.Flush()
	if got := cl.count(); got != 1 {
		t.Fatalf("after flush: got %d log lines, want 1", got)
	}

	// Drain: receiving the queued connection is its acceptance;
	// closing it releases the first serve's Wait, mirroring what
	// the service accept loop does in production.
	conn := <-l.cqueue
	if err := conn.Close(); err != nil {
		t.Fatalf("close queued conn: %v", err)
	}
	<-served
}
