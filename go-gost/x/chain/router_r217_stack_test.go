package chain_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	"github.com/go-gost/core/dialer"
	"github.com/go-gost/core/handler"
	corelogger "github.com/go-gost/core/logger"
	xchain "github.com/go-gost/x/chain"
	relayconnector "github.com/go-gost/x/connector/relay"
	tcpdialer "github.com/go-gost/x/dialer/tcp"
	relayhandler "github.com/go-gost/x/handler/relay"
	xhop "github.com/go-gost/x/hop"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
	xs "github.com/go-gost/x/selector"
)

// AUDIT-R217 B-01(b): real-stack long-lived connection experiment.
// The full panel stack shape — client Router → chain (one hop, one
// node) → tcp dialer + relay connector → production relay handler →
// its own Router direct-dials the target — with a 2.4s envelope and
// N=3 attempts, so the single successful attempt's slice is ≈800ms.
// Bytes are pushed across the slice moment (~0.8s) and across the
// envelope end (2.4s): if an established connection were governed by
// the attempt context, one of the two round trips would fail.
func TestRouterR217RealStackConnOutlivesSliceAndEnvelope(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	// Echo target.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()
	echoAddr := echoLn.Addr().String()

	// Relay handler (next hop), direct-dialling targets via a plain
	// Router with no chain — the panel's relay-service shape.
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relayLn.Close()
	rh := relayhandler.NewHandler(
		handler.RouterOption(xchain.NewRouter(corechain.LoggerRouterOption(xlogger.Nop()))),
		handler.LoggerOption(xlogger.Nop()),
		handler.ServiceOption("r217-b01b-relay"),
	)
	if err := rh.Init(mdx.NewMetadata(nil)); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := relayLn.Accept()
			if err != nil {
				return
			}
			go rh.Handle(context.Background(), c)
		}
	}()

	// Client chain: one node = tcp dialer + relay connector.
	connector := relayconnector.NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := connector.Init(mdx.NewMetadata(nil)); err != nil {
		t.Fatal(err)
	}
	tr := xchain.NewTransport(
		tcpdialer.NewDialer(dialer.LoggerOption(xlogger.Nop())),
		connector,
	)
	node := corechain.NewNode("node-1", relayLn.Addr().String(), corechain.TransportNodeOption(tr))
	h := xhop.NewHop(
		xhop.NodeOption(node),
		xhop.SelectorOption(xs.NewSelector[*corechain.Node](xs.FIFOStrategy[*corechain.Node]())),
		xhop.LoggerOption(xlogger.Nop()),
	)
	c := xchain.NewChain("r217-b01b")
	c.AddHop(h)
	r := xchain.NewRouter(
		corechain.ChainRouterOption(c),
		corechain.TimeoutRouterOption(2400*time.Millisecond),
		corechain.RetriesRouterOption(2),
		corechain.LoggerRouterOption(xlogger.Nop()),
	)

	t0 := time.Now()
	conn, err := r.Dial(context.Background(), "tcp", echoAddr)
	if err != nil {
		t.Fatalf("Dial through the relay stack: %v", err)
	}
	defer conn.Close()

	roundTrip := func(at time.Duration, payload string) {
		t.Helper()
		if d := time.Until(t0.Add(at)); d > 0 {
			time.Sleep(d)
		}
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		defer conn.SetDeadline(time.Time{})
		if _, err := conn.Write([]byte(payload)); err != nil {
			t.Fatalf("write at %v after dial: %v", time.Since(t0), err)
		}
		buf := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read at %v after dial: %v", time.Since(t0), err)
		}
		if string(buf) != payload {
			t.Fatalf("echo mismatch at %v: got %q want %q", time.Since(t0), buf, payload)
		}
		t.Logf("round trip OK at %v (payload %q)", time.Since(t0), payload)
	}

	roundTrip(1200*time.Millisecond, "past-slice")    // slice ≈800ms
	roundTrip(2900*time.Millisecond, "past-envelope") // envelope = 2.4s
}
