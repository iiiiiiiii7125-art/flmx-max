package wrapper

import (
	"io"
	"net"
	"testing"
	"time"
)

// Package K: limitConn must forward half-closes to the wrapped
// conn, or Transport's propagation silently no-ops behind the
// traffic limiter and the FIN'd leg lingers in CLOSE-WAIT.
func TestLimitConnHalfCloseForwarding(t *testing.T) {
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

	w := &limitConn{Conn: raw}
	if err := w.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}
	peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("peer read err = %v, want io.EOF (half-close not forwarded)", err)
	}
	if err := w.CloseRead(); err != nil {
		t.Fatalf("CloseRead: %v", err)
	}
}

func TestLimitConnHalfCloseUnsupported(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w := &limitConn{Conn: a}
	if err := w.CloseWrite(); err == nil {
		t.Fatal("CloseWrite over net.Pipe = nil, want not-supported error")
	}
	if err := w.CloseRead(); err == nil {
		t.Fatal("CloseRead over net.Pipe = nil, want not-supported error")
	}
}
