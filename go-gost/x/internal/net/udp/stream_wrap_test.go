package udp

import (
	"net"
	"testing"
)

// Unit pins for the transport detection behind F-UDP-2b: the
// stream wrapper must be keyed on the transport's datagram
// semantics, not on the payload network.

type wrapFakePacketConn struct {
	net.Conn
	inbox chan []byte
}

func (c *wrapFakePacketConn) Read(p []byte) (int, error) {
	// Kernel datagram semantics: one datagram per Read, overflow
	// silently discarded.
	return copy(p, <-c.inbox), nil
}

func (c *wrapFakePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Read(p)
	return n, c.RemoteAddr(), err
}

func (c *wrapFakePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return len(p), nil
}

type wrapFakeStreamConn struct {
	net.Conn
}

func TestIsDatagramConn(t *testing.T) {
	dgram := &wrapFakePacketConn{inbox: make(chan []byte, 1)}
	if !IsDatagramConn(dgram) {
		t.Fatal("packet conn must be detected as datagram")
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if IsDatagramConn(c1) {
		t.Fatal("stream pipe conn must not be detected as datagram")
	}
	if IsDatagramConn(&wrapFakeStreamConn{Conn: c1}) {
		t.Fatal("wrapped stream conn must not be detected as datagram")
	}
}

func TestStreamConnIfDatagram(t *testing.T) {
	// Stream transport: returned unchanged.
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if got := StreamConnIfDatagram(c1); got != net.Conn(c1) {
		t.Fatal("stream conn must be returned unchanged")
	}

	// Datagram transport: wrapped, and small reads must not lose
	// the tail of a datagram that carried more than the read.
	dgram := &wrapFakePacketConn{inbox: make(chan []byte, 1)}
	merged := []byte{0x01, 0x00, 0x00, 0x00, 'f', 'i', 'r', 's', 't', '-', 'f', 'r', 'a', 'm', 'e'}
	dgram.inbox <- merged

	wrapped := StreamConnIfDatagram(dgram)
	if wrapped == net.Conn(dgram) {
		t.Fatal("datagram conn must be wrapped")
	}
	if IsDatagramConn(wrapped) {
		t.Fatal("wrapped conn must no longer report datagram semantics")
	}
	// Applying the helper again must not double-wrap.
	if got := StreamConnIfDatagram(wrapped); got != wrapped {
		t.Fatal("already wrapped conn must be returned unchanged")
	}

	head := make([]byte, 4)
	if _, err := wrapped.Read(head); err != nil {
		t.Fatalf("read header: %v", err)
	}
	rest := make([]byte, len(merged)-4)
	n, err := wrapped.Read(rest)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if got := append(head, rest[:n]...); string(got) != string(merged) {
		t.Fatalf("got %q, want %q", got, merged)
	}
}
