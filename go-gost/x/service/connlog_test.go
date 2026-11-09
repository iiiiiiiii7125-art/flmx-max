package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/metadata"
	xlogger "github.com/go-gost/x/logger"
)

// queueListener hands the test's prepared conns to Serve one by
// one; Close unblocks Accept with ErrClosed.
type queueListener struct {
	conns     chan net.Conn
	addr      net.Addr
	closeOnce sync.Once
}

func newQueueListener(addr net.Addr) *queueListener {
	return &queueListener{conns: make(chan net.Conn, 4), addr: addr}
}

func (l *queueListener) Init(metadata.Metadata) error { return nil }

func (l *queueListener) Accept() (net.Conn, error) {
	c, ok := <-l.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *queueListener) Addr() net.Addr { return l.addr }

func (l *queueListener) Close() error {
	l.closeOnce.Do(func() { close(l.conns) })
	return nil
}

// gateHandler blocks in Handle until released (or returns at once
// when release is nil), standing in for a live forwarded
// connection whose Handle return is the close point.
type gateHandler struct {
	release chan struct{}
	handled atomic.Int32
}

func (h *gateHandler) Init(metadata.Metadata) error { return nil }

func (h *gateHandler) Handle(_ context.Context, _ net.Conn, _ ...handler.HandleOption) error {
	h.handled.Add(1)
	if h.release != nil {
		<-h.release
	}
	return nil
}

// addrConn overrides the addresses of a net.Pipe end so Serve sees
// realistic TCP/UDP peer addresses.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

func pipeConnPair(remote net.Addr) (server net.Conn, client net.Conn) {
	s, c := net.Pipe()
	return &addrConn{Conn: s, remote: remote}, c
}

func waitHandled(t *testing.T, h *gateHandler, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.handled.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("handler ran %d times, want %d", h.handled.Load(), want)
}

func waitConnEvents(t *testing.T, buf *ConnLogBuffer, want int) []ConnEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []ConnEvent
	for time.Now().Before(deadline) {
		got = append(got, buf.Drain(0)...)
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d conn events, got %d: %+v", want, len(got), got)
	return nil
}

// serveOneConn starts an entry-marked (or not) service, feeds it a
// single connection, and returns the pieces the test asserts on.
func serveOneConn(t *testing.T, remote net.Addr, entry bool) (*ConnLogBuffer, *gateHandler, func()) {
	t.Helper()
	buf := GetConnLogBuffer()
	buf.reset()
	SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24003})
	h := &gateHandler{release: make(chan struct{})}
	opts := []Option{LoggerOption(xlogger.NewLogger())}
	if entry {
		opts = append(opts, ConnLogEntryOption(true))
	}
	svc := NewService("fwd_entry_tcp", ln, h, opts...)
	go func() { _ = svc.Serve() }()

	server, client := pipeConnPair(remote)
	ln.conns <- server
	t.Cleanup(func() { _ = client.Close() })
	return buf, h, func() {
		close(h.release)
		_ = svc.Close()
	}
}

func TestConnLogTCPOpenClosePairing(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 54321}
	buf, h, finish := serveOneConn(t, remote, true)

	// While the handler is still blocked (connection alive), the
	// open event must already be buffered — the "连接中" UI feeds
	// on open events alone.
	open := waitConnEvents(t, buf, 1)[0]
	if open.Type != ConnEventOpen {
		t.Fatalf("first event type = %q, want open", open.Type)
	}
	if open.EntryID != "fwd_entry_tcp" || open.EntryKind != ConnEntryKindForward {
		t.Fatalf("entry = %q/%q, want fwd_entry_tcp/forward", open.EntryID, open.EntryKind)
	}
	if open.ClientIP != "203.0.113.7" || open.ClientPort != 54321 {
		t.Fatalf("client = %s:%d, want 203.0.113.7:54321", open.ClientIP, open.ClientPort)
	}
	if open.Network != "tcp" || open.SessionKind != ConnSessionKindTCP {
		t.Fatalf("network/kind = %q/%q, want tcp/tcp_conn", open.Network, open.SessionKind)
	}
	if open.ConnID == "" || open.StartMs <= 0 || open.EndMs != 0 {
		t.Fatalf("open event fields wrong: %+v", open)
	}
	if got := buf.Len(); got != 0 {
		t.Fatalf("buffer holds %d events while connection alive, want 0 extra", got)
	}

	finish()
	waitHandled(t, h, 1)
	cls := waitConnEvents(t, buf, 1)[0]
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID {
		t.Fatalf("close event = %+v, want close paired with conn %s", cls, open.ConnID)
	}
	if cls.StartMs != open.StartMs || cls.EndMs < cls.StartMs {
		t.Fatalf("close times start=%d end=%d, want start=%d and end>=start", cls.StartMs, cls.EndMs, open.StartMs)
	}
	if cls.ClientIP != open.ClientIP || cls.SessionKind != ConnSessionKindTCP {
		t.Fatalf("close event lost descriptive fields: %+v", cls)
	}
}

