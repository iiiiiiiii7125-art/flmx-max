package ss

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-gost/core/chain"
	"github.com/go-gost/core/handler"
	"github.com/go-gost/core/recorder"
	ssutil "github.com/go-gost/x/internal/util/ss"
	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// requireSSLoopbackUDP is the environment self-check for the SS-UDP
// stats test (FIX-UDP-RETURN caliber, same shape as
// bind_udp_stats_test.go's requireBindLoopbackUDP): the test drives
// an unconnected WriteTo to a loopback ephemeral port plus an echo
// back, and this sandbox blocks exactly that shape with EPERM. A
// test that cannot run here must SKIP with a note, never impersonate
// a product failure. The probe uses the exact shape the test drives,
// so a SKIP here means the test itself could not have run.
func requireSSLoopbackUDP(t *testing.T) {
	t.Helper()

	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback UDP unavailable (ss-UDP self-check): ListenPacket: %v", err)
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
		t.Skipf("loopback UDP unavailable (ss-UDP self-check): sender ListenPacket: %v", err)
	}
	defer sender.Close()
	if _, err := sender.WriteTo([]byte("ss-udp-probe"), echo.LocalAddr()); err != nil {
		t.Skipf("loopback UDP send blocked in this environment (ss-UDP self-check, unconnected WriteTo): %v", err)
	}
	_ = sender.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	if _, _, err := sender.ReadFrom(buf); err != nil {
		t.Skipf("loopback UDP echo not received in this environment (ss-UDP self-check): %v", err)
	}
}

// ssStatsCaptureRecorder keeps every recorder payload the handler
// records, so the test can read the HandlerRecorderObject the
// association produced.
type ssStatsCaptureRecorder struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (r *ssStatsCaptureRecorder) Record(_ context.Context, b []byte, _ ...recorder.RecordOption) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, append([]byte(nil), b...))
	return nil
}

func (r *ssStatsCaptureRecorder) recorded(t *testing.T) []xrecorder.HandlerRecorderObject {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]xrecorder.HandlerRecorderObject, 0, len(r.payloads))
	for _, p := range r.payloads {
		var ro xrecorder.HandlerRecorderObject
		if err := json.Unmarshal(p, &ro); err != nil {
			t.Fatalf("recorded payload is not a HandlerRecorderObject: %v (%s)", err, p)
		}
		out = append(out, ro)
	}
	return out
}

// ssTestServerConn models the per-client connection the UDP listener
// hands the handler in production: an unconnected socket (so WriteTo
// to any address works) with RemoteAddr fixed to the client. A
// connected UDP socket cannot be used here: its WriteTo fails with
// "use of WriteTo with pre-connected connection", which is exactly
// how the first version of this test lost every reply.
type ssTestServerConn struct {
	*net.UDPConn
	remote net.Addr
}

func (c *ssTestServerConn) RemoteAddr() net.Addr { return c.remote }

// fresh unconnected UDP socket, and the handler's relay writes each
// datagram to the echo address taken from the client's SOCKS UDP
// header. Bind is unused by this handler.
type ssEchoRouter struct{}

func (ssEchoRouter) Options() *chain.RouterOptions { return &chain.RouterOptions{} }

func (ssEchoRouter) Dial(_ context.Context, _, _ string) (net.Conn, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	// *net.UDPConn is both net.Conn and net.PacketConn; the
	// handler asserts the latter.
	return pc.(*net.UDPConn), nil
}

func (ssEchoRouter) Bind(_ context.Context, _, _ string, _ ...chain.BindOption) (net.Listener, error) {
	return nil, errors.New("ss udp stats test: Bind unused")
}

