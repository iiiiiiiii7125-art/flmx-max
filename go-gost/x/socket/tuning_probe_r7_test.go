package socket

// R7-C-02 (agent side): probeSpeedtestDownload and
// probeReferenceConnectRTT must judge their targets before any dial —
// hard-deny always, private ranges rejected by default, redirect
// hops re-judged per hop. A node must not become a blind SSRF /
// port-scan primitive for the panel-side caller.

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestR7C02SpeedtestLoopbackRejected(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("speedtest-payload"))
	}))
	defer srv.Close()
	if _, _, err := probeSpeedtestDownload(context.Background(), srv.URL+"/file.bin"); err == nil {
		t.Fatal("loopback speedtest URL must be rejected")
	}
	if hits.Load() != 0 {
		t.Fatalf("loopback target was contacted %d times, want 0", hits.Load())
	}
}

func TestR7C02SpeedtestMetadataRejected(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest",
		"http://[64:ff9b::a9fe:a9fe]/latest",
		"http://example.com@169.254.169.254/f",
		"http://10.0.0.5/file.bin",
	} {
		if _, _, err := probeSpeedtestDownload(context.Background(), u); err == nil {
			t.Errorf("%s: must be rejected", u)
		}
	}
}

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
