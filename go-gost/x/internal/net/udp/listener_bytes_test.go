package udp

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/internal/util/eventlog"
	xlogger "github.com/go-gost/x/logger"
)

// captureLogger records Warn/Warnf lines; every other method is
// the nop logger's.
type captureLogger struct {
	logger.Logger
	mu    sync.Mutex
	lines []string
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{Logger: xlogger.Nop()}
}

func (l *captureLogger) Warn(args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprint(args...))
	l.mu.Unlock()
}

func (l *captureLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *captureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *captureLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// P1-02 regression: the session receive queue must be capped by
// BYTES, not only by packet count. Flooding a stalled consumer
// with maximum-size datagrams previously allowed 1024 x 65535B
// ≈ 64MiB to accumulate in one session; the byte budget must stop
// admission at DefaultReadQueueBytes while the (much larger)
// packet-count depth never binds.
func TestSessionQueueByteCapBoundsFlood(t *testing.T) {
	const dgram = 65536 // 64KiB, the maximum UDP datagram class

	laddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}
	raddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002}
	// Packet depth far above what the byte budget admits, so the
	// byte cap is provably the binding constraint.
	c := newConn(nil, laddr, raddr, 4096, false)
	defer c.Close()

	payload := make([]byte, dgram)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	accepted := 0
	for i := 0; i < 2000; i++ {
		err := c.WriteQueue(payload)
		if err == nil {
			accepted++
			continue
		}
		if !errors.Is(err, errQueueFull) {
			t.Fatalf("WriteQueue #%d: unexpected error %v", i, err)
		}
	}

	if maxByBytes := DefaultReadQueueBytes / dgram; accepted != maxByBytes {
		t.Fatalf("accepted %d x %dB datagrams, want exactly %d (byte budget %d)",
			accepted, dgram, maxByBytes, DefaultReadQueueBytes)
	}
	if c.qBytes != accepted*dgram {
		t.Fatalf("accounted queue bytes = %d, want %d", c.qBytes, accepted*dgram)
	}

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if limit := int64(DefaultReadQueueBytes) + 4<<20; growth > limit {
		t.Fatalf("live heap grew by %d bytes under a 2000x64KiB flood, want <= %d (byte cap + slack)",
			growth, limit)
	}

	// Dequeueing one datagram frees exactly its bytes, and once
	// the queue is drained the budget admits new datagrams again.
	bb, err := c.readDatagram()
	if err != nil {
		t.Fatalf("readDatagram: %v", err)
	}
	if len(bb) != dgram {
		t.Fatalf("dequeued %d bytes, want %d", len(bb), dgram)
	}
	if want := (accepted - 1) * dgram; c.qBytes != want {
		t.Fatalf("queue bytes after one dequeue = %d, want %d", c.qBytes, want)
	}
	for c.qBytes > 0 {
		if _, err := c.readDatagram(); err != nil {
			t.Fatalf("drain readDatagram: %v", err)
		}
	}
	if err := c.WriteQueue(payload); err != nil {
		t.Fatalf("WriteQueue after drain: %v (budget must free up on dequeue)", err)
	}
}

// P1-02, dual-cap semantics: with small datagrams the packet-count
// depth is hit long before the byte budget — whichever cap comes
// first drops.
func TestSessionQueuePacketCapBindsFirst(t *testing.T) {
	laddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}
	raddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002}
	c := newConn(nil, laddr, raddr, 4, false)
	defer c.Close()

	payload := make([]byte, 100)
	for i := 0; i < 4; i++ {
		if err := c.WriteQueue(payload); err != nil {
			t.Fatalf("WriteQueue #%d: %v", i, err)
		}
	}
	if err := c.WriteQueue(payload); !errors.Is(err, errQueueFull) {
		t.Fatalf("5th datagram: got %v, want errQueueFull (packet-count cap)", err)
	}
	if c.qBytes != 400 {
		t.Fatalf("accounted queue bytes = %d, want 400 (byte cap %d untouched)", c.qBytes, DefaultReadQueueBytes)
	}
}

