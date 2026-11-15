package http

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestSingleConnListenerSecondAcceptBlocksUntilClose is the regression
// test for the api/metrics truncation: the second Accept must block
// until the connection closes, not return immediately. Returning
// immediately made http.Server.Serve return before the serve goroutine
// ran, and the trailing Shutdown then aborted the request (empty reply).
func TestSingleConnListenerSecondAcceptBlocksUntilClose(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	l := NewSingleConnListener(context.Background(), server)

	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("first Accept: %v", err)
	}
	if conn == nil {
		t.Fatalf("first Accept returned nil conn")
	}

	second := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		second <- err
	}()

	// The second Accept must still be blocked after 200ms.
	select {
	case err := <-second:
		t.Fatalf("second Accept returned early: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Closing the connection must unblock the second Accept.
	if err := conn.Close(); err != nil {
		t.Fatalf("Close conn: %v", err)
	}
	select {
	case err := <-second:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("second Accept err = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Accept still blocked after conn Close")
	}
}

// TestSingleConnListenerSecondAcceptUnblocksOnCtxDone: cancelling the
// context must also unblock a pending second Accept.
func TestSingleConnListenerSecondAcceptUnblocksOnCtxDone(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	l := NewSingleConnListener(ctx, server)

	if _, err := l.Accept(); err != nil {
		t.Fatalf("first Accept: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		second <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-second:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("second Accept err = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Accept still blocked after ctx cancel")
	}
}

// TestSingleConnListenerCloseUnblocksAccept: listener Close must unblock
// a pending second Accept (the path http.Server.Shutdown takes).
func TestSingleConnListenerCloseUnblocksAccept(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	l := NewSingleConnListener(context.Background(), server)
	if _, err := l.Accept(); err != nil {
		t.Fatalf("first Accept: %v", err)
	}

	second := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		second <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-second:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("second Accept err = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("second Accept still blocked after listener Close")
	}
}

// TestSingleConnListenerAddr returns the connection's local address.
func TestSingleConnListenerAddr(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	l := NewSingleConnListener(context.Background(), server)
	if got, want := l.Addr(), server.LocalAddr(); got.String() != want.String() {
		t.Fatalf("Addr = %v, want %v", got, want)
	}
}
