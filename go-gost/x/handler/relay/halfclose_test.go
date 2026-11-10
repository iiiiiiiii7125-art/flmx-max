package relay

import (
	"io"
	"net"
	"testing"
	"time"
)

// Package K: the relay handler's tcpConn heads the session
// stack, so it must forward half-closes — otherwise Transport's
// propagation silently no-ops on every relay session and the
// FIN'd leg lingers in CLOSE-WAIT.
func TestRelayTCPConnHalfCloseForwarding(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acceptCh := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		acceptCh <- c
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer := <-acceptCh
	defer peer.Close()

	c := &tcpConn{Conn: raw}
	if err := c.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("peer read err = %v, want io.EOF (half-close not forwarded)", err)
	}
	if err := c.CloseRead(); err != nil {
		t.Fatalf("CloseRead: %v", err)
	}
}

func TestRelayTCPConnHalfCloseUnsupported(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := &tcpConn{Conn: a}
	if err := c.CloseWrite(); err == nil {
		t.Fatal("CloseWrite over net.Pipe = nil, want not-supported error")
	}
	if err := c.CloseRead(); err == nil {
		t.Fatal("CloseRead over net.Pipe = nil, want not-supported error")
	}
}
