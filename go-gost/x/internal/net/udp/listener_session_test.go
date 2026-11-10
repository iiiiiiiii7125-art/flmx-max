package udp

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// Regression tests for the relay-over-UDP data-plane defects:
//   - F-UDP-2: the session conn's stream Read must not discard the
//     tail of a datagram that carried more than the requested bytes.
//   - F-UDP-3: a session's read deadline must be session-local and
//     must never arm a timeout on the shared listening socket; the
//     listen loop must survive transient read errors.

func newSessionTestListener(t *testing.T, pc net.PacketConn) net.Listener {
	t.Helper()
	ln := NewListener(pc, &ListenConfig{
		Backlog:        128,
		ReadQueueSize:  128,
		ReadBufferSize: MaxDatagramSize,
		TTL:            30 * time.Second,
		Keepalive:      true,
		Logger:         xlogger.Nop(),
	})
	t.Cleanup(func() { ln.Close() })
	return ln
}

func listenUDP(t *testing.T) net.PacketConn {
	t.Helper()
	requireLoopbackUDP(t)
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	return pc
}

func dialUDPClient(t *testing.T, addr net.Addr) *net.UDPConn {
	t.Helper()
	c, err := net.DialUDP("udp", nil, addr.(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func acceptWithTimeout(t *testing.T, ln net.Listener, d time.Duration) net.Conn {
	t.Helper()
	type res struct {
		conn net.Conn
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- res{conn, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Accept: %v", r.err)
		}
		return r.conn
	case <-time.After(d):
		t.Fatalf("Accept did not return within %v (listener wedged?)", d)
		return nil
	}
}

func TestSessionConnStreamReadRetainsDatagramRemainder(t *testing.T) {
	pc := listenUDP(t)
	ln := newSessionTestListener(t, pc)
	client := dialUDPClient(t, pc.LocalAddr())

	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	conn := acceptWithTimeout(t, ln, 2*time.Second)

	// Stream face: read the single datagram in small pieces, the way
	// io.ReadFull-based framing does (4B header, 2B length, body).
	errCh := make(chan error, 1)
	go func() {
		var got []byte
		for _, want := range []int{4, 2, 30, 64} {
			buf := make([]byte, want)
			if _, err := io.ReadFull(conn, buf); err != nil {
				errCh <- err
				return
			}
			got = append(got, buf...)
		}
		if string(got) != string(payload) {
			errCh <- errors.New("stream read returned wrong bytes")
			return
		}
		errCh <- nil
	}()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("stream Read lost datagram tail: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream Read blocked: datagram remainder was discarded")
	}

	// Packet face on the same session still returns whole datagrams.
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, addr, err := conn.(net.PacketConn).ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "hello" || addr.String() != client.LocalAddr().String() {
		t.Fatalf("ReadFrom = %q from %v, want %q from %v", buf[:n], addr, "hello", client.LocalAddr())
	}
}

func TestDatagramReaderPreservesBytesAcrossDatagrams(t *testing.T) {
	r := NewDatagramReader(&datagramFuncReader{dgrams: [][]byte{[]byte("abcdef"), []byte("gh")}})

	buf := make([]byte, 2)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "ab" {
		t.Fatalf("read 1 = %q, %v", buf, err)
	}
	buf = make([]byte, 4)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "cdef" {
		t.Fatalf("read 2 = %q, %v (datagram tail discarded?)", buf, err)
	}
	buf = make([]byte, 2)
	if _, err := io.ReadFull(r, buf); err != nil || string(buf) != "gh" {
		t.Fatalf("read 3 = %q, %v", buf, err)
	}
	if _, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("final read err = %v, want EOF", err)
	}
}

// datagramFuncReader mimics a raw datagram socket: every Read returns
// at most one datagram and discards any overflow.
type datagramFuncReader struct {
	dgrams [][]byte
}

func (r *datagramFuncReader) Read(p []byte) (int, error) {
	if len(r.dgrams) == 0 {
		return 0, io.EOF
	}
	d := r.dgrams[0]
	r.dgrams = r.dgrams[1:]
	return copy(p, d), nil
}

func TestSessionDeadlineIsSessionLocal(t *testing.T) {
	pc := listenUDP(t)
	ln := newSessionTestListener(t, pc)

	clientA := dialUDPClient(t, pc.LocalAddr())
	if _, err := clientA.Write([]byte("a1")); err != nil {
		t.Fatal(err)
	}
	connA := acceptWithTimeout(t, ln, 2*time.Second)

	// Drain the datagram that created the session; the deadline
	// experiment below needs an empty queue.
	if n, _, err := connA.(net.PacketConn).ReadFrom(make([]byte, 16)); err != nil || n != 2 {
		t.Fatalf("drain session A: n=%d err=%v", n, err)
	}

	// Arm a short read deadline on session A and let a read time out.
	if err := connA.SetReadDeadline(time.Now().Add(80 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := connA.Read(make([]byte, 16))
		readErr <- err
	}()
	select {
	case err := <-readErr:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("session A read err = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session A read did not honour its own deadline")
	}

	// Session B is accepted and readable.
	clientB := dialUDPClient(t, pc.LocalAddr())
	if _, err := clientB.Write([]byte("b1")); err != nil {
		t.Fatal(err)
	}
	connB := acceptWithTimeout(t, ln, 2*time.Second)

	// Wait until long after A's deadline fired. On the defective
	// code the deadline sat on the shared listening socket, so the
	// listen loop died at this point and no new session could ever
	// be accepted again.
	time.Sleep(300 * time.Millisecond)

	clientC := dialUDPClient(t, pc.LocalAddr())
	if _, err := clientC.Write([]byte("c1")); err != nil {
		t.Fatal(err)
	}
	connC := acceptWithTimeout(t, ln, 2*time.Second)

	buf := make([]byte, 16)
	if n, _, err := connB.(net.PacketConn).ReadFrom(buf); err != nil || string(buf[:n]) != "b1" {
		t.Fatalf("session B ReadFrom = %q, %v; want b1", buf[:n], err)
	}
	if n, _, err := connC.(net.PacketConn).ReadFrom(buf); err != nil || string(buf[:n]) != "c1" {
		t.Fatalf("session C ReadFrom = %q, %v; want c1", buf[:n], err)
	}
}

// flakyPacketConn fails the first failFirst ReadFrom calls with err,
// then delegates to the real socket.
type flakyPacketConn struct {
	net.PacketConn
	failFirst int32
	err       error
	calls     atomic.Int32
}

func (c *flakyPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.calls.Add(1) <= c.failFirst {
		return 0, nil, c.err
	}
	return c.PacketConn.ReadFrom(b)
}

func TestListenLoopSurvivesTemporaryReadError(t *testing.T) {
	pc := listenUDP(t)
	flaky := &flakyPacketConn{PacketConn: pc, failFirst: 1, err: os.ErrDeadlineExceeded}
	ln := newSessionTestListener(t, flaky)

	client := dialUDPClient(t, pc.LocalAddr())
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}

	conn := acceptWithTimeout(t, ln, 2*time.Second)
	buf := make([]byte, 16)
	if n, _, err := conn.(net.PacketConn).ReadFrom(buf); err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("ReadFrom = %q, %v; want ping", buf[:n], err)
	}
}

func TestListenLoopReportsFatalReadError(t *testing.T) {
	pc := listenUDP(t)
	boom := errors.New("fatal boom")
	flaky := &flakyPacketConn{PacketConn: pc, failFirst: 1 << 30, err: boom}
	ln := newSessionTestListener(t, flaky)

	_, err := ln.Accept()
	if !errors.Is(err, boom) {
		t.Fatalf("Accept err = %v, want the fatal read error", err)
	}
}