func TestConnLogUDPSessionPairing(t *testing.T) {
	remote := &net.UDPAddr{IP: net.ParseIP("198.51.100.9"), Port: 40000}
	buf, h, finish := serveOneConn(t, remote, true)

	open := waitConnEvents(t, buf, 1)[0]
	if open.Network != "udp" || open.SessionKind != ConnSessionKindUDP {
		t.Fatalf("udp session classified as %q/%q, want udp/udp_session", open.Network, open.SessionKind)
	}
	if open.ClientIP != "198.51.100.9" || open.ClientPort != 40000 {
		t.Fatalf("client = %s:%d, want 198.51.100.9:40000", open.ClientIP, open.ClientPort)
	}

	finish()
	waitHandled(t, h, 1)
	cls := waitConnEvents(t, buf, 1)[0]
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID || cls.SessionKind != ConnSessionKindUDP {
		t.Fatalf("udp close event = %+v, want udp_session close paired with %s", cls, open.ConnID)
	}
}

func TestConnLogNonEntryServiceSilent(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.8"), Port: 12345}
	buf, h, finish := serveOneConn(t, remote, false)
	// Prove the connection really flowed through Handle before
	// asserting the absence of events.
	waitHandled(t, h, 1)
	finish()
	// Give the goroutine a beat to (not) emit a close event.
	time.Sleep(50 * time.Millisecond)
	if got := buf.Len(); got != 0 {
		t.Fatalf("non-entry service produced %d conn events, want 0", got)
	}
}

func TestConnLogMasterSwitchOff(t *testing.T) {
	buf := GetConnLogBuffer()
	buf.reset()
	SetConnLogEnabled(false)
	defer SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24004})
	h := &gateHandler{}
	svc := NewService("fwd_entry_off", ln, h,
		LoggerOption(xlogger.NewLogger()), ConnLogEntryOption(true))
	go func() { _ = svc.Serve() }()
	server, client := pipeConnPair(&net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 2222})
	defer func() { _ = client.Close() }()
	ln.conns <- server

	deadline := time.Now().Add(2 * time.Second)
	for h.handled.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.handled.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", h.handled.Load())
	}
	time.Sleep(50 * time.Millisecond)
	if got := buf.Len(); got != 0 {
		t.Fatalf("disabled master switch produced %d conn events, want 0", got)
	}
	_ = svc.Close()
}

func TestConnLogBufferOverflowDropsOldest(t *testing.T) {
	buf := NewConnLogBuffer()
	total := ConnLogBufferCapacity + 7
	for i := 0; i < total; i++ {
		buf.Push(ConnEvent{Type: ConnEventOpen, ConnID: connIDForIndex(i)})
	}
	if got := buf.Len(); got != ConnLogBufferCapacity {
		t.Fatalf("Len = %d, want capacity %d", got, ConnLogBufferCapacity)
	}
	if got := buf.Dropped(); got != 7 {
		t.Fatalf("Dropped = %d, want 7", got)
	}
	out := buf.Drain(0)
	if len(out) != ConnLogBufferCapacity {
		t.Fatalf("Drain returned %d events, want %d", len(out), ConnLogBufferCapacity)
	}
	if out[0].ConnID != connIDForIndex(7) {
		t.Fatalf("oldest surviving = %q, want %q (first 7 dropped)", out[0].ConnID, connIDForIndex(7))
	}
	if out[len(out)-1].ConnID != connIDForIndex(total-1) {
		t.Fatalf("newest = %q, want %q", out[len(out)-1].ConnID, connIDForIndex(total-1))
	}
	if got := buf.Len(); got != 0 {
		t.Fatalf("Len after drain = %d, want 0", got)
	}
}

func TestConnLogBufferDrainPartialFIFO(t *testing.T) {
	buf := NewConnLogBuffer()
	for i := 0; i < 5; i++ {
		buf.Push(ConnEvent{Type: ConnEventOpen, ConnID: connIDForIndex(i)})
	}
	out := buf.Drain(2)
	if len(out) != 2 || out[0].ConnID != connIDForIndex(0) || out[1].ConnID != connIDForIndex(1) {
		t.Fatalf("partial drain = %+v, want first two in FIFO order", out)
	}
	if got := buf.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3", got)
	}
	if got := buf.Dropped(); got != 0 {
		t.Fatalf("Dropped = %d, want 0", got)
	}
}

func connIDForIndex(i int) string {
	return fmt.Sprintf("conn-%d", i)
}

func TestConnLogEntryFromMetadata(t *testing.T) {
	cases := []struct {
		name string
		md   map[string]any
		want bool
	}{
		{"nil", nil, false},
		{"empty", map[string]any{}, false},
		{"bool true", map[string]any{"connlogEntry": true}, true},
		{"bool false", map[string]any{"connlogEntry": false}, false},
		{"viper lowercased key", map[string]any{"connlogentry": true}, true},
		{"string true", map[string]any{"connlogEntry": "true"}, true},
		{"string 1", map[string]any{"connlogEntry": "1"}, true},
		{"string other", map[string]any{"connlogEntry": "yes"}, false},
		{"json number", map[string]any{"connlogEntry": float64(1)}, true},
		{"json zero", map[string]any{"connlogEntry": float64(0)}, false},
		{"unrelated key", map[string]any{"idleTimeout": 300}, false},
	}
	for _, tc := range cases {
		if got := ConnLogEntryFromMetadata(tc.md); got != tc.want {
			t.Errorf("%s: ConnLogEntryFromMetadata = %v, want %v", tc.name, got, tc.want)
		}
	}
}
