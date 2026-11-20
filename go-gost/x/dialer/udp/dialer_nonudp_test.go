package udp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-gost/core/dialer"
)

// fakeNetDialer returns a fixed conn (or error) instead of dialling.
type fakeNetDialer struct {
	conn net.Conn
	err  error
}

func (f *fakeNetDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return f.conn, f.err
}

// R18-D4-02 regression: when the underlying dialer returns a conn that
// is not a *net.UDPConn — e.g. chain/route.go returning *relay.udpConn
// or *packetConn behind a multiplex front node — Dial must return an
// error, never panic on an unchecked type assertion.
func TestDialNonUDPConnReturnsErrorNotPanic(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	// net.Pipe returns *net.pipeAddr conns, never *net.UDPConn: the
	// same shape relay/packet conns take through a multiplex route.
	d := NewDialer()
	conn, err := d.Dial(context.Background(), "127.0.0.1:53",
		dialer.NetDialerDialOption(&fakeNetDialer{conn: client}))
	if err == nil {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("Dial with a non-*net.UDPConn underlying conn succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "unexpected conn type") {
		t.Fatalf("error %q should name the unexpected conn type", err.Error())
	}
	// The rejected underlying conn must be closed, not leaked.
	if _, err := client.Write([]byte("x")); err == nil {
		t.Fatal("underlying conn was not closed after the rejected dial")
	}
}

// R18-D4-02: the happy path still returns a working UDP conn.
func TestDialUDPConnStillWorks(t *testing.T) {
	ln, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	uc, err := net.DialUDP("udp", nil, ln.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDialer()
	conn, err := d.Dial(context.Background(), ln.LocalAddr().String(),
		dialer.NetDialerDialOption(&fakeNetDialer{conn: uc}))
	if err != nil {
		t.Fatalf("Dial with *net.UDPConn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 16)
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := ln.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("server got %q, want %q", buf[:n], "ping")
	}
}
