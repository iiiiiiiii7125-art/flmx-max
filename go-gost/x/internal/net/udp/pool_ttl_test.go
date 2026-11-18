package udp

import (
	"net"
	"testing"
	"time"
)

// D3-01 regression: a non-positive TTL must never reach
// time.NewTicker (it panics), and the panic would fire inside the
// pool's idleCheck goroutine, killing the whole agent process.
// Before the fix, NewListener with TTL 0 crashed the test binary
// from that goroutine; newConnPool(0) panicked the same way.

// defaultTTL below is the 5m value the listener metadata parsers
// (listener/udp, listener/rudp) now default to; the pool
// must converge on the same value. It is derived from DefaultTTL
// (not a second literal) so the two cannot drift apart again.
const defaultTTL = DefaultTTL

func TestConnPoolNonPositiveTTLFallsBackToDefault(t *testing.T) {
	if DefaultTTL != 5*time.Minute {
		t.Fatalf("DefaultTTL = %v, want 5m (UDP session idle TTL, aligned with the panel-generated ttl and the downstream kernels' 300s UDP idle defaults)", DefaultTTL)
	}
	for _, ttl := range []time.Duration{0, -time.Second, -time.Hour} {
		p := newConnPool(ttl)
		if p.ttl != defaultTTL {
			t.Fatalf("newConnPool(%v): ttl = %v, want default %v", ttl, p.ttl, defaultTTL)
		}
		p.Close()
	}
}

func TestConnPoolPositiveTTLKept(t *testing.T) {
	p := newConnPool(7 * time.Second)
	if p.ttl != 7*time.Second {
		t.Fatalf("newConnPool(7s): ttl = %v, want 7s", p.ttl)
	}
	p.Close()
}

func TestNewListenerZeroTTLDoesNotPanic(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := NewListener(pc, &ListenConfig{Addr: pc.LocalAddr(), TTL: 0})
	defer ln.Close()
	// Give the idleCheck goroutine a chance to tick on the default
	// (short of waiting out the real 30s default, reaching this
	// point at all proves NewTicker did not panic: a panic in that
	// goroutine aborts the whole process).
	time.Sleep(100 * time.Millisecond)
}
