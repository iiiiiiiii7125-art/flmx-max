package net

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xio "github.com/go-gost/x/internal/io"
)

// P1-01 regression tests: Transport endpoints built over adapters
// (bufio readers, HTTP/2 request bodies) historically exposed
// neither deadline methods nor io.Closer, so the FIX-1 no-progress
// bound could neither arm them nor tear them down, and a session
// parked on such an endpoint outlived the 300s cap. These tests pin
// the two endpoint shapes the handlers now construct:
//
//   - NewReadWriteConn(br, conn, conn) — the bufio-backed client
//     side of HTTP upgrades and sniffed sessions (handler/http,
//     handler/tunnel, handler/unix, both sniffers): deadlines and
//     Close reach the underlying conn through the embedded conn.
//   - a closer-only adapter (the HTTP/2 CONNECT stream endpoint in
//     handler/http2): no deadline methods, but io.Closer whose
//     Close unblocks the parked Read; the bound reaches it via the
//     armable sibling's teardown.

var errParkedBodyClosed = errors.New("parked body closed")

// parkedBody blocks in Read until Close is called, mirroring an
// HTTP/2 request body parked on its pipe.
type parkedBody struct {
	closed chan struct{}
	once   sync.Once
	closes atomic.Int32
}

func newParkedBody() *parkedBody {
	return &parkedBody{closed: make(chan struct{})}
}

func (b *parkedBody) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errParkedBodyClosed
}

func (b *parkedBody) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.closed) })
	return nil
}

// closerOnlyAdapter is the HTTP/2 endpoint shape: Read/Write plus
// Close, no deadline methods.
type closerOnlyAdapter struct {
	body    *parkedBody
	written atomic.Int64
}

func (a *closerOnlyAdapter) Read(p []byte) (int, error) { return a.body.Read(p) }

func (a *closerOnlyAdapter) Write(p []byte) (int, error) {
	a.written.Add(int64(len(p)))
	return len(p), nil
}

func (a *closerOnlyAdapter) Close() error { return a.body.Close() }

func TestTransportCloserOnlyAdapterTeardown(t *testing.T) {
	const idle = 300 * time.Millisecond

	adapter := &closerOnlyAdapter{body: newParkedBody()}
	// Pin the shape: no deadline support on this endpoint.
	if _, ok := any(adapter).(readDeadliner); ok {
		t.Fatal("test adapter unexpectedly implements SetReadDeadline")
	}
	if _, ok := any(adapter).(writeDeadliner); ok {
		t.Fatal("test adapter unexpectedly implements SetWriteDeadline")
	}

	// Sibling: a silent but deadline-armable conn.
	peerSide, transportSide := tcpPair(t)
	defer peerSide.Close()
	defer transportSide.Close()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- transport(adapter, transportSide, idle) }()

	// The adapter direction stays parked well into the window.
	select {
	case err := <-done:
		t.Fatalf("Transport returned early: %v", err)
	case <-time.After(idle / 2):
	}
	if n := adapter.body.closes.Load(); n != 0 {
		t.Fatalf("adapter closed %d times before teardown", n)
	}

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
		if elapsed := time.Since(start); elapsed < idle {
			t.Fatalf("Transport returned after %v, before the %v idle window", elapsed, idle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not tear down the session parked on a closer-only adapter")
	}
	// Transport returning at all proves the parked Read was
	// released; it can only have been released by teardown's Close.
	if n := adapter.body.closes.Load(); n == 0 {
		t.Fatal("teardown never closed the adapter endpoint")
	}
}

// The HTTP/2 one-way shape: the sibling direction streams steadily
// (server -> client) while the adapter direction sends nothing.
// The session must survive many idle windows, and be reaped once
// the flow stops.
func TestTransportCloserOnlyAdapterTrickleNotKilled(t *testing.T) {
	const idle = 300 * time.Millisecond

	adapter := &closerOnlyAdapter{body: newParkedBody()}
	peerSide, transportSide := tcpPair(t)
	defer peerSide.Close()
	defer transportSide.Close()

	done := make(chan error, 1)
	go func() { done <- transport(adapter, transportSide, idle) }()

	// 9 chunks of 16B, one every idle/3: span ≈ 3 idle windows.
	chunk := bytes.Repeat([]byte("x"), 16)
	for i := 0; i < 9; i++ {
		if _, err := peerSide.Write(chunk); err != nil {
			t.Fatalf("peer write %d: %v", i, err)
		}
		time.Sleep(idle / 3)
	}
	if got, want := adapter.written.Load(), int64(9*16); got != want {
		t.Fatalf("adapter received %d bytes, want %d", got, want)
	}
	select {
	case err := <-done:
		t.Fatalf("Transport returned during a one-way flowing session: %v", err)
	default:
	}

	// Flow stopped: the no-progress bound must now reap the
	// session, including the copy parked on the adapter's Read.
	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not reap the session after the one-way flow stopped")
	}
}

