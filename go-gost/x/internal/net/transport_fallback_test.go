package net

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// Regression tests for the generic (non-splice) copy path.
//
// io.CopyBuffer dispatches to io.WriterTo / io.ReaderFrom
// unconditionally and treats any error they return as fatal, so
// trackedRW must never decline a copy for endpoints that are not a
// raw *net.TCPConn pair. The first version of the no-progress fix
// returned errFastPathUnavailable there, which killed every
// Transport session over wrapped endpoints (stats/traffic wrappers,
// readWriteConn, UDP session conns) on entry — the connector/relay
// UDP end-to-end tests caught it, while this package's own tests,
// the benchmarks and the live rig all happened to run over raw TCP
// and stayed green. These tests pin the wrapped/in-memory shapes.

// hideTCP wraps a net.Conn so the concrete *net.TCPConn type is no
// longer visible to Transport's fast-path type assertions, while
// deadline and half-close methods stay available by promotion.
type hideTCP struct{ net.Conn }

func TestTransportWrappedEndpointsExchange(t *testing.T) {
	c1, s1 := tcpPair(t)
	c2, s2 := tcpPair(t)
	defer c1.Close()
	defer c2.Close()

	done := make(chan error, 1)
	go func() { done <- Transport(hideTCP{s1}, hideTCP{s2}) }()

	// Echo at the far end of the second pair.
	go func() {
		buf := make([]byte, 5)
		if _, err := io.ReadFull(c2, buf); err == nil {
			c2.Write(buf)
		}
	}()

	if _, err := c1.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c1, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q, want %q", got, "hello")
	}

	c1.Close()
	c2.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Transport: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after both ends closed")
	}
}

func TestTransportWrappedNoProgressReaped(t *testing.T) {
	_, s1 := tcpPair(t)
	_, s2 := tcpPair(t)
	// Both peers stay silent with their kernels alive: nothing but
	// the no-progress bound can end this session.
	start := time.Now()
	err := transport(hideTCP{s1}, hideTCP{s2}, 300*time.Millisecond)
	if err != ErrNoProgress {
		t.Fatalf("got %v, want ErrNoProgress", err)
	}
	if el := time.Since(start); el < 300*time.Millisecond || el > 5*time.Second {
		t.Fatalf("reaped after %v, want within [300ms, 5s]", el)
	}
}

func TestTransportPipeEndpoints(t *testing.T) {
	p1a, p1b := net.Pipe()
	p2a, p2b := net.Pipe()

	done := make(chan error, 1)
	go func() { done <- Transport(p1a, p2a) }()

	go func() {
		buf := make([]byte, 4)
		if _, err := io.ReadFull(p2b, buf); err == nil {
			p2b.Write(buf)
		}
	}()

	if _, err := p1b.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(p1b, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, []byte("ping")) {
		t.Fatalf("got %q, want %q", got, "ping")
	}

	p1b.Close()
	p2b.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after pipe ends closed")
	}
}
