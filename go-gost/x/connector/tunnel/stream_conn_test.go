package tunnel_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/connector"
	"github.com/go-gost/relay"
	tunnelconnector "github.com/go-gost/x/connector/tunnel"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// In-memory datagram endpoint pair with kernel UDP semantics: one
// datagram per Read, overflow silently discarded. It implements
// net.PacketConn, like the real datagram transports (e.g. the udp
// dialer's conn). Real UDP sockets are unusable in this sandbox
// (sendto EPERM), see connector/relay/udp_relay_test.go.
type datagramAddr string

func (a datagramAddr) Network() string { return "udp" }
func (a datagramAddr) String() string  { return string(a) }

type datagramEndpoint struct {
	addr  datagramAddr
	peer  *datagramEndpoint
	inbox chan []byte
	done  chan struct{}
	once  sync.Once

	mu  sync.Mutex
	rdl time.Time
}

func datagramPair(a, b string) (*datagramEndpoint, *datagramEndpoint) {
	ea := &datagramEndpoint{addr: datagramAddr(a), inbox: make(chan []byte, 64), done: make(chan struct{})}
	eb := &datagramEndpoint{addr: datagramAddr(b), inbox: make(chan []byte, 64), done: make(chan struct{})}
	ea.peer, eb.peer = eb, ea
	return ea, eb
}

func (e *datagramEndpoint) Read(p []byte) (int, error) {
	e.mu.Lock()
	rdl := e.rdl
	e.mu.Unlock()

	var timeout <-chan time.Time
	if !rdl.IsZero() {
		d := time.Until(rdl)
		if d <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		timeout = timer.C
	}
	select {
	case data := <-e.inbox:
		// One datagram per Read; the overflow is discarded,
		// exactly like a kernel UDP socket.
		return copy(p, data), nil
	case <-e.done:
		return 0, net.ErrClosed
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	}
}

func (e *datagramEndpoint) Write(p []byte) (int, error) {
	data := append([]byte(nil), p...)
	select {
	case e.peer.inbox <- data:
		return len(p), nil
	case <-e.peer.done:
		return 0, net.ErrClosed
	}
}

func (e *datagramEndpoint) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := e.Read(p)
	if err != nil {
		return 0, nil, err
	}
	return n, e.peer.addr, nil
}

func (e *datagramEndpoint) WriteTo(p []byte, _ net.Addr) (int, error) { return e.Write(p) }

func (e *datagramEndpoint) Close() error {
	e.once.Do(func() { close(e.done) })
	return nil
}

func (e *datagramEndpoint) LocalAddr() net.Addr  { return e.addr }
func (e *datagramEndpoint) RemoteAddr() net.Addr { return e.peer.addr }

func (e *datagramEndpoint) SetDeadline(t time.Time) error {
	e.mu.Lock()
	e.rdl = t
	e.mu.Unlock()
	return nil
}

func (e *datagramEndpoint) SetReadDeadline(t time.Time) error  { return e.SetDeadline(t) }
func (e *datagramEndpoint) SetWriteDeadline(t time.Time) error { return nil }

// F-UDP-2b regression: the tunnel connector drains the relay
// response with a fixed-size header read inside Connect. When the
// transport is datagram-oriented and the server merges the response
// with the first data frame into one datagram, that small read
// discards the frame on an unwrapped conn. The payload network here
// is TCP, so the payload-keyed wrapper did not apply before the fix.
func TestTunnelTCPOverDatagramTransport(t *testing.T) {
	clientEnd, serverEnd := datagramPair("client:1000", "server:2000")
	defer clientEnd.Close()
	defer serverEnd.Close()

	payload := []byte("first-frame-payload")
	go func() {
		buf := make([]byte, 65535)
		if _, err := serverEnd.Read(buf); err != nil {
			return
		}
		var b bytes.Buffer
		resp := relay.Response{Version: relay.Version1, Status: relay.StatusOK}
		if _, err := resp.WriteTo(&b); err != nil {
			return
		}
		b.Write(payload)
		// Response + first frame in ONE datagram, as the relay
		// handler's merged write produces.
		serverEnd.Write(b.Bytes())
	}()

	c := tunnelconnector.NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := c.Init(mdx.NewMetadata(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	cc, err := c.Connect(context.Background(), clientEnd, "tcp", "127.0.0.1:19090")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cc.Close()

	if err := cc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(cc, buf); err != nil {
		t.Fatalf("read first frame: %v (frame lost to the response drain?)", err)
	}
	if !bytes.Equal(buf, payload) {
		t.Fatalf("got %q, want %q", buf, payload)
	}
}
