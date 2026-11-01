package relay_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/handler"
	relayconnector "github.com/go-gost/x/connector/relay"
	relayhandler "github.com/go-gost/x/handler/relay"
	xudp "github.com/go-gost/x/internal/net/udp"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// End-to-end regression tests for relay-over-UDP (F-UDP-2/3/5).
//
// The topology mirrors the panel-generated one that was broken in
// production: a relay handler behind a session-oriented UDP listener
// (with the keepalive metadata the panel now generates, F-UDP-1) and
// a relay connector over a datagram transport. Before the fix, the
// first response frame was discarded by the connector's small reads
// and any later frame was rejected with "bad version"; a failed
// handshake also wedged the listener one handshake-timeout later via
// a deadline armed on the shared listening socket.
//
// The two outer legs (client transport, echo target) run over an
// in-memory datagram hub instead of real UDP sockets: this sandbox
// forbids sending from an unconnected UDP socket (sendto EPERM), so
// no real-socket round trip is possible here at all. The hub
// reproduces kernel datagram semantics exactly - one datagram per
// Read, overflow silently discarded - which is the property the code
// under test depends on; every component between the legs (listener,
// session conns, handler, connector, framing) is the production code.
// Real-socket verification of the same gates happens on the test
// machine (Phase F).

// ---- in-memory datagram hub -------------------------------------------

type memAddr string

func (a memAddr) Network() string { return "udp" }
func (a memAddr) String() string  { return string(a) }

type memDatagram struct {
	data []byte
	from net.Addr
}

type memHub struct {
	mu   sync.Mutex
	ends map[string]*memEndpoint
	seq  int
}

func newMemHub() *memHub {
	return &memHub{ends: make(map[string]*memEndpoint)}
}

func (h *memHub) endpoint() *memEndpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e := &memEndpoint{
		hub:   h,
		addr:  memAddr(fmt.Sprintf("mem-%d", h.seq)),
		inbox: make(chan memDatagram, 1024),
		down:  make(chan struct{}),
	}
	h.ends[e.addr.String()] = e
	return e
}

func (h *memHub) lookup(addr net.Addr) *memEndpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ends[addr.String()]
}

// memEndpoint is one datagram endpoint: it implements net.Conn (for
// connected-style use) and net.PacketConn (for listener-style use)
// with kernel UDP semantics.
type memEndpoint struct {
	hub   *memHub
	addr  memAddr
	peer  *memEndpoint // default destination for Write
	inbox chan memDatagram
	down  chan struct{}
	once  sync.Once

	mu  sync.Mutex
	rdl time.Time
	wdl time.Time
}

func (e *memEndpoint) connect(peer *memEndpoint) { e.peer = peer }

func (e *memEndpoint) deliver(d memDatagram) error {
	select {
	case <-e.down:
		return net.ErrClosed
	default:
	}
	select {
	case e.inbox <- d:
		return nil
	case <-e.down:
		return net.ErrClosed
	}
}

