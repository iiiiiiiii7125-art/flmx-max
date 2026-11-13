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
	"github.com/go-gost/x/registry"
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

// freshConnlogRing installs a fresh default reporter for the test
// and returns its ring: the Serve hook enqueues through
// EnqueueConnEvent into exactly this ring (integration-unified
// data path — there is no second buffer).
func freshConnlogRing(t *testing.T) *connRing {
	t.Helper()
	connlogMu.Lock()
	connlogDefault = newConnlogReporter(connlogConfig{})
	rep := connlogDefault
	connlogMu.Unlock()
	t.Cleanup(func() {
		connlogMu.Lock()
		connlogDefault = nil
		connlogMu.Unlock()
	})
	return rep.ring
}

func waitConnEvents(t *testing.T, ring *connRing, want int) []ConnEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var got []ConnEvent
	for time.Now().Before(deadline) {
		got = append(got, ring.Drain()...)
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
func serveOneConn(t *testing.T, remote net.Addr, entry bool) (*connRing, *gateHandler, func()) {
	t.Helper()
	ring := freshConnlogRing(t)
	SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24003})
	h := &gateHandler{release: make(chan struct{})}
	opts := []Option{LoggerOption(xlogger.NewLogger())}
	if entry {
		opts = append(opts, ConnLogEntryOption(true, "fwd_entry_tcp"))
	}
	svc := NewService("fwd_entry_tcp", ln, h, opts...)
	go func() { _ = svc.Serve() }()

	server, client := pipeConnPair(remote)
	ln.conns <- server
	t.Cleanup(func() { _ = client.Close() })
	return ring, h, func() {
		close(h.release)
		_ = svc.Close()
	}
}

func TestConnLogTCPOpenClosePairing(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 54321}
	ring, h, finish := serveOneConn(t, remote, true)

	// While the handler is still blocked (connection alive), the
	// open event must already be queued — the "连接中" UI feeds
	// on open events alone.
	open := waitConnEvents(t, ring, 1)[0]
	if open.Type != ConnEventOpen {
		t.Fatalf("first event type = %q, want open", open.Type)
	}
	if open.EntryID != "fwd_entry_tcp" || open.EntryKind != ConnEntryForward {
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
	if got := ring.Len(); got != 0 {
		t.Fatalf("ring holds %d events while connection alive, want 0 extra", got)
	}

	finish()
	waitHandled(t, h, 1)
	cls := waitConnEvents(t, ring, 1)[0]
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID {
		t.Fatalf("close event = %+v, want close paired with conn %s", cls, open.ConnID)
	}
	if cls.Status != ConnStatusClosed {
		t.Fatalf("close status = %q, want closed", cls.Status)
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
	ring, h, finish := serveOneConn(t, remote, true)

	open := waitConnEvents(t, ring, 1)[0]
	if open.Network != "udp" || open.SessionKind != ConnSessionKindUDP {
		t.Fatalf("udp session classified as %q/%q, want udp/udp_session", open.Network, open.SessionKind)
	}
	if open.ClientIP != "198.51.100.9" || open.ClientPort != 40000 {
		t.Fatalf("client = %s:%d, want 198.51.100.9:40000", open.ClientIP, open.ClientPort)
	}

	finish()
	waitHandled(t, h, 1)
	cls := waitConnEvents(t, ring, 1)[0]
	if cls.Type != ConnEventClose || cls.ConnID != open.ConnID || cls.SessionKind != ConnSessionKindUDP {
		t.Fatalf("udp close event = %+v, want udp_session close paired with %s", cls, open.ConnID)
	}
}

func TestConnLogNonEntryServiceSilent(t *testing.T) {
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.8"), Port: 12345}
	ring, h, finish := serveOneConn(t, remote, false)
	// Prove the connection really flowed through Handle before
	// asserting the absence of events.
	waitHandled(t, h, 1)
	finish()
	// Give the goroutine a beat to (not) emit a close event.
	time.Sleep(50 * time.Millisecond)
	if got := ring.Len(); got != 0 {
		t.Fatalf("non-entry service produced %d conn events, want 0", got)
	}
}

func TestConnLogMasterSwitchOff(t *testing.T) {
	ring := freshConnlogRing(t)
	SetConnLogEnabled(false)
	defer SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24004})
	h := &gateHandler{}
	svc := NewService("fwd_entry_off", ln, h,
		LoggerOption(xlogger.NewLogger()), ConnLogEntryOption(true, "fwd_entry_off"))
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
	if got := ring.Len(); got != 0 {
		t.Fatalf("disabled master switch produced %d conn events, want 0", got)
	}
	_ = svc.Close()
}

