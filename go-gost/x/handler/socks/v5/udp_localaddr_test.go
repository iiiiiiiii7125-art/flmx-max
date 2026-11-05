package v5

import (
	"bytes"
	"context"
	"net"
	"testing"

	xlogger "github.com/go-gost/x/logger"
	xrecorder "github.com/go-gost/x/recorder"
)

// stubUDPAddrConn is a net.Conn whose LocalAddr is a *net.UDPAddr,
// as happens when the SOCKS5 handler is paired with a non-TCP
// (wrapped/UDP/TUN) connection.
type stubUDPAddrConn struct {
	net.Conn
	local  net.Addr
	writes bytes.Buffer
}

func (c *stubUDPAddrConn) LocalAddr() net.Addr {
	return c.local
}

func (c *stubUDPAddrConn) RemoteAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 5000}
}

func (c *stubUDPAddrConn) Write(b []byte) (int, error) {
	return c.writes.Write(b)
}

func (c *stubUDPAddrConn) Close() error { return nil }

// D3-02 trigger 1: handleUDP used an unchecked
// conn.LocalAddr().(*net.TCPAddr) assertion, panicking the whole
// agent when the connection's local address was not TCP. It must
// return an error (and a SOCKS5 failure reply) instead.
func TestHandleUDPNonTCPALocalAddrReturnsError(t *testing.T) {
	h := &socks5Handler{md: metadata{enableUDP: true}}
	conn := &stubUDPAddrConn{
		local: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1080},
	}
	ro := &xrecorder.HandlerRecorderObject{}

	err := h.handleUDP(context.Background(), conn, ro, xlogger.NewLogger())
	if err == nil {
		t.Fatal("handleUDP with a UDP local address must return an error, not panic/succeed")
	}
	if conn.writes.Len() == 0 {
		t.Fatal("handleUDP should write a SOCKS5 failure reply before returning")
	}
}
