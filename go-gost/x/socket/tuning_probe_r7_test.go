package socket

// R7-C-02 (agent side): probeReferenceConnectRTT must judge its
// target before any dial — hard-deny always, private ranges
// rejected by default. A node must not become a blind SSRF /
// port-scan primitive for the panel-side caller.

import (
	"net"
	"testing"
)

func TestR7C02ReferenceDialNonPublicRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if _, err := probeReferenceConnectRTT(ln.Addr().String()); err == nil {
		t.Fatal("loopback reference host must be rejected, not dialed")
	}
	if _, err := probeReferenceConnectRTT("169.254.169.254:80"); err == nil {
		t.Fatal("metadata reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT("10.0.0.5:443"); err == nil {
		t.Fatal("private reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT("8.8.8.8:0"); err == nil {
		t.Fatal("port 0 reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT("8.8.8.8:70000"); err == nil {
		t.Fatal("port 70000 reference host must be rejected")
	}
}
