package relay_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/handler"
	xchain "github.com/go-gost/x/chain"
	relayconnector "github.com/go-gost/x/connector/relay"
	relayhandler "github.com/go-gost/x/handler/relay"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// F-212-B regression gates: connection establishment through a
// relay pair must not be gated on the first payload byte in either
// direction. The panel renders no "nodelay" metadata for tcp chain
// segments, so both the connector and the handler run in delayed
// mode: the connector cached its request header until the first
// application Write, and the handler cached its response header
// until the first Write in the reverse direction. A server-first
// target (game login servers that send an INIT packet on accept
// and then wait) therefore never saw the request at all: the
// entry's handshake sat in a buffer while the exit waited for it,
// until the exit's 15s request-header read deadline killed the
// hop — 0 bytes delivered, closed by peer at ~15,001ms, reproduced
// live on the 2.1.4 agent. The mirror combination (eager connector
// x delayed handler) deadlocked client-first traffic the same
// way: Connect drains the response before any payload can flow,
// and the cached response only left on the target's first byte,
// which itself waits for that payload.
//
// These tests run the production connector and handler over real
// loopback TCP. Every gate uses a 3s bound: far above the
// loopback-scale establishment time, far below the 15s signature.

var serverFirstINIT = bytes.Repeat([]byte("INIT"), 50) // 200 bytes

// startServerFirstTarget accepts, immediately sends the INIT
// packet, then echoes whatever the client sends afterwards.
func startServerFirstTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				if _, err := conn.Write(serverFirstINIT); err != nil {
					return
				}
				io.Copy(conn, conn)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// startEchoTarget accepts and echoes (pure client-first peer).
func startEchoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				io.Copy(conn, conn)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// startRelayHandler serves the production relay handler with the
// given metadata over loopback TCP, dialling targets directly.
func startRelayHandler(t *testing.T, handlerMd map[string]any) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	h := relayhandler.NewHandler(
		handler.RouterOption(xchain.NewRouter(chain.LoggerRouterOption(xlogger.Nop()))),
		handler.LoggerOption(xlogger.Nop()),
		handler.ServiceOption("test-relay-server-first"),
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
	return ln.Addr().String()
}

// dialViaRelay connects through the relay pair and returns the
// established stream. Connect runs under a 3s guard: an eager
// connector drains the response inside Connect, so a gated pair
// hangs there rather than returning an error.
func dialViaRelay(t *testing.T, relayAddr, target string, connectorMd map[string]any) net.Conn {
	t.Helper()
	raw, err := net.Dial("tcp", relayAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })

	c := relayconnector.NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := c.Init(mdx.NewMetadata(connectorMd)); err != nil {
		t.Fatal(err)
	}

	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		cc, err := c.Connect(context.Background(), raw, "tcp", target)
		ch <- result{cc, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("relay Connect: %v", r.err)
		}
		t.Cleanup(func() { r.conn.Close() })
		return r.conn
	case <-time.After(3 * time.Second):
		t.Fatal("relay Connect did not return within 3s: establishment is gated on a first byte that cannot flow yet")
		return nil
	}
}

// readServerFirstINIT reads before writing anything: the client
// stays silent, so the INIT can only arrive if establishment was
// not gated on a client byte.
func readServerFirstINIT(t *testing.T, cc net.Conn) {
	t.Helper()
	if err := cc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer cc.SetReadDeadline(time.Time{})

	start := time.Now()
	got := make([]byte, len(serverFirstINIT))
	if _, err := io.ReadFull(cc, got); err != nil {
		t.Fatalf("server-first INIT not delivered to a silent client: %v (F-212-B signature: 0 bytes until the hop is killed)", err)
	}
	if !bytes.Equal(got, serverFirstINIT) {
		t.Fatalf("INIT payload corrupted: got %q", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("INIT took %v, want loopback scale", elapsed)
	}
}

func echoRoundTrip(t *testing.T, cc net.Conn, payload []byte) {
	t.Helper()
	if err := cc.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer cc.SetReadDeadline(time.Time{})

	if _, err := cc.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(cc, got); err != nil {
		t.Fatalf("echo Read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo corrupted: got %q want %q", got, payload)
	}
}

// The production shape for a tcp chain segment: delayed
// connector x delayed handler, server-first target.
func TestRelayServerFirstDelayedPair(t *testing.T) {
	target := startServerFirstTarget(t)
	relayAddr := startRelayHandler(t, map[string]any{})

	cc := dialViaRelay(t, relayAddr, target, map[string]any{})
	readServerFirstINIT(t, cc)
	// The stream must be fully usable after a read-driven
	// handshake, in both directions.
	echoRoundTrip(t, cc, []byte("after-init"))
}

// Client-first mirror through the same delayed pair: the
// pre-existing working shape, kept as a no-regression guard.
func TestRelayClientFirstDelayedPair(t *testing.T) {
	target := startEchoTarget(t)
	relayAddr := startRelayHandler(t, map[string]any{})

	cc := dialViaRelay(t, relayAddr, target, map[string]any{})
	echoRoundTrip(t, cc, []byte("client-first"))
}

// Delayed connector x eager handler, server-first: the handler
// side does its part eagerly, so only the connector's cached
// request header can gate establishment.
func TestRelayServerFirstDelayedConnectorEagerHandler(t *testing.T) {
	target := startServerFirstTarget(t)
	relayAddr := startRelayHandler(t, map[string]any{"nodelay": true})

	cc := dialViaRelay(t, relayAddr, target, map[string]any{})
	readServerFirstINIT(t, cc)
	echoRoundTrip(t, cc, []byte("after-init"))
}

// Eager connector x delayed handler, client-first: Connect
// drains the response before returning, and the delayed handler
// cached that response behind the target's first byte — which
// waits for client payload that cannot be sent until Connect
// returns. Establishment must complete anyway.
func TestRelayClientFirstEagerConnectorDelayedHandler(t *testing.T) {
	target := startEchoTarget(t)
	relayAddr := startRelayHandler(t, map[string]any{})

	cc := dialViaRelay(t, relayAddr, target, map[string]any{"nodelay": true})
	echoRoundTrip(t, cc, []byte("client-first"))
}