// The bufio-backed upgrade/sniffing shape: both endpoints are
// NewReadWriteConn over silent conns. A fully stalled session must
// be torn down inside the window, with both underlying conns
// closed.
func TestTransportReadWriteConnEndpointsTeardown(t *testing.T) {
	const idle = 300 * time.Millisecond

	peer1, side1 := tcpPair(t)
	defer peer1.Close()
	peer2, side2 := tcpPair(t)
	defer peer2.Close()

	rw1 := NewReadWriteConn(bufio.NewReader(side1), side1, side1)
	rw2 := NewReadWriteConn(bufio.NewReader(side2), side2, side2)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- transport(rw1, rw2, idle) }()

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
		if elapsed := time.Since(start); elapsed < idle {
			t.Fatalf("Transport returned after %v, before the %v idle window", elapsed, idle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not tear down the stalled readWriteConn session")
	}

	for name, c := range map[string]net.Conn{"side1": side1, "side2": side2} {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatalf("%s endpoint still readable after teardown", name)
		} else if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			t.Fatalf("%s endpoint not closed after teardown (read timed out)", name)
		}
	}
}

// NewReadWriteConn must surface the wrapped conn's deadline and
// Close methods — that reachability is the whole point of using it
// over xio.NewReadWriter at the adapter construction sites.
func TestNewReadWriteConnForwardsDeadlineAndClose(t *testing.T) {
	client, server := tcpPair(t)
	defer server.Close()

	rw := NewReadWriteConn(bufio.NewReader(client), client, client)

	rd, ok := rw.(readDeadliner)
	if !ok {
		t.Fatal("readWriteConn does not expose SetReadDeadline")
	}
	if _, ok := rw.(writeDeadliner); !ok {
		t.Fatal("readWriteConn does not expose SetWriteDeadline")
	}
	if err := rd.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	// The deadline must have landed on the underlying conn.
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on wrapped conn unaffected by wrapper SetReadDeadline")
	} else if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("read err = %v, want timeout", err)
	}
	_ = rd.SetReadDeadline(time.Time{})

	closer, ok := rw.(io.Closer)
	if !ok {
		t.Fatal("readWriteConn does not expose Close")
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := server.Read(make([]byte, 1)); err == nil {
		t.Fatal("peer did not observe wrapper Close")
	} else if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
		t.Fatal("peer read timed out: wrapper Close did not close the conn")
	}
}

// Endpoint-shape throughput comparison for the adapter fix: the
// legacy xio.NewReadWriter(br, conn) endpoint vs the
// NewReadWriteConn(br, conn, conn) endpoint that replaces it. The
// transport itself is untouched; this pins that swapping the
// adapter construction costs nothing on the forwarding path.
func runAdapterThroughput(b *testing.B, wrap func(net.Conn) io.ReadWriter, total int) {
	b.Helper()
	clientSide, transportToClient := tcpPair(b)
	backendSide, transportToBackend := tcpPair(b)
	defer clientSide.Close()
	defer backendSide.Close()
	go echoBackend(backendSide)

	done := make(chan error, 1)
	go func() { done <- Transport(wrap(transportToClient), transportToBackend) }()

	chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	go func() {
		for sent := 0; sent < total; {
			n := len(chunk)
			if rem := total - sent; rem < n {
				n = rem
			}
			if _, err := clientSide.Write(chunk[:n]); err != nil {
				return
			}
			sent += n
		}
		clientSide.(*net.TCPConn).CloseWrite()
	}()

	buf := make([]byte, 64*1024)
	for received := 0; received < total; {
		n, err := clientSide.Read(buf)
		received += n
		if err != nil {
			b.Fatalf("client read after %d/%d bytes: %v", received, total, err)
		}
	}
	if err := <-done; err != nil {
		b.Fatalf("transport: %v", err)
	}
	transportToClient.Close()
	transportToBackend.Close()
}

func BenchmarkTransportAdapterLegacyShape(b *testing.B) {
	const total = 32 << 20
	b.SetBytes(total)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runAdapterThroughput(b, func(c net.Conn) io.ReadWriter {
			return xio.NewReadWriter(bufio.NewReader(c), c)
		}, total)
	}
}

func BenchmarkTransportAdapterConnShape(b *testing.B) {
	const total = 32 << 20
	b.SetBytes(total)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		runAdapterThroughput(b, func(c net.Conn) io.ReadWriter {
			return NewReadWriteConn(bufio.NewReader(c), c, c)
		}, total)
	}
}
