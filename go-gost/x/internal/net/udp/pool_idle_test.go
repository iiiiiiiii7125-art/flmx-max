package udp

import (
	"net"
	"sync"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// 乙类 regression (incident INCIDENT-idle-teardown §3.2): the
// session pool's idle judgement used to count inbound arrivals
// only, so a purely outbound UDP flow (a client that sent one
// request and only receives) was reaped after one TTL despite
// live traffic, and a purely inbound-fed session on the bind side
// had the mirror problem. conn.WriteTo now clears the idle flag
// on success, so activity in EITHER direction keeps the session.
//
// The tests run on an in-memory PacketConn: the behaviour under
// test is the pool/conn idle-flag semantics, not the wire.

type poolFakePacketConn struct {
	local  net.Addr
	closed chan struct{}
	once   sync.Once
}

func newPoolFakePacketConn() *poolFakePacketConn {
	return &poolFakePacketConn{
		local:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10000},
		closed: make(chan struct{}),
	}
}

func (f *poolFakePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	<-f.closed
	return 0, nil, net.ErrClosed
}

func (f *poolFakePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-f.closed:
		return 0, net.ErrClosed
	default:
		return len(b), nil
	}
}

func (f *poolFakePacketConn) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *poolFakePacketConn) LocalAddr() net.Addr              { return f.local }
func (f *poolFakePacketConn) SetDeadline(time.Time) error      { return nil }
func (f *poolFakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (f *poolFakePacketConn) SetWriteDeadline(time.Time) error { return nil }

var (
	poolTestLocal  = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10000}
	poolTestRemote = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20000}
)

// keepAliveFor feeds the session with the given activity every
// 20ms for the given span, then stops.
func keepAliveFor(c *conn, outbound bool, span time.Duration) {
	deadline := time.Now().Add(span)
	for time.Now().Before(deadline) {
		if outbound {
			_, _ = c.WriteTo([]byte("x"), poolTestRemote)
		} else {
			_ = c.WriteQueue([]byte("x"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertSessionReaped(t *testing.T, p *connPool, c *conn) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := p.Get("k"); !ok && c.isClosed() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session not reaped after activity stopped")
}

func TestPoolOutboundOnlyActivityKeepsSession(t *testing.T) {
	p := newConnPool(60 * time.Millisecond).WithLogger(xlogger.Nop())
	defer p.Close()

	c := newConn(newPoolFakePacketConn(), poolTestLocal, poolTestRemote, 1024, true)
	p.Set("k", c)

	// Outbound only — no inbound packet ever arrives — for ~8 TTL
	// scan cycles. The session must survive every scan.
	keepAliveFor(c, true, 480*time.Millisecond)
	if _, ok := p.Get("k"); !ok {
		t.Fatal("outbound-only session was reaped by the pool despite live traffic")
	}
	if c.isClosed() {
		t.Fatal("outbound-only session conn was closed despite live traffic")
	}

	// Once the traffic stops the pool must still reap it: the fix
	// widens what counts as activity, it does not disable reaping.
	assertSessionReaped(t, p, c)
}

func TestPoolInboundOnlyActivityKeepsSession(t *testing.T) {
	p := newConnPool(60 * time.Millisecond).WithLogger(xlogger.Nop())
	defer p.Close()

	c := newConn(newPoolFakePacketConn(), poolTestLocal, poolTestRemote, 1024, true)
	p.Set("k", c)

	// Inbound only (the pre-fix behaviour, kept as the control).
	keepAliveFor(c, false, 480*time.Millisecond)
	if _, ok := p.Get("k"); !ok {
		t.Fatal("inbound-only session was reaped by the pool despite live traffic")
	}
	if c.isClosed() {
		t.Fatal("inbound-only session conn was closed despite live traffic")
	}

	assertSessionReaped(t, p, c)
}
