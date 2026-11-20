package service

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/listener"
	tcplistener "github.com/go-gost/x/listener/tcp"
	xlogger "github.com/go-gost/x/logger"
	xmetadata "github.com/go-gost/x/metadata"
	proxyproto "github.com/pires/go-proxyproto"
)

// R18-C5-01 (PeerIP leg): Serve captures the raw previous-hop peer
// before any PROXY folding and records it in ConnEvent.PeerIP
// only when it differs from the final client IP. A connection
// whose PROXY header claims 203.0.113.99 from the real peer
// 127.0.0.1 must log ClientIP 203.0.113.99 with PeerIP 127.0.0.1;
// a direct connection keeps PeerIP empty (wire-identical).

// serveFedConn starts an entry-marked service, feeds it one
// prepared connection, and returns the pieces the test asserts on.
func serveFedConn(t *testing.T, serverConn net.Conn) (*connRing, *gateHandler, func()) {
	t.Helper()
	ring := freshConnlogRing(t)
	SetConnLogEnabled(true)

	ln := newQueueListener(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 24003})
	h := &gateHandler{release: make(chan struct{})}
	svc := NewService("fwd_entry_tcp", ln, h,
		LoggerOption(xlogger.NewLogger()),
		ConnLogEntryOption(true, "fwd_entry_tcp"))
	go func() { _ = svc.Serve() }()

	ln.conns <- serverConn
	return ring, h, func() {
		close(h.release)
		_ = svc.Close()
	}
}

// realTCPPair returns a connected (server, client) pair over real
// loopback TCP. When claim is non-empty the client immediately
// writes a PROXY v1 header claiming that IP, and the server side
// is wrapped in a proxyproto.Conn (default USE policy, as a
// trusting listener would); otherwise the raw pair is returned.
func realTCPPair(t *testing.T, claim string) (server net.Conn, client net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if claim != "" {
		if _, err := fmt.Fprintf(client, "PROXY TCP4 %s 198.51.100.7 45678 4001\r\n", claim); err != nil {
			t.Fatalf("write PROXY header: %v", err)
		}
	}
	select {
	case raw := <-accepted:
		if claim != "" {
			return proxyproto.NewConn(raw), client
		}
		return raw, client
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for accept")
		return nil, nil
	}
}

func TestConnLogPeerIPFromProxyHeader(t *testing.T) {
	server, client := realTCPPair(t, "203.0.113.99")
	defer client.Close()
	ring, h, done := serveFedConn(t, server)
	waitHandled(t, h, 1)
	events := waitConnEvents(t, ring, 1)
	open := events[0]
	if open.Type != ConnEventOpen {
		t.Fatalf("first event must be open, got %+v", open)
	}
	if open.ClientIP != "203.0.113.99" {
		t.Fatalf("ClientIP must be the claimed 203.0.113.99, got %q", open.ClientIP)
	}
	if open.PeerIP != "127.0.0.1" {
		t.Fatalf("PeerIP must be the raw peer 127.0.0.1, got %q", open.PeerIP)
	}
	done()
	closeEvents := waitConnEvents(t, ring, 1)
	closeEv := closeEvents[0]
	if closeEv.Type != ConnEventClose {
		t.Fatalf("second event must be close, got %+v", closeEv)
	}
	if closeEv.ClientIP != "203.0.113.99" || closeEv.PeerIP != "127.0.0.1" {
		t.Fatalf("close event must repeat ClientIP 203.0.113.99 / PeerIP 127.0.0.1, got %+v", closeEv)
	}
}

func TestConnLogPeerIPEmptyForDirectConn(t *testing.T) {
	server, client := realTCPPair(t, "")
	defer client.Close()
	ring, h, done := serveFedConn(t, server)
	waitHandled(t, h, 1)
	events := waitConnEvents(t, ring, 1)
	open := events[0]
	if open.Type != ConnEventOpen {
		t.Fatalf("first event must be open, got %+v", open)
	}
	if open.ClientIP != "127.0.0.1" {
		t.Fatalf("ClientIP must be the real peer 127.0.0.1, got %q", open.ClientIP)
	}
	if open.PeerIP != "" {
		t.Fatalf("PeerIP must stay empty for a direct connection, got %q", open.PeerIP)
	}
	done()
	// Drain the close event inside this test: the handler
	// goroutine enqueues it after Handle returns, and an
	// undrained event would land in the next test's fresh ring
	// (the reporter is global) and pollute its first read.
	closeEvents := waitConnEvents(t, ring, 1)
	closeEv := closeEvents[0]
	if closeEv.Type != ConnEventClose {
		t.Fatalf("second event must be close, got %+v", closeEv)
	}
	if closeEv.PeerIP != "" {
		t.Fatalf("close event PeerIP must stay empty for a direct connection, got %q", closeEv.PeerIP)
	}
}

// TestConnLogPeerIPViaRealTCPListener is the joint path: the real
// tcp listener (panel-shaped config: ProxyProtocol + allowlist)
// feeds Serve, so the PROXY claim is honoured by the allowlisted
// listener and the raw peer is still recorded — this is the exact
// production path, wrappers included.
func TestConnLogPeerIPViaRealTCPListener(t *testing.T) {
	ring := freshConnlogRing(t)
	SetConnLogEnabled(true)

	ln := tcplistener.NewListener(
		listener.AddrOption("127.0.0.1:0"),
		listener.ProxyProtocolOption(1),
		listener.LoggerOption(xlogger.Nop()),
	)
	if err := ln.Init(xmetadata.NewMetadata(map[string]any{"proxyProtocolAllowlist": []any{"127.0.0.1"}})); err != nil {
		t.Fatalf("init listener: %v", err)
	}
	h := &gateHandler{release: make(chan struct{})}
	svc := NewService("fwd_entry_tcp", ln, h,
		LoggerOption(xlogger.NewLogger()),
		ConnLogEntryOption(true, "fwd_entry_tcp"))
	go func() { _ = svc.Serve() }()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, err := fmt.Fprint(client, "PROXY TCP4 203.0.113.99 198.51.100.7 45678 4001\r\n"); err != nil {
		t.Fatalf("write PROXY header: %v", err)
	}
	waitHandled(t, h, 1)
	events := waitConnEvents(t, ring, 1)
	open := events[0]
	if open.ClientIP != "203.0.113.99" {
		t.Fatalf("ClientIP must be the claimed 203.0.113.99, got %q", open.ClientIP)
	}
	if open.PeerIP != "127.0.0.1" {
		t.Fatalf("PeerIP must be the raw peer 127.0.0.1, got %q", open.PeerIP)
	}
	close(h.release)
	_ = svc.Close()
	// Drain the close event inside this test (see
	// TestConnLogPeerIPEmptyForDirectConn): an undrained event
	// would land in the next test's fresh ring.
	closeEvents := waitConnEvents(t, ring, 1)
	closeEv := closeEvents[0]
	if closeEv.Type != ConnEventClose {
		t.Fatalf("second event must be close, got %+v", closeEv)
	}
	if closeEv.ClientIP != "203.0.113.99" || closeEv.PeerIP != "127.0.0.1" {
		t.Fatalf("close event must repeat ClientIP 203.0.113.99 / PeerIP 127.0.0.1, got %+v", closeEv)
	}
}