// TestSSUDPRecorderStats pins the metering caliber of SS-UDP
// (F3-A4SS, same family as A-低-4): before the fix the handler
// recorded every SS-UDP association with InputBytes == 0 and
// OutputBytes == 0 — the two copy loops moved packets but never
// counted them, and neither PacketConn leg had a stats wrapper.
// The client-facing leg is now wrapped with WrapPacketConn (the
// SOCKS5-UDP precedent), so 10 x 100B payloads per direction must
// land exactly in the recorded object: input == output == 1000.
// Exact equality holds because the wrapper sits outside the cipher
// and the SOCKS UDP header framing, so it counts payload bytes per
// datagram with no per-datagram overhead at this layer; the
// upstream leg stays unwrapped, so nothing is counted twice.
func TestSSUDPRecorderStats(t *testing.T) {
	requireSSLoopbackUDP(t)
	setSSRelayWindow(t, 300*time.Millisecond)

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

	capture := &ssStatsCaptureRecorder{}
	h := NewHandler(
		handler.AuthOption(url.UserPassword("aes-256-cfb", "test-password")),
		handler.ServiceOption("test-ss-udp"),
		handler.LoggerOption(xlogger.Nop()),
		handler.RouterOption(ssEchoRouter{}),
		handler.RecordersOption(recorder.RecorderObject{
			Record:   xrecorder.RecorderServiceHandler,
			Recorder: capture,
		}),
	)
	if err := h.Init(nil); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Client-side raw socket first; the server side is an unconnected
	// socket whose RemoteAddr is the client — the shape the UDP
	// listener hands the handler in production.
	clientRaw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client ListenPacket: %v", err)
	}
	defer clientRaw.Close()

	serverRaw, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("server ListenPacket: %v", err)
	}
	defer serverRaw.Close()
	serverConn := &ssTestServerConn{UDPConn: serverRaw.(*net.UDPConn), remote: clientRaw.LocalAddr()}

	handleErr := make(chan error, 1)
	go func() {
		handleErr <- h.Handle(context.Background(), serverConn)
	}()

	// Real SS-UDP client: cipher + SOCKS UDP framing, same as a
	// production client. Same method/password/key as the handler
	// (Init(nil) leaves the key empty).
	cipher, err := ssutil.ShadowCipher("aes-256-cfb", "test-password", "")
	if err != nil {
		t.Fatalf("ShadowCipher: %v", err)
	}
	client := ssutil.UDPClientConn(cipher.PacketConn(clientRaw), serverConn.LocalAddr(), echo.LocalAddr())

	const datagrams = 10
	const payloadLen = 100
	payload := bytes.Repeat([]byte("x"), payloadLen)
	buf := make([]byte, 2048)
	for i := 0; i < datagrams; i++ {
		if _, err := client.Write(payload); err != nil {
			t.Fatalf("datagram %d Write: %v", i, err)
		}
		_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := client.Read(buf)
		if err != nil {
			t.Fatalf("datagram %d Read: %v", i, err)
		}
		if n != payloadLen || !bytes.Equal(buf[:n], payload) {
			t.Fatalf("datagram %d: got %d bytes, want %d echo bytes", i, n, payloadLen)
		}
	}

	// No more traffic: the armed idle window reaps the association
	// and Handle returns, which is when ro is recorded.
	select {
	case err := <-handleErr:
		if err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Handle did not return after the idle window reaped the association")
	}

	ros := capture.recorded(t)
	if len(ros) != 1 {
		t.Fatalf("recorded %d handler objects, want exactly 1", len(ros))
	}
	ro := ros[0]
	t.Logf("ss-udp recorder stats: input=%d output=%d network=%s service=%s (payload %dB/direction)",
		ro.InputBytes, ro.OutputBytes, ro.Network, ro.Service, datagrams*payloadLen)

	if ro.Network != "udp" {
		t.Fatalf("ro.Network = %q, want %q", ro.Network, "udp")
	}
	if ro.Service != "test-ss-udp" {
		t.Fatalf("ro.Service = %q, want %q", ro.Service, "test-ss-udp")
	}
	if ro.InputBytes != datagrams*payloadLen {
		t.Fatalf("SS-UDP input not counted: input=%d, want %d (payload bytes client->remote)",
			ro.InputBytes, datagrams*payloadLen)
	}
	if ro.OutputBytes != datagrams*payloadLen {
		t.Fatalf("SS-UDP output not counted: output=%d, want %d (payload bytes remote->client)",
			ro.OutputBytes, datagrams*payloadLen)
	}
}
