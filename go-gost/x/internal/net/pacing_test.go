package net

import (
	"net"
	"testing"
	"time"
)

func TestNewPacedConnDisabledReturnsSameConn(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	c, err := net.Dial("udp", ln.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if got := NewPacedConn(c, 0); got != c {
		t.Fatalf("interval 0: got wrapped conn, want the conn itself")
	}
	if got := NewPacedConn(c, -time.Second); got != c {
		t.Fatalf("negative interval: got wrapped conn, want the conn itself")
	}
	if got := NewPacedConn(nil, time.Second); got != nil {
		t.Fatalf("nil conn: got %v, want nil", got)
	}
}

func TestPacedConnSpreadsWrites(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	raw, err := net.Dial("udp", ln.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()

	// Drain in the background so no datagram ever blocks the sender.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64)
		for i := 0; i < 5; i++ {
			if _, _, err := ln.ReadFrom(buf); err != nil {
				return
			}
		}
	}()

	interval := 5 * time.Millisecond
	c := NewPacedConn(raw, interval)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := c.Write([]byte{byte(i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	// First write immediate, then 4 gaps of interval: 20ms ideal.
	// Scheduling slop is tolerated, but anything under 15ms means
	// the grid is not spacing.
	if elapsed < 15*time.Millisecond {
		t.Fatalf("5 writes took %v, want >= 15ms (4 x %v gaps)", elapsed, interval)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("5 writes took %v, want < 500ms", elapsed)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for the 5 datagrams to arrive")
	}
}

func TestPacerFirstWaitImmediateThenSpaced(t *testing.T) {
	p := NewPacer(20 * time.Millisecond)
	start := time.Now()
	p.Wait()
	if d := time.Since(start); d > 10*time.Millisecond {
		t.Fatalf("first Wait took %v, want immediate", d)
	}
	p.Wait()
	if d := time.Since(start); d < 15*time.Millisecond {
		t.Fatalf("second Wait returned after %v, want >= 15ms", d)
	}
}

func TestPacerDisabledNeverWaits(t *testing.T) {
	p := NewPacer(0)
	start := time.Now()
	for i := 0; i < 100; i++ {
		p.Wait()
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("disabled pacer took %v for 100 waits", d)
	}
}
