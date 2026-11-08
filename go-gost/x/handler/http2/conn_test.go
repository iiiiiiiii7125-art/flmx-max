package http2

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	xio "github.com/go-gost/x/internal/io"
	xnet "github.com/go-gost/x/internal/net"
)

// P1-01: the HTTP/2 CONNECT endpoint must expose io.Closer so
// Transport's no-progress teardown can unblock the copy goroutine
// parked on the request body, and must deliberately NOT expose
// stream deadline methods (Go's HTTP/2 stream deadlines break the
// body pipe stickily; see streamReadWriter in conn.go).

var errFakeBodyClosed = errors.New("fake body closed")

type fakeBody struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeBody() *fakeBody {
	return &fakeBody{closed: make(chan struct{})}
}

func (b *fakeBody) Read(p []byte) (int, error) {
	<-b.closed
	return 0, errFakeBodyClosed
}

func (b *fakeBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestStreamReadWriterCloseUnblocksRead(t *testing.T) {
	body := newFakeBody()
	var out bytes.Buffer
	// Same construction as the handler: base adapter over the
	// body, streamReadWriter re-exposing the body's Close.
	rw := &streamReadWriter{
		ReadWriter: xio.NewReadWriter(body, &out),
		body:       body,
	}

	if _, ok := any(rw).(io.Closer); !ok {
		t.Fatal("streamReadWriter does not implement io.Closer")
	}
	// Deliberate design (conn.go): no destructive stream deadlines.
	if _, ok := any(rw).(interface{ SetReadDeadline(time.Time) error }); ok {
		t.Fatal("streamReadWriter unexpectedly implements SetReadDeadline")
	}
	if _, ok := any(rw).(interface{ SetWriteDeadline(time.Time) error }); ok {
		t.Fatal("streamReadWriter unexpectedly implements SetWriteDeadline")
	}

	readc := make(chan error, 1)
	go func() {
		_, err := rw.Read(make([]byte, 16))
		readc <- err
	}()

	// Read is parked on the body; only Close can release it.
	select {
	case err := <-readc:
		t.Fatalf("Read returned before Close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	closer, _ := any(rw).(io.Closer)
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-readc:
		if !errors.Is(err, errFakeBodyClosed) {
			t.Fatalf("Read err = %v, want the body-closed error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the parked Read")
	}

	// Write passes through to the wrapped writer.
	if _, err := rw.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if out.String() != "ping" {
		t.Fatalf("Write delivered %q, want %q", out.String(), "ping")
	}
}

// P1-01 hijack sub-form: the hijacked endpoint is the raw client
// conn re-wrapped OUTSIDE the limiter/stats wrappers (which embed
// io.ReadWriter and would otherwise hide the conn's deadlines and
// Close again). The outer NewReadWriteConn must expose the full
// net.Conn surface of the hijacked conn, and data must still flow
// through the inner wrappers.

func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	acceptc := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		acceptc <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-acceptc
	if server == nil {
		t.Fatal("accept failed")
	}
	return client, server
}

// countingRW is a metrics sink for the test wrapper below.
type countingRW struct {
	rw  io.ReadWriter
	mu  sync.Mutex
	in  int64
	out int64
}

func (c *countingRW) Read(p []byte) (int, error) {
	n, err := c.rw.Read(p)
	c.mu.Lock()
	c.in += int64(n)
	c.mu.Unlock()
	return n, err
}

func (c *countingRW) Write(p []byte) (int, error) {
	n, err := c.rw.Write(p)
	c.mu.Lock()
	c.out += int64(n)
	c.mu.Unlock()
	return n, err
}

func (c *countingRW) counts() (in, out int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.in, c.out
}

// rwOnlyWrapper mimics the limiter/stats wrappers: it embeds
// io.ReadWriter, so every other method of the wrapped value is
// hidden from the outside.
type rwOnlyWrapper struct {
	io.ReadWriter
}

func (w *rwOnlyWrapper) Read(p []byte) (int, error)  { return w.ReadWriter.Read(p) }
func (w *rwOnlyWrapper) Write(p []byte) (int, error) { return w.ReadWriter.Write(p) }

func TestHijackedEndpointRewrapExposesConn(t *testing.T) {
	hijacked, peer := tcpPair(t)
	defer peer.Close()

	// Handler construction, hijack branch: the hijacked conn is
	// wrapped around a counting core by an rw-only wrapper, then
	// re-wrapped outside it.
	counted := &countingRW{rw: hijacked}
	inner := &rwOnlyWrapper{ReadWriter: counted}
	rw := xnet.NewReadWriteConn(inner, inner, hijacked)

	// Before the re-wrap the endpoint was rw-only; pin that the
	// inner wrapper alone really hides the conn surface.
	if _, ok := any(inner).(io.Closer); ok {
		t.Fatal("inner rw-only wrapper unexpectedly implements io.Closer")
	}
	if _, ok := any(inner).(interface{ SetReadDeadline(time.Time) error }); ok {
		t.Fatal("inner rw-only wrapper unexpectedly implements SetReadDeadline")
	}

	// The re-wrapped endpoint exposes the hijacked conn's surface.
	nc, ok := any(rw).(net.Conn)
	if !ok {
		t.Fatal("re-wrapped hijacked endpoint does not implement net.Conn")
	}
	if _, ok = any(rw).(io.Closer); !ok {
		t.Fatal("re-wrapped hijacked endpoint does not implement io.Closer")
	}
	if _, ok = any(rw).(interface{ SetReadDeadline(time.Time) error }); !ok {
		t.Fatal("re-wrapped hijacked endpoint does not implement SetReadDeadline")
	}
	if _, ok = any(rw).(interface{ SetWriteDeadline(time.Time) error }); !ok {
		t.Fatal("re-wrapped hijacked endpoint does not implement SetWriteDeadline")
	}

	// Data still flows through the inner wrapper in both
	// directions (the wrapper is in the data path, not bypassed).
	// The endpoint reads with a full-size buffer: readWriteConn
	// passes the caller's buffer straight to the inner wrapper,
	// so a small ReadFull buffer in wrap order would starve the
	// wrapper's Read of bytes already sitting past it in the
	// readWriteConn buffer (buffered-remainder hazard, inherent to
	// the wrapper order, see handler comment). Transport's own
	// 64 KiB copy buffer never starves it.
	// Deadlines bound every step: a blocked read here must fail
	// the test fast, never hang the package.
	deadline := time.Now().Add(10 * time.Second)
	_ = peer.SetDeadline(deadline)
	_ = hijacked.SetDeadline(deadline)
	defer func() {
		_ = peer.SetDeadline(time.Time{})
		_ = hijacked.SetDeadline(time.Time{})
	}()
	if _, err := peer.Write([]byte("down")); err != nil {
		t.Fatalf("peer write: %v", err)
	}
	big := make([]byte, 64*1024)
	n, err := rw.Read(big)
	if err != nil {
		t.Fatalf("endpoint read: %v", err)
	}
	if string(big[:n]) != "down" {
		t.Fatalf("endpoint read %q, want %q", big[:n], "down")
	}
	if _, err := rw.Write([]byte("uplink")); err != nil {
		t.Fatalf("endpoint write: %v", err)
	}
	buf := make([]byte, 6)
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatalf("peer read: %v", err)
	}
	if string(buf) != "uplink" {
		t.Fatalf("peer read %q, want %q", buf, "uplink")
	}
	if in, out := counted.counts(); in != 4 || out != 6 {
		t.Fatalf("inner wrapper saw in=%d out=%d, want 4/6 (wrapper bypassed?)", in, out)
	}

	// Close on the endpoint closes the hijacked conn itself.
	if err := nc.Close(); err != nil {
		t.Fatalf("endpoint Close: %v", err)
	}
	if _, err := hijacked.Write([]byte("x")); err == nil {
		t.Fatal("hijacked conn still writable after endpoint Close")
	}
}
