package relay_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/go-gost/core/connector"
	relayconnector "github.com/go-gost/x/connector/relay"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// F-UDP-2b regression: a TCP payload tunnelled over a UDP segment
// (datagram transport). The topology is identical to
// TestRelayOverUDPMultiRoundTrip except that the payload network is
// TCP: the relay server merges its response with the first echo
// frame into one datagram, and the connector must not lose that
// frame to its response drain. Before the fix the stream wrapper
// was keyed on the payload network only, so for a TCP payload over
// a datagram transport the first frame was discarded
// deterministically and every such connection hung on its first
// read (the SEG-udp live failure).
func TestRelayTCPOverUDPTransportRoundTrip(t *testing.T) {
	hub := newMemHub()
	const target = "127.0.0.1:5311"
	router := stubRouter{targets: map[string]func() (net.Conn, error){target: newEchoTarget(hub, "ECHO:")}}
	server := startRelayServer(t, hub, nil, router)

	transport := hub.endpoint()
	transport.connect(server)
	t.Cleanup(func() { transport.Close() })

	c := relayconnector.NewConnector(connector.LoggerOption(xlogger.Nop()))
	if err := c.Init(mdx.NewMetadata(map[string]any{})); err != nil {
		t.Fatal(err)
	}
	cc, err := c.Connect(context.Background(), transport, "tcp", target)
	if err != nil {
		t.Fatalf("relay Connect: %v", err)
	}
	t.Cleanup(func() { cc.Close() })

	for i := 0; i < 4; i++ {
		payload := []byte(fmt.Sprintf("tcp-pkt-%d", i))
		got := roundTrip(t, cc, payload)
		if want := append([]byte("ECHO:"), payload...); !bytes.Equal(got, want) {
			t.Fatalf("round trip %d: got %q, want %q", i, got, want)
		}
	}
}
