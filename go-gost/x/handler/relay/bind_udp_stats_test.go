package relay

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/observer"
	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/relay"
	ctxvalue "github.com/go-gost/x/ctx"
	relay_util "github.com/go-gost/x/internal/util/relay"
	xlogger "github.com/go-gost/x/logger"
	mdx "github.com/go-gost/x/metadata"
)

// discardObserver satisfies observer.Observer; the handler only needs
// a non-nil Observer for h.stats to exist, and this test reads the
// per-client counters straight from h.stats.
type discardObserver struct{}

func (discardObserver) Observe(ctx context.Context, events []observer.Event, opts ...observer.Option) error {
	return nil
}

// requireBindLoopbackUDP is the environment self-check for the BIND
// UDP stats test (FIX-UDP-RETURN 口径): the relay's endpoint socket
// reaches its target with an unconnected WriteTo to a loopback
// ephemeral port, and this sandbox blocks exactly that shape with
// EPERM. A test that cannot run here must SKIP with a note, never
// impersonate a product failure. The probe uses the exact shape the
// test drives — unconnected WriteTo + echo back — so a SKIP here
// means the test itself could not have run.
func requireBindLoopbackUDP(t *testing.T) {
	t.Helper()

	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback UDP unavailable (bind-UDP self-check): ListenPacket: %v", err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			echo.WriteTo(buf[:n], addr)
		}
	}()

	sender, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback UDP unavailable (bind-UDP self-check): sender ListenPacket: %v", err)
	}
	defer sender.Close()
	if _, err := sender.WriteTo([]byte("bind-udp-probe"), echo.LocalAddr()); err != nil {
		t.Skipf("loopback UDP send blocked in this environment (bind-UDP self-check, unconnected WriteTo): %v", err)
	}
	_ = sender.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if _, _, err := sender.ReadFrom(buf); err != nil {
		t.Skipf("loopback UDP echo not received in this environment (bind-UDP self-check): %v", err)
	}

	// The relay control plane is TCP loopback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback TCP unavailable (bind-UDP self-check): %v", err)
	}
	ln.Close()
}

// TestRelayBindUDPObserverStats pins the metering caliber of relay
// BIND UDP (A-低-4): the UDP data plane runs over the TCP control
// connection as framed datagrams (connector/relay bindUDP wraps the
// control conn in UDPTunClientPacketConn; the handler side unwraps
// it in UDPTunServerConn), and handleBind wraps that control conn
// with the per-client observer stats — so each payload byte is
// counted exactly once, plus ~10B of SOCKS UDP header per datagram
// and the BIND response. Wrapping the endpoint PacketConn with the
// same per-client stats as well would count every payload byte a
// second time, so this test asserts the single-counting window:
// 10 x 100B payloads per direction must land in [1000, 2000).
func TestRelayBindUDPObserverStats(t *testing.T) {
	requireBindLoopbackUDP(t)

	// UDP echo target: a real datagram round-trip, not a stub.
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo ListenPacket: %v", err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := echo.WriteTo(buf[:n], addr); err != nil {
				return
			}
		}
	}()

	h := NewHandler(
		handler.ServiceOption("test-relay-bind-udp"),
		handler.ObserverOption(discardObserver{}),
		handler.LoggerOption(xlogger.Nop()),
	)
	if err := h.Init(mdx.NewMetadata(map[string]any{"bind": true})); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rh := h.(*relayHandler)
	defer rh.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("control Listen: %v", err)
	}
	defer ln.Close()

	ctx := ctxvalue.ContextWithClientID(context.Background(), ctxvalue.ClientID("test-client"))
	handleErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			handleErr <- err
			return
		}
		handleErr <- rh.Handle(ctx, conn)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("control Dial: %v", err)
	}
	defer client.Close()

	af := &relay.AddrFeature{}
	if err := af.ParseFrom("127.0.0.1:0"); err != nil {
		t.Fatalf("AddrFeature: %v", err)
	}
	req := relay.Request{
		Version: relay.Version1,
		Cmd:     relay.CmdBind | relay.FUDP,
		Features: []relay.Feature{
			af,
			&relay.NetworkFeature{Network: relay.NetworkUDP},
		},
	}
	if _, err := req.WriteTo(client); err != nil {
		t.Fatalf("write BIND request: %v", err)
	}
	resp := &relay.Response{}
	if _, err := resp.ReadFrom(client); err != nil {
		t.Fatalf("read BIND response: %v", err)
	}
	if resp.Status != relay.StatusOK {
		t.Fatalf("BIND status = %d, want %d (OK)", resp.Status, relay.StatusOK)
	}

	const datagrams = 10
	const payloadLen = 100
	payload := bytes.Repeat([]byte("x"), payloadLen)
	tun := relay_util.UDPTunClientPacketConn(client)
	buf := make([]byte, 2048)
	for i := 0; i < datagrams; i++ {
		if _, err := tun.WriteTo(payload, echo.LocalAddr()); err != nil {
			t.Fatalf("datagram %d WriteTo: %v", i, err)
		}
		_ = tun.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := tun.ReadFrom(buf)
		if err != nil {
			t.Fatalf("datagram %d ReadFrom: %v", i, err)
		}
		if n != payloadLen || !bytes.Equal(buf[:n], payload) {
			t.Fatalf("datagram %d: got %d bytes, want %d echo bytes", i, n, payloadLen)
		}
	}

	// Closing the control connection ends the BIND association.
	client.Close()
	select {
	case err := <-handleErr:
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Handle did not return after control connection close")
	}

	st := rh.stats.Stats("test-client")
	in := st.Get(stats.KindInputBytes)
	out := st.Get(stats.KindOutputBytes)
	total := st.Get(stats.KindTotalConns)
	current := st.Get(stats.KindCurrentConns)
	t.Logf("bindUDP observer stats: input=%d output=%d totalConns=%d currentConns=%d (payload %dB/direction)",
		in, out, total, current, datagrams*payloadLen)

	if in < datagrams*payloadLen || out < datagrams*payloadLen {
		t.Fatalf("relay BIND UDP did not enter observer stats: input=%d output=%d, want >= %d each",
			in, out, datagrams*payloadLen)
	}
	if in >= 2*datagrams*payloadLen || out >= 2*datagrams*payloadLen {
		t.Fatalf("relay BIND UDP double-counted in observer stats: input=%d output=%d, want < %d each",
			in, out, 2*datagrams*payloadLen)
	}
	if total != 1 {
		t.Fatalf("totalConns = %d, want 1", total)
	}
	if current != 0 {
		t.Fatalf("currentConns = %d, want 0 after association end", current)
	}
}