// P1-03 regression (end to end): a datagram flood against a
// stalled consumer must be counted, and must NOT produce one log
// line per dropped packet. Summaries are emitted at most once per
// aggregation window, plus a final flush on listener close.
func TestListenerFloodDropsCountedAndLogCapped(t *testing.T) {
	requireLoopbackUDP(t)
	capLog := newCaptureLogger()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		Backlog:        4,
		ReadQueueSize:  1,
		ReadBufferSize: 2048,
		TTL:            30 * time.Second,
		Logger:         capLog,
	})

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("one")); err != nil {
		t.Fatalf("write: %v", err)
	}
	acceptc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			acceptc <- c
		}
	}()
	var sess net.Conn
	select {
	case sess = <-acceptc:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept timed out")
	}
	defer sess.Close()

	// Nobody reads the session: everything after the first
	// datagram must be dropped and counted.
	const flood = 200
	for i := 0; i < flood; i++ {
		if _, err := client.Write([]byte("x")); err != nil {
			t.Fatalf("flood write #%d: %v", i, err)
		}
	}

	dropped := ln.(*listener).dropped
	deadline := time.Now().Add(10 * time.Second)
	for {
		if dropped.Total() >= 150 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("counted %d dropped datagrams after 10s, want >= 150 of %d flooded", dropped.Total(), flood)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The aggregation window (30s) has not elapsed: not a single
	// per-packet line may have been written.
	if n := capLog.count(); n != 0 {
		t.Fatalf("flood produced %d log lines inside the aggregation window, want 0: %v", n, capLog.all())
	}

	// Close flushes one final summary line for the drop kind.
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lines := capLog.all()
	if len(lines) != 1 {
		t.Fatalf("close flushed %d summary lines, want exactly 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "discarded (session recv queue full)") {
		t.Fatalf("summary line = %q, want the recv-queue drop summary", lines[0])
	}
}

// P1-03 regression (aggregator): the summary cadence — first flush
// once the window has elapsed, silence inside the window, and a
// forced flush on close — with exact pending/total accounting for
// all three event kinds.
//
// The listener's counters now live on the shared eventlog
// Aggregator, so this test drives that Aggregator directly with a
// short (40ms) window instead of the deleted private eventLog: the
// window-open flush is driven by Flush() after a sleep past the
// window (window-gating itself is covered by the shared package's
// own TestWindowAggregation); the pending/total numbers and the
// summary wordings are asserted exactly as before.
func TestEventLogSummaryCadence(t *testing.T) {
	capLog := newCaptureLogger()
	events := eventlog.New(capLog, 40*time.Millisecond)
	dropped := events.Counter("udp listener: %d datagram(s) discarded (session recv queue full), %d total")
	sessDropped := events.Counter("udp listener: %d new session(s) discarded (connection queue full), %d total")
	readErrs := events.DetailCounter("udp listener: %d temporary read error(s), last: %s, %d total")
	// Pretend the previous window just expired.
	time.Sleep(50 * time.Millisecond)

	dropped.Note()
	if n := capLog.count(); n != 1 {
		t.Fatalf("first note past the window flushed %d lines, want 1", n)
	}
	for i := 0; i < 100; i++ {
		dropped.Note()
	}
	if n := capLog.count(); n != 1 {
		t.Fatalf("100 notes inside the window produced %d lines total, want 1", n)
	}

	// Force the next window open: the 100 pending drops flush as
	// one line, carrying pending and cumulative counts.
	time.Sleep(50 * time.Millisecond)
	events.Flush()
	lines := capLog.all()
	if len(lines) != 2 {
		t.Fatalf("window flush produced %d lines total, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[1], "100 datagram(s) discarded") || !strings.Contains(lines[1], "101 total") {
		t.Fatalf("second summary = %q, want 100 pending / 101 total", lines[1])
	}

	sessDropped.Note()
	readErrs.NoteErr(errors.New("boom"))
	if n := capLog.count(); n != 2 {
		t.Fatalf("notes inside the window produced %d lines total, want 2", n)
	}
	events.Flush()
	lines = capLog.all()
	if len(lines) != 4 {
		t.Fatalf("close flush produced %d lines total, want 4: %v", len(lines), lines)
	}
	joined := strings.Join(lines[2:], "\n")
	if !strings.Contains(joined, "1 new session(s) discarded") {
		t.Fatalf("close flush missing session-drop summary: %q", joined)
	}
	if !strings.Contains(joined, "1 temporary read error(s), last: boom") {
		t.Fatalf("close flush missing read-error summary with last error: %q", joined)
	}

	if d, s, r := dropped.Total(), sessDropped.Total(), readErrs.Total(); d != 101 || s != 1 || r != 1 {
		t.Fatalf("totals = (%d, %d, %d), want (101, 1, 1)", d, s, r)
	}
}