func TestConnRingOverflowDropsOldest(t *testing.T) {
	ring := newConnRing(8)
	dropped := 0
	for i := 0; i < 15; i++ {
		if ring.Push(ConnEvent{Type: ConnEventOpen, ConnID: connIDForIndex(i)}) {
			dropped++
		}
	}
	if dropped != 7 {
		t.Fatalf("dropped-oldest signals = %d, want 7", dropped)
	}
	if got := ring.Len(); got != 8 {
		t.Fatalf("Len = %d, want capacity 8", got)
	}
	out := ring.Drain()
	if len(out) != 8 {
		t.Fatalf("Drain returned %d events, want 8", len(out))
	}
	if out[0].ConnID != connIDForIndex(7) {
		t.Fatalf("oldest surviving = %q, want %q (first 7 dropped)", out[0].ConnID, connIDForIndex(7))
	}
	if out[len(out)-1].ConnID != connIDForIndex(14) {
		t.Fatalf("newest = %q, want %q", out[len(out)-1].ConnID, connIDForIndex(14))
	}
	if got := ring.Len(); got != 0 {
		t.Fatalf("Len after drain = %d, want 0", got)
	}
}

func TestConnRingDrainFIFO(t *testing.T) {
	ring := newConnRing(8)
	for i := 0; i < 5; i++ {
		ring.Push(ConnEvent{Type: ConnEventOpen, ConnID: connIDForIndex(i)})
	}
	out := ring.Drain()
	if len(out) != 5 {
		t.Fatalf("Drain returned %d events, want 5", len(out))
	}
	for i, ev := range out {
		if ev.ConnID != connIDForIndex(i) {
			t.Fatalf("drain[%d] = %q, want %q (FIFO)", i, ev.ConnID, connIDForIndex(i))
		}
	}
}

func TestConnlogEnqueueOverflowCountsDropped(t *testing.T) {
	r := newConnlogReporter(connlogConfig{})
	total := defaultConnlogRingCapacity + 1
	for i := 0; i < total; i++ {
		r.Enqueue(ConnEvent{Type: ConnEventOpen, ConnID: connIDForIndex(i)})
	}
	if got := r.DroppedPending(); got != 1 {
		t.Fatalf("DroppedPending = %d, want 1 (ring overflow must be counted)", got)
	}
	if got := r.ring.Len(); got != defaultConnlogRingCapacity {
		t.Fatalf("ring Len = %d, want capacity %d", got, defaultConnlogRingCapacity)
	}
}

func connIDForIndex(i int) string {
	return fmt.Sprintf("conn-%d", i)
}

// TestCountConnLogEntryServices pins the lane-C live-mark count:
// only services registered with the connlog entry mark count, and
// unregistering a marked service drops the count again. The count
// is asserted as a delta so unrelated registrations elsewhere in
// the package cannot skew it; the source of truth is the live
// registry, never a config file.
func TestCountConnLogEntryServices(t *testing.T) {
	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24004})
	h := &gateHandler{}
	mk := func(name string, entry bool) {
		t.Helper()
		opts := []Option{LoggerOption(xlogger.NewLogger())}
		if entry {
			opts = append(opts, ConnLogEntryOption(true, name))
		}
		svc := NewService(name, ln, h, opts...)
		if err := registry.ServiceRegistry().Register(name, svc); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
		t.Cleanup(func() { registry.ServiceRegistry().Unregister(name) })
	}

	suffix := time.Now().UnixNano()
	marked := fmt.Sprintf("connlog-count-marked-%d", suffix)
	plain := fmt.Sprintf("connlog-count-plain-%d", suffix)

	base := CountConnLogEntryServices()
	mk(marked, true)
	if got := CountConnLogEntryServices(); got != base+1 {
		t.Fatalf("after registering marked service: count = %d, want %d", got, base+1)
	}
	mk(plain, false)
	if got := CountConnLogEntryServices(); got != base+1 {
		t.Fatalf("after registering unmarked service: count = %d, want %d (unmarked must not count)", got, base+1)
	}
	registry.ServiceRegistry().Unregister(marked)
	if got := CountConnLogEntryServices(); got != base {
		t.Fatalf("after unregistering marked service: count = %d, want %d", got, base)
	}
}
