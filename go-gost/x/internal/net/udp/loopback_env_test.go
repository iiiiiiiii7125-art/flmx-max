package udp

import (
	"net"
	"testing"
	"time"
)

// requireLoopbackUDP is the environment self-check for the
// real-socket contract tests in this package (O-S2-1,
// FIX-UDP-RETURN 口径): a contract test that cannot run in its
// environment must SKIP with a note, never impersonate a product
// failure.
//
// The check is a direct loopback echo in the exact shape the
// tests use — connected DialUDP + Write + ReadFrom on
// 127.0.0.1:0. Deliberately NOT the unconnected WriteTo shape:
// this sandbox blocks unconnected sendto to loopback ephemeral
// ports with EPERM while connected loopback works, and no test
// in this package uses WriteTo — a WriteTo self-check would
// wrongly SKIP tests that can actually run here.
func requireLoopbackUDP(t *testing.T) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback UDP unavailable in this environment (O-S2-1 self-check): ListenPacket: %v", err)
	}
	defer pc.Close()
	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Skipf("loopback UDP unavailable in this environment (O-S2-1 self-check): DialUDP: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("loopback-echo")); err != nil {
		t.Skipf("loopback UDP send blocked in this environment (O-S2-1 self-check): %v", err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 32)
	if _, _, err := pc.ReadFrom(buf); err != nil {
		t.Skipf("loopback UDP echo not received in this environment (O-S2-1 self-check): %v", err)
	}
}
