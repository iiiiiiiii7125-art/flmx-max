package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/go-gost/core/logger"
)

// R2-P1-F10 regression: the TUN handler's fourteen per-packet Warn
// points are aggregated through eventlog. A flood of malformed
// packets must produce exactly one summary line per event kind
// (emitted by the transport's final Flush), carrying the full
// count — never one line per packet.

type tunCaptureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *tunCaptureLogger) record(format string, args ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *tunCaptureLogger) WithFields(map[string]any) logger.Logger { return l }
func (l *tunCaptureLogger) Trace(args ...any)                       {}
func (l *tunCaptureLogger) Tracef(string, ...any)                   {}
func (l *tunCaptureLogger) Debug(args ...any)                       {}
func (l *tunCaptureLogger) Debugf(string, ...any)                   {}
func (l *tunCaptureLogger) Info(args ...any)                        {}
func (l *tunCaptureLogger) Infof(string, ...any)                    {}
func (l *tunCaptureLogger) Warn(args ...any)                        { l.record("%v", args...) }
func (l *tunCaptureLogger) Warnf(f string, args ...any)             { l.record(f, args...) }
func (l *tunCaptureLogger) Error(args ...any)                       {}
func (l *tunCaptureLogger) Errorf(string, ...any)                   {}
func (l *tunCaptureLogger) Fatal(args ...any)                       {}
func (l *tunCaptureLogger) Fatalf(string, ...any)                   {}
func (l *tunCaptureLogger) GetLevel() logger.LogLevel               { return logger.TraceLevel }
func (l *tunCaptureLogger) IsLevelEnabled(logger.LogLevel) bool     { return false }

func (l *tunCaptureLogger) warnLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

// scriptedTun feeds the given packets from Read, then io.EOF.
type scriptedTun struct {
	packets [][]byte
	idx     int
}

func (s *scriptedTun) Read(b []byte) (int, error) {
	if s.idx >= len(s.packets) {
		return 0, io.EOF
	}
	p := s.packets[s.idx]
	s.idx++
	return copy(b, p), nil
}

func (s *scriptedTun) Write(b []byte) (int, error) { return len(b), nil }

func TestTransportClientBadHeaderFloodAggregates(t *testing.T) {
	const flood = 1000
	// 0x40: IPv4 version nibble set, far below the minimum header
	// length, so ipv4.ParseHeader fails for every packet.
	bad := []byte{0x40, 0x00, 0x00, 0x00}
	tun := &scriptedTun{}
	for i := 0; i < flood; i++ {
		tun.packets = append(tun.packets, bad)
	}

	log := &tunCaptureLogger{}
	h := &tunHandler{}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	_ = h.transportClient(context.Background(), tun, c1, log)

	lines := log.warnLines()
	if len(lines) != 1 {
		t.Fatalf("warn lines = %d, want exactly 1 aggregated line for %d bad packets: %v", len(lines), flood, lines)
	}
	want := fmt.Sprintf("%d packet(s) discarded (bad IP header)", flood)
	if !strings.Contains(lines[0], want) || !strings.Contains(lines[0], fmt.Sprintf("%d total", flood)) {
		t.Fatalf("summary line %q does not carry %q and the total", lines[0], want)
	}
}

func TestTunPacketEventsKindsAggregateSeparately(t *testing.T) {
	log := &tunCaptureLogger{}
	events := newTunPacketEvents(log)

	for i := 0; i < 5; i++ {
		events.unknown.Note()
	}
	for i := 0; i < 2; i++ {
		events.keepalive.NoteDetail("to 10.0.0.1:1194: write udp: broken pipe")
	}
	for i := 0; i < 3; i++ {
		events.badHeader.NoteErr(errors.New("header too short"))
	}
	events.Flush()

	lines := log.warnLines()
	if len(lines) != 3 {
		t.Fatalf("warn lines = %d, want 3 (one per kind): %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"5 unknown packet(s) discarded, 5 total",
		"2 keepalive failure(s)",
		"3 packet(s) discarded (bad IP header)",
		"header too short",
		"3 total",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summaries missing %q: %v", want, lines)
		}
	}

	// A second Flush with nothing pending emits nothing further.
	events.Flush()
	if got := len(log.warnLines()); got != 3 {
		t.Fatalf("after empty Flush: warn lines = %d, want 3", got)
	}
}
