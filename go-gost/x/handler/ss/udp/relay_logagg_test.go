package ss

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// F11 regression: relayPacket logged one Warn per dropped packet
// (four call points, both directions). The drops must now be
// aggregated through eventlog like udp.Relay's: at most one
// summary line per kind per association, carrying the total —
// while the drop semantics themselves stay identical.

type captureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *captureLogger) record(format string, args ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *captureLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *captureLogger) Trace(args ...any)                       {}
func (l *captureLogger) Tracef(string, ...any)                   {}
func (l *captureLogger) Debug(args ...any)                       {}
func (l *captureLogger) Debugf(string, ...any)                   {}
func (l *captureLogger) Info(args ...any)                        {}
func (l *captureLogger) Infof(string, ...any)                    {}
func (l *captureLogger) Warn(args ...any)                        { l.record("%v", args...) }
func (l *captureLogger) Warnf(f string, args ...any)             { l.record(f, args...) }
func (l *captureLogger) Error(args ...any)                       {}
func (l *captureLogger) Errorf(string, ...any)                   {}
func (l *captureLogger) Fatal(args ...any)                       {}
func (l *captureLogger) Fatalf(string, ...any)                   {}
func (l *captureLogger) GetLevel() logger.LogLevel               { return logger.TraceLevel }
func (l *captureLogger) IsLevelEnabled(logger.LogLevel) bool     { return true }

func (l *captureLogger) warnLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

type aggPacketConn struct {
	mu     sync.Mutex
	msgs   [][]byte
	addr   net.Addr
	writes int
	closed chan struct{}
	once   sync.Once
}

func newAggPacketConn(addr net.Addr, msgs ...string) *aggPacketConn {
	c := &aggPacketConn{addr: addr, closed: make(chan struct{})}
	for _, m := range msgs {
		c.msgs = append(c.msgs, []byte(m))
	}
	return c
}

func (c *aggPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	if len(c.msgs) > 0 {
		m := c.msgs[0]
		c.msgs = c.msgs[1:]
		c.mu.Unlock()
		return copy(b, m), c.addr, nil
	}
	c.mu.Unlock()
	<-c.closed
	return 0, nil, net.ErrClosed
}

func (c *aggPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return len(b), nil
}

func (c *aggPacketConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func (c *aggPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *aggPacketConn) LocalAddr() net.Addr              { return ssFakeAddr("127.0.0.1:0") }
func (c *aggPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *aggPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *aggPacketConn) SetWriteDeadline(time.Time) error { return nil }

type blockAllBypass struct{ containsCalls atomic.Int64 }

func (b *blockAllBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	b.containsCalls.Add(1)
	return true
}
func (b *blockAllBypass) IsWhitelist() bool { return false }

type failPinnerBypass struct{ pinCalls atomic.Int64 }

func (b *failPinnerBypass) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (b *failPinnerBypass) IsWhitelist() bool { return false }
func (b *failPinnerBypass) PinDialTarget(context.Context, string, string) (string, error) {
	b.pinCalls.Add(1)
	return "", errors.New("pin denied")
}

func waitForCount(t *testing.T, counter *atomic.Int64, want int64, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for counter.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if counter.Load() != want {
		t.Fatalf("%s calls = %d, want %d", what, counter.Load(), want)
	}
}

func TestRelayPacketBypassDropsAreAggregated(t *testing.T) {
	bp := &blockAllBypass{}
	h := &ssuHandler{options: handler.Options{Bypass: bp}}
	log := &captureLogger{}

	peer := &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 53}
	pc1 := newAggPacketConn(peer, "a", "b", "c")
	pc2 := newAggPacketConn(peer)
	defer pc2.Close()

	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), pc1, pc2,
			&xrecorder.HandlerRecorderObject{}, log)
	}()

	// All three packets hit the bypass and are dropped...
	waitForCount(t, &bp.containsCalls, 3, "bypass Contains")
	if got := pc2.writeCount(); got != 0 {
		t.Fatalf("pc2 writes = %d, want 0 (bypass drops must not be forwarded)", got)
	}

	// ...then the association ends; the flush emits the summary.
	pc1.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket did not return after pc1 closed")
	}

	lines := log.warnLines()
	if len(lines) != 1 {
		t.Fatalf("warn lines = %v, want exactly one aggregated summary", lines)
	}
	if !strings.Contains(lines[0], "3 packet(s) dropped by bypass") ||
		!strings.Contains(lines[0], "3 total") {
		t.Fatalf("summary line = %q, want the aggregated bypass count", lines[0])
	}
}

func TestRelayPacketPinFailuresAreAggregated(t *testing.T) {
	bp := &failPinnerBypass{}
	h := &ssuHandler{options: handler.Options{Bypass: bp}}
	log := &captureLogger{}

	peer := &net.UDPAddr{IP: net.ParseIP("203.0.113.8"), Port: 53}
	pc1 := newAggPacketConn(peer, "x", "y")
	pc2 := newAggPacketConn(peer)
	defer pc2.Close()

	done := make(chan error, 1)
	go func() {
		done <- h.relayPacket(context.Background(), pc1, pc2,
			&xrecorder.HandlerRecorderObject{}, log)
	}()

	// Both packets fail their pin and are dropped fail-closed.
	waitForCount(t, &bp.pinCalls, 2, "PinDialTarget")
	if got := pc2.writeCount(); got != 0 {
		t.Fatalf("pc2 writes = %d, want 0 (pin failures must drop)", got)
	}

	pc1.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("relayPacket did not return after pc1 closed")
	}

	lines := log.warnLines()
	if len(lines) != 1 {
		t.Fatalf("warn lines = %v, want exactly one aggregated summary", lines)
	}
	if !strings.Contains(lines[0], "2 bypass pin failure(s)") ||
		!strings.Contains(lines[0], "pin denied") {
		t.Fatalf("summary line = %q, want the aggregated pin failure count and last error", lines[0])
	}
}
