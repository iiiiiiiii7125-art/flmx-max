package wrapper

import (
	"net"
	"testing"
)

// R18-C5-01: serverConn.Raw() forwards to the wrapped connection
// so the service layer can reach the proxyproto.Conn underneath
// the wrapper chain and record the raw peer (PeerIP).

type fakeRawConn struct {
	net.Conn
	raw net.Conn
}

func (f *fakeRawConn) Raw() net.Conn { return f.raw }

func TestServerConnRawForwards(t *testing.T) {
	_, inner := net.Pipe()
	defer inner.Close()
	_, marker := net.Pipe()
	defer marker.Close()
	c := &serverConn{Conn: &fakeRawConn{Conn: inner, raw: marker}}
	if got := c.Raw(); got != marker {
		t.Fatalf("Raw() must return the inner conn's Raw(), got %v", got)
	}
}

func TestServerConnRawFallsBackToInner(t *testing.T) {
	_, inner := net.Pipe()
	defer inner.Close()
	c := &serverConn{Conn: inner}
	if got := c.Raw(); got != inner {
		t.Fatalf("Raw() must return the inner conn when it has no Raw(), got %v", got)
	}
}
