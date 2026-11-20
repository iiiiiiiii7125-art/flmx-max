package proxyproto

import (
	"net"
	"testing"
	"time"
)

// R18-C5-01: PROXY receive is only armed with a non-empty allowlist
// of trusted senders (Lax whitelist: a non-allowlisted sender's
// header is ignored — the connection still works, but its address
// stays the real peer). Without an allowlist the bare listener is
// returned (fail-closed: no PROXY header is parsed at all).
// This file references the new WrapListenerWithAllowlist symbol, so
// it cannot compile on old code — compile failure is its red
// signal there; the behavioral red lives in the tcp listener test.

const c5ProxyHeader = "PROXY TCP4 203.0.113.99 198.51.100.7 45678 4001\r\n"

func c5AcceptOne(t *testing.T, ln net.Listener) net.Conn {
	t.Helper()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("accept: %v", r.err)
		}
		return r.c
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for accept")
		return nil
	}
}

func c5DialProxy(t *testing.T, addr net.Addr, localIP string, payload string) net.Conn {
	t.Helper()
	raddr, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("expected *net.TCPAddr, got %T", addr)
	}
	var laddr *net.TCPAddr
	if localIP != "" {
		laddr = &net.TCPAddr{IP: net.ParseIP(localIP)}
	}
	c, err := net.DialTCP("tcp", laddr, raddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := c.Write([]byte(c5ProxyHeader + payload)); err != nil {
		_ = c.Close()
		t.Fatalf("write PROXY header: %v", err)
	}
	return c
}

func c5RemoteHost(t *testing.T, c net.Conn) string {
	t.Helper()
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		t.Fatalf("split remote addr %q: %v", c.RemoteAddr(), err)
	}
	return host
}

func TestWrapListenerWithAllowlistHonoursListedSender(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	wrapped, err := WrapListenerWithAllowlist(1, []string{"127.0.0.1"}, ln, 2*time.Second)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	client := c5DialProxy(t, wrapped.Addr(), "", "hello")
	defer client.Close()
	server := c5AcceptOne(t, wrapped)
	defer server.Close()
	if got := c5RemoteHost(t, server); got != "203.0.113.99" {
		t.Fatalf("allowlisted sender's claimed IP must be honoured, got RemoteAddr host %q", got)
	}
	buf := make([]byte, 5)
	if _, err := server.Read(buf); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("payload must follow the PROXY header, got %q", string(buf))
	}
}

func TestWrapListenerWithAllowlistIgnoresUnlistedSender(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	wrapped, err := WrapListenerWithAllowlist(1, []string{"127.0.0.1"}, ln, 2*time.Second)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}

	// Dial from 127.0.0.2, which is not allowlisted, with a forged
	// header claiming 203.0.113.99.
	client := c5DialProxy(t, wrapped.Addr(), "127.0.0.2", "hello")
	defer client.Close()
	server := c5AcceptOne(t, wrapped)
	defer server.Close()
	if got := c5RemoteHost(t, server); got != "127.0.0.2" {
		t.Fatalf("unlisted sender's forged header must be ignored, got RemoteAddr host %q", got)
	}
	buf := make([]byte, 5)
	if _, err := server.Read(buf); err != nil {
		t.Fatalf("read payload after ignored header: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("connection must keep working after an ignored header, got %q", string(buf))
	}
}

func TestWrapListenerWithAllowlistEmptyFailsClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	for _, allow := range [][]string{nil, {}} {
		wrapped, err := WrapListenerWithAllowlist(1, allow, ln, 2*time.Second)
		if err != nil {
			t.Fatalf("wrap with empty allowlist must not error, got %v", err)
		}
		if wrapped != ln {
			t.Fatalf("empty allowlist must return the bare listener (fail-closed)")
		}
	}
	wrapped, err := WrapListenerWithAllowlist(0, []string{"127.0.0.1"}, ln, 2*time.Second)
	if err != nil {
		t.Fatalf("wrap with ppv 0 must not error, got %v", err)
	}
	if wrapped != ln {
		t.Fatalf("ppv 0 must return the bare listener")
	}

	// With the bare listener a forged header is never parsed:
	// RemoteAddr stays the real peer.
	client := c5DialProxy(t, ln.Addr(), "", "hello")
	defer client.Close()
	server := c5AcceptOne(t, ln)
	defer server.Close()
	if got := c5RemoteHost(t, server); got != "127.0.0.1" {
		t.Fatalf("bare listener must report the real peer, got RemoteAddr host %q", got)
	}
}

func TestWrapListenerWithAllowlistInvalidEntryErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	wrapped, err := WrapListenerWithAllowlist(1, []string{"not-an-ip"}, ln, 2*time.Second)
	if err == nil {
		t.Fatalf("invalid allowlist entry must error")
	}
	if wrapped != ln {
		t.Fatalf("invalid allowlist must return the bare listener (fail-closed)")
	}
}