func (e *memEndpoint) sendTo(b []byte, dst *memEndpoint) (int, error) {
	if dst == nil {
		return 0, errors.New("mem: no destination")
	}
	e.mu.Lock()
	wdl := e.wdl
	e.mu.Unlock()
	if !wdl.IsZero() && time.Until(wdl) <= 0 {
		return 0, os.ErrDeadlineExceeded
	}
	data := append([]byte(nil), b...)
	if err := dst.deliver(memDatagram{data: data, from: e.addr}); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (e *memEndpoint) recv(b []byte) (int, net.Addr, error) {
	e.mu.Lock()
	rdl := e.rdl
	e.mu.Unlock()

	var timeout <-chan time.Time
	var timer *time.Timer
	if !rdl.IsZero() {
		d := time.Until(rdl)
		if d <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		timer = time.NewTimer(d)
		timeout = timer.C
		defer timer.Stop()
	}

	select {
	case d := <-e.inbox:
		// One datagram per Read; overflow is discarded, exactly
		// like a kernel UDP socket.
		return copy(b, d.data), d.from, nil
	case <-e.down:
		return 0, nil, net.ErrClosed
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (e *memEndpoint) Read(b []byte) (int, error) {
	n, _, err := e.recv(b)
	return n, err
}

func (e *memEndpoint) ReadFrom(b []byte) (int, net.Addr, error) {
	return e.recv(b)
}

func (e *memEndpoint) Write(b []byte) (int, error) {
	return e.sendTo(b, e.peer)
}

func (e *memEndpoint) WriteTo(b []byte, addr net.Addr) (int, error) {
	return e.sendTo(b, e.hub.lookup(addr))
}

func (e *memEndpoint) Close() error {
	e.once.Do(func() { close(e.down) })
	return nil
}

func (e *memEndpoint) LocalAddr() net.Addr  { return e.addr }
func (e *memEndpoint) RemoteAddr() net.Addr { return e.peer.addr }

func (e *memEndpoint) SetDeadline(t time.Time) error {
	e.mu.Lock()
	e.rdl, e.wdl = t, t
	e.mu.Unlock()
	return nil
}

func (e *memEndpoint) SetReadDeadline(t time.Time) error {
	e.mu.Lock()
	e.rdl = t
	e.mu.Unlock()
	return nil
}

func (e *memEndpoint) SetWriteDeadline(t time.Time) error {
	e.mu.Lock()
	e.wdl = t
	e.mu.Unlock()
	return nil
}

// ---- fixtures ----------------------------------------------------------

type stubRouter struct {
	targets map[string]func() (net.Conn, error)
}

func (r stubRouter) Options() *chain.RouterOptions { return &chain.RouterOptions{} }

func (r stubRouter) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if f, ok := r.targets[address]; ok {
		return f()
	}
	return nil, fmt.Errorf("no such target %s", address)
}

func (r stubRouter) Bind(ctx context.Context, network, address string, opts ...chain.BindOption) (net.Listener, error) {
	return nil, errors.New("bind not supported in test")
}

// echoTargetEnd is the far end of an echo target: every datagram read
// is written back with the marker prepended.
func echoTargetEnd(end *memEndpoint, marker string) {
	go func() {
		buf := make([]byte, 65535)
		for {
			n, err := end.Read(buf)
			if err != nil {
				return
			}
			resp := append([]byte(marker), buf[:n]...)
			if _, err := end.Write(resp); err != nil {
				return
			}
		}
	}()
}

func startRelayServer(t *testing.T, hub *memHub, handlerMd map[string]any, router stubRouter) *memEndpoint {
	t.Helper()

	serverEnd := hub.endpoint()
	ln := xudp.NewListener(serverEnd, &xudp.ListenConfig{
		Backlog:        128,
		ReadQueueSize:  128,
		ReadBufferSize: xudp.MaxDatagramSize,
		TTL:            30 * time.Second,
		// keepalive is what the panel generates for session-oriented
		// UDP services after F-UDP-1; these tests pin the x data
		// plane under that configuration.
		Keepalive: true,
		Logger:    xlogger.Nop(),
	})
	t.Cleanup(func() { ln.Close() })

	h := relayhandler.NewHandler(
		handler.RouterOption(router),
		handler.LoggerOption(xlogger.Nop()),
		handler.ServiceOption("test-relay-udp"),
	)
	if err := h.Init(mdx.NewMetadata(handlerMd)); err != nil {
		t.Fatal(err)
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go h.Handle(context.Background(), conn)
		}
	}()

	return serverEnd
}

// newEchoTarget registers an echo target under addr and returns the
// client end the router should hand to the handler.
func newEchoTarget(hub *memHub, marker string) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		clientEnd := hub.endpoint()
		echoEnd := hub.endpoint()
		clientEnd.connect(echoEnd)
		echoEnd.connect(clientEnd)
		echoTargetEnd(echoEnd, marker)
		return clientEnd, nil
	}
}

