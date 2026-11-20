package tcp

import (
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/listener"
	xlogger "github.com/go-gost/x/logger"
	xmetadata "github.com/go-gost/x/metadata"
)

// R18-C5-01 (agent listener leg): the tcp listener arms PROXY
// receive only when the panel supplied a non-empty
// proxyProtocolAllowlist in the listener metadata. These tests
// drive the real listener (NewListener + Init + Accept), so they
// fail on old code with ordinary assertion failures — on old code
// every PROXY header was trusted (go-proxyproto default USE).

func c5NewTestListener(t *testing.T, allowlist any) listener.Listener {
	t.Helper()
	ln := NewListener(
		listener.AddrOption("127.0.0.1:0"),
		listener.ProxyProtocolOption(1),
		listener.LoggerOption(xlogger.Nop()),
	)
	md := map[string]any{}
	if allowlist != nil {
		md["proxyProtocolAllowlist"] = allowlist
	}
	if err := ln.Init(xmetadata.NewMetadata(md)); err != nil {
		t.Fatalf("init listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func c5AcceptListenerConn(t *testing.T, ln listener.Listener) net.Conn {
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

func c5DialListener(t *testing.T, addr net.Addr, localIP string) net.Conn {
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
	// Forged PROXY header claiming 203.0.113.99, then payload.
	if _, err := c.Write([]byte("PROXY TCP4 203.0.113.99 198.51.100.7 45678 4001\r\nhello")); err != nil {
		_ = c.Close()
		t.Fatalf("write PROXY header: %v", err)
	}
	return c
}

func c5ListenerRemoteHost(t *testing.T, c net.Conn) string {
	t.Helper()
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		t.Fatalf("split remote addr %q: %v", c.RemoteAddr(), err)
	}
	return host
}

func TestTCPListenerAllowlistHonoursListedSender(t *testing.T) {
	// The panel ships the allowlist as a JSON array, which the
	// agent parses as []any of strings — use that exact shape.
	ln := c5NewTestListener(t, []any{"127.0.0.1"})
	client := c5DialListener(t, ln.Addr(), "")
	defer client.Close()
	server := c5AcceptListenerConn(t, ln)
	defer server.Close()
	if got := c5ListenerRemoteHost(t, server); got != "203.0.113.99" {
		t.Fatalf("allowlisted sender's claimed IP must be honoured, got RemoteAddr host %q", got)
	}
}

func TestTCPListenerAllowlistIgnoresUnlistedSender(t *testing.T) {
	ln := c5NewTestListener(t, []string{"127.0.0.1"})
	client := c5DialListener(t, ln.Addr(), "127.0.0.2")
	defer client.Close()
	server := c5AcceptListenerConn(t, ln)
	defer server.Close()
	if got := c5ListenerRemoteHost(t, server); got != "127.0.0.2" {
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

func TestTCPListenerNoAllowlistFailsClosed(t *testing.T) {
	ln := c5NewTestListener(t, nil)
	client := c5DialListener(t, ln.Addr(), "")
	defer client.Close()
	server := c5AcceptListenerConn(t, ln)
	defer server.Close()
	if got := c5ListenerRemoteHost(t, server); got != "127.0.0.1" {
		t.Fatalf("without an allowlist a forged PROXY header must never be trusted, got RemoteAddr host %q", got)
	}
}
