package socket

// R7-C-02 (agent side): probeReferenceConnectRTT must judge its
// target before any dial — hard-deny always, private ranges
// rejected by default. A node must not become a blind SSRF /
// port-scan primitive for the panel-side caller.

import (
	"context"
	"net"
	"testing"
	"time"
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
	if _, err := probeReferenceConnectRTT(context.Background(), ln.Addr().String()); err == nil {
		t.Fatal("loopback reference host must be rejected, not dialed")
	}
	if _, err := probeReferenceConnectRTT(context.Background(), "169.254.169.254:80"); err == nil {
		t.Fatal("metadata reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT(context.Background(), "10.0.0.5:443"); err == nil {
		t.Fatal("private reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT(context.Background(), "8.8.8.8:0"); err == nil {
		t.Fatal("port 0 reference host must be rejected")
	}
	if _, err := probeReferenceConnectRTT(context.Background(), "8.8.8.8:70000"); err == nil {
		t.Fatal("port 70000 reference host must be rejected")
	}
}

// R18-D2-09: a probe round must honor its caller's context. A
// pre-cancelled context returns immediately with the partial
// stats (nothing sampled) instead of running out the full
// 10-sample round (~900ms of sleeps); a mid-series cancel stops
// at the next sample boundary.
func TestProbeReferenceSeriesHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	st, err := probeReferenceSeries(ctx, "8.8.8.8:81")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("probeReferenceSeries with a cancelled ctx = %v, want the partial stats and a nil error", err)
	}
	if st.Sent != 0 {
		t.Fatalf("probeReferenceSeries with a cancelled ctx sent %d samples, want 0", st.Sent)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("probeReferenceSeries with a cancelled ctx took %v, want an immediate return", elapsed)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel2()
	}()
	start = time.Now()
	st2, err := probeReferenceSeries(ctx2, "8.8.8.8:81")
	elapsed = time.Since(start)
	if err != nil {
		t.Fatalf("probeReferenceSeries with a mid-series cancel = %v, want the partial stats and a nil error", err)
	}
	if st2.Sent == 0 || st2.Sent >= probeSeriesCount {
		t.Fatalf("mid-series cancel sent %d samples, want a partial round (1..9)", st2.Sent)
	}
	if elapsed > 700*time.Millisecond {
		t.Fatalf("mid-series cancel took %v, want the round to stop at the next sample boundary", elapsed)
	}
}