func dialRelay(t *testing.T, hub *memHub, server *memEndpoint, target string) net.Conn {
	t.Helper()
	transport := hub.endpoint()
	transport.connect(server)
	t.Cleanup(func() { transport.Close() })

	c := relayconnector.NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := c.Init(mdx.NewMetadata(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	cc, err := c.Connect(context.Background(), transport, "udp", target)
	if err != nil {
		t.Fatalf("relay Connect: %v", err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func roundTrip(t *testing.T, cc net.Conn, payload []byte) []byte {
	t.Helper()
	if err := cc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer cc.SetReadDeadline(time.Time{})

	if _, err := cc.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 65535)
	n, err := cc.Read(buf)
	if err != nil {
		t.Fatalf("Read (payload %d bytes): %v", len(payload), err)
	}
	return buf[:n]
}

// ---- tests -------------------------------------------------------------

func TestRelayOverUDPMultiRoundTrip(t *testing.T) {
	hub := newMemHub()
	const target = "127.0.0.1:5309"
	router := stubRouter{targets: map[string]func() (net.Conn, error){target: newEchoTarget(hub, "ECHO:")}}
	server := startRelayServer(t, hub, nil, router)
	cc := dialRelay(t, hub, server, target)

	// >= 6 sequential round trips on ONE association, each reply
	// verified byte for byte. The first reply already exercises the
	// merged response+first-frame datagram (F-UDP-2).
	for i := 0; i < 8; i++ {
		payload := []byte(fmt.Sprintf("pkt-%d-payload", i))
		got := roundTrip(t, cc, payload)
		if want := append([]byte("ECHO:"), payload...); !bytes.Equal(got, want) {
			t.Fatalf("round trip %d: got %q, want %q", i, got, want)
		}
	}
}

func TestRelayOverUDPLargeFrame(t *testing.T) {
	hub := newMemHub()
	const target = "127.0.0.1:5309"
	router := stubRouter{targets: map[string]func() (net.Conn, error){target: newEchoTarget(hub, "ECHO:")}}
	server := startRelayServer(t, hub, nil, router)
	cc := dialRelay(t, hub, server, target)

	// 60000-byte frame: above every old silent-truncation ceiling on
	// the path (8192 listen buffer, 4096 sniffing buffer) and within
	// the protocol's 65535 frame limit (F-UDP-5).
	payload := make([]byte, 60000)
	for i := range payload {
		payload[i] = byte(i * 31 % 251)
	}
	got := roundTrip(t, cc, payload)
	if want := append([]byte("ECHO:"), payload...); !bytes.Equal(got, want) {
		t.Fatalf("large frame mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestRelayOverUDPGarbageThenNewAssociation(t *testing.T) {
	hub := newMemHub()
	const target = "127.0.0.1:5309"
	router := stubRouter{targets: map[string]func() (net.Conn, error){target: newEchoTarget(hub, "ECHO:")}}
	// Short handshake timeout so the test can wait past it; the
	// production default is 15s (that fuse is what killed the
	// listener in the field).
	server := startRelayServer(t, hub, map[string]any{"readTimeout": "300ms"}, router)

	raw := hub.endpoint()
	raw.connect(server)

	// Garbage that is not a relay request at all.
	if _, err := raw.Write([]byte{0x00, 0x05, 'p', 'k', 't', '-', 'x'}); err != nil {
		t.Fatal(err)
	}
	// A plausible-looking but truncated request: the header promises
	// 64 bytes of features that never arrive, so the handler blocks
	// in the handshake read until its read deadline fires.
	if _, err := raw.Write([]byte{0x01, 0x81, 0x00, 0x40}); err != nil {
		t.Fatal(err)
	}

	// Wait well past the handshake deadline. On the defective code
	// the deadline was left armed on the shared listening socket and
	// the listen loop died here; no new association could be served.
	time.Sleep(800 * time.Millisecond)

	cc := dialRelay(t, hub, server, target)
	for i := 0; i < 6; i++ {
		payload := []byte("after-garbage")
		got := roundTrip(t, cc, payload)
		if want := append([]byte("ECHO:"), payload...); !bytes.Equal(got, want) {
			t.Fatalf("round trip %d after garbage: got %q, want %q", i, got, want)
		}
	}
}

func TestRelayOverUDPConcurrentAssociations(t *testing.T) {
	hub := newMemHub()
	const target1 = "127.0.0.1:5309"
	const target2 = "127.0.0.1:5310"
	router := stubRouter{targets: map[string]func() (net.Conn, error){
		target1: newEchoTarget(hub, "T1:"),
		target2: newEchoTarget(hub, "T2:"),
	}}
	server := startRelayServer(t, hub, nil, router)

	cc1 := dialRelay(t, hub, server, target1)
	cc2 := dialRelay(t, hub, server, target2)

	// Interleaved traffic on two associations from the same host to
	// different targets: replies must not cross associations.
	for i := 0; i < 4; i++ {
		p1 := []byte("one")
		if got, want := roundTrip(t, cc1, p1), append([]byte("T1:"), p1...); !bytes.Equal(got, want) {
			t.Fatalf("association 1 round %d: got %q, want %q", i, got, want)
		}
		p2 := []byte("two")
		if got, want := roundTrip(t, cc2, p2), append([]byte("T2:"), p2...); !bytes.Equal(got, want) {
			t.Fatalf("association 2 round %d: got %q, want %q", i, got, want)
		}
	}
}
