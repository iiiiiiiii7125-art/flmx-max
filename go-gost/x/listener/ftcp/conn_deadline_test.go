package ftcp

import (
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// fakePacketConn is a net.PacketConn test double that reproduces the
// shared-socket semantics behind F-UDP-3: a deadline armed on the
// socket applies to every user of it, and once a past read/write
// deadline is armed, ReadFrom/WriteTo fail with a timeout error.
type fakePacketConn struct {
	mu        sync.Mutex
	rdeadline time.Time
	wdeadline time.Time
	deadlineN int // total Set*Deadline calls received
	inbox     chan fakePacket
	localAddr net.Addr
}

type fakePacket struct {
	b    []byte
	addr net.Addr
}

func newFakePacketConn() *fakePacketConn {
	return &fakePacketConn{
		inbox:     make(chan fakePacket, 16),
		localAddr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10000},
	}
}

func (f *fakePacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	f.mu.Lock()
	dl := f.rdeadline
	f.mu.Unlock()
	if !dl.IsZero() && time.Until(dl) <= 0 {
		return 0, nil, os.ErrDeadlineExceeded
	}
	p := <-f.inbox
	return copy(b, p.b), p.addr, nil
}

func (f *fakePacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	f.mu.Lock()
	dl := f.wdeadline
	f.mu.Unlock()
	if !dl.IsZero() && time.Until(dl) <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	return len(b), nil
}

func (f *fakePacketConn) Close() error { return nil }

func (f *fakePacketConn) LocalAddr() net.Addr { return f.localAddr }

func (f *fakePacketConn) SetDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rdeadline = t
	f.wdeadline = t
	f.deadlineN++
	return nil
}

func (f *fakePacketConn) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rdeadline = t
	f.deadlineN++
	return nil
}

func (f *fakePacketConn) SetWriteDeadline(t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wdeadline = t
	f.deadlineN++
	return nil
}

func (f *fakePacketConn) deadlineCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deadlineN
}

func newTestSession(pc net.PacketConn, port int) *serverConn {
	return newServerConn(pc,
		&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port},
		&serverConnConfig{ttl: time.Hour, qsize: 16})
}

// LB-P3-2 / F-UDP-3: a session that arms an already-expired read
// deadline (the failed-handshake pattern) must time out its own read
// without arming the shared listening socket: the listener must still
// accept and serve a new session afterwards.
func TestSessionReadDeadlineDoesNotArmSharedSocket(t *testing.T) {
	pc := newFakePacketConn()
	a := newTestSession(pc, 20001)

	if err := a.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}

	// Session A's own read must report its deadline.
	readErr := make(chan error, 1)
	go func() {
		_, err := a.Read(make([]byte, 64))
		readErr <- err
	}()
	select {
	case err := <-readErr:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("session A read error = %v, want deadline exceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session A read did not honour its own deadline")
	}

	// The shared listening socket must still deliver a new datagram
	// (i.e. the accept path is unharmed).
	peer := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002}
	pc.inbox <- fakePacket{b: []byte("ping"), addr: peer}
	accErr := make(chan error, 1)
	go func() {
		_, _, err := pc.ReadFrom(make([]byte, 64))
		accErr <- err
	}()
	select {
	case err := <-accErr:
		if err != nil {
			t.Fatalf("shared socket ReadFrom after session deadline: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shared socket did not deliver the new datagram")
	}

	// And the new session on the same socket is served normally.
	b := newTestSession(pc, 20002)
	if err := b.send([]byte("hello")); err != nil {
		t.Fatalf("queue to session B: %v", err)
	}
	buf := make([]byte, 64)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatalf("session B read: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("session B read %q, want %q", buf[:n], "hello")
	}

	if pc.deadlineCalls() != 0 {
		t.Fatalf("session deadlines leaked to shared socket: %d calls", pc.deadlineCalls())
	}
}

// LB-P3-2 / F-UDP-3: one session's deadline must not affect a
// concurrent session's reads or writes over the same shared socket,
// while still applying to the session that set it.
func TestSessionDeadlineDoesNotAffectPeerSession(t *testing.T) {
	pc := newFakePacketConn()
	a := newTestSession(pc, 20011)
	b := newTestSession(pc, 20012)

	if err := a.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	// Session B writes through the shared socket: must succeed.
	if _, err := b.Write([]byte("out")); err != nil {
		t.Fatalf("session B write after peer deadline: %v", err)
	}

	// Session B reads its own queued datagram: must succeed.
	if err := b.send([]byte("in")); err != nil {
		t.Fatalf("queue to session B: %v", err)
	}
	buf := make([]byte, 64)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatalf("session B read after peer deadline: %v", err)
	}
	if string(buf[:n]) != "in" {
		t.Fatalf("session B read %q, want %q", buf[:n], "in")
	}

	// Session A's own write honours its expired write deadline.
	if _, err := a.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("session A write error = %v, want deadline exceeded", err)
	}

	if pc.deadlineCalls() != 0 {
		t.Fatalf("session deadlines leaked to shared socket: %d calls", pc.deadlineCalls())
	}
}
