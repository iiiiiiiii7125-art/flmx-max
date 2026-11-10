package chain

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// Package K regression pin for the tunnel_49 signature (ECS1686
// field case): one tunnel's session log showed opens = 2x its
// siblings' while closes matched the siblings — half its
// sessions opened and never closed, net-accumulating ~1.4
// sessions/s until the agent's RSS walked into the wall.
//
// Mechanism: the previous hop's route dial for that tunnel
// failed at the handshake stage after this side had accepted,
// the router retried and succeeded — but the failed attempt's
// freshly connected conn was never closed (route.go closed only
// the substrate), so the accepted session on this side parked
// forever: no FIN ever arrives, and TCP keepalive cannot help
// because the leaked socket's kernel on the dialing side keeps
// answering probes for a conn its userspace forgot.
// opens = real + orphan, closes = real: exactly 2:1 whenever
// every first attempt fails this way.
//
// This test reproduces the signature at the route layer: a
// two-hop route whose second hop handshake fails once
// (ws-style: the transport does not close its input on failure)
// and succeeds on the retry. The peer (a real listener standing
// in for the next hop's agent) must observe BOTH accepted conns
// close: the failed attempt's via the route's own cleanup, the
// successful one via the caller. opens 2, closes 2 — balanced.

var errFlakyHandshake = errors.New("flaky handshake failure")

type peerDialTransport struct {
	stubTransport
	target string
}

// Dial returns a real pipe end as the substrate (the shared
// stubTransport returns nil, which no real transport does and
// which the route's failure cleanup cannot Close).
func (t peerDialTransport) Dial(context.Context, string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

func (t peerDialTransport) Connect(_ context.Context, _ net.Conn, _, _ string) (net.Conn, error) {
	return net.Dial("tcp", t.target)
}

type flakyHandshakeTransport struct {
	stubTransport
	attempts *atomic.Int32
}

func (t flakyHandshakeTransport) Handshake(_ context.Context, conn net.Conn) (net.Conn, error) {
	if t.attempts.Add(1) == 1 {
		// ws-style failure: input ownership stays with the
		// caller; this transport does not close it.
		return nil, errFlakyHandshake
	}
	return conn, nil
}

func TestHandshakeFailureAttemptIsClosedForRetryBalance(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	var attempts atomic.Int32
	c := NewChain("tunnel49-shape")
	c.AddHop(fixedHop{node: corechain.NewNode("hop-a", "127.0.0.1:1",
		corechain.TransportNodeOption(peerDialTransport{target: ln.Addr().String()}))})
	c.AddHop(fixedHop{node: corechain.NewNode("hop-b", ln.Addr().String(),
		corechain.TransportNodeOption(flakyHandshakeTransport{attempts: &attempts}))})
	rt := c.Route(context.Background(), "tcp", "example.com:443")
	if rt == nil || len(rt.Nodes()) != 2 {
		t.Fatalf("expected a two-node route, got %v", rt)
	}

	// Attempt 1: handshake fails after the peer accepted.
	if _, err := rt.Dial(context.Background(), "tcp", "example.com:443"); !errors.Is(err, errFlakyHandshake) {
		t.Fatalf("attempt 1 err = %v, want the flaky handshake error", err)
	}
	peer1 := <-accepted
	peer1.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer1.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("failed attempt's peer conn read err = %v, want io.EOF — the attempt's conn was leaked, its session parks (the 2:1 signature)", err)
	}
	peer1.Close()

	// Attempt 2 (the router's retry): succeeds end to end.
	conn, err := rt.Dial(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	peer2 := <-accepted
	if _, err := conn.Write([]byte{0x7}); err != nil {
		t.Fatal(err)
	}
	peer2.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := peer2.Read(buf); err != nil || buf[0] != 0x7 {
		t.Fatalf("peer2 read: err=%v byte=%v", err, buf)
	}
	conn.Close()
	peer2.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer2.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("successful session's peer conn read err = %v, want io.EOF after caller close", err)
	}
	peer2.Close()

	if got := attempts.Load(); got != 2 {
		t.Fatalf("handshake attempts = %d, want 2", got)
	}
	// opens 2 (peer accepted twice), closes 2 (both observed
	// EOF above): the open/close ledger balances.
}
