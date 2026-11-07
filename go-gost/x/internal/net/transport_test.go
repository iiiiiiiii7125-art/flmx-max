package net

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// D3-03 regression: a client that half-closes (shutdown(WR)) after
// sending its request must still receive the full response. The old
// Transport returned the moment the client->server copy saw EOF, and
// the caller closing both conns truncated the in-flight response.

func tcpPair(tb testing.TB) (client, server net.Conn) {
	tb.Helper()
	t := tb
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	acceptc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			acceptc <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	select {
	case server = <-acceptc:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept timed out")
	}
	return client, server
}

func TestTransportHalfCloseDeliversFullResponse(t *testing.T) {
	// Topology: testClient <-> [Transport] <-> backend.
	// The backend reads the request until EOF, then streams a large
	// response back in small chunks before closing.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 32*1024) // 512 KiB

	backendSide, transportToBackend := tcpPair(t)
	defer backendSide.Close()
	go func() {
		// Backend: consume the request to EOF, then reply slowly.
		io.Copy(io.Discard, backendSide)
		for off := 0; off < len(payload); off += 8192 {
			end := off + 8192
			if end > len(payload) {
				end = len(payload)
			}
			if _, err := backendSide.Write(payload[off:end]); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		backendSide.Close()
	}()

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()

	transportDone := make(chan error, 1)
	go func() {
		transportDone <- Transport(transportToClient, transportToBackend)
	}()

	// Client: send the request, half-close, then read the response
	// to EOF — exactly the pattern the old Transport truncated.
	if _, err := clientSide.Write([]byte("request")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if err := clientSide.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite: %v", err)
	}
	clientSide.SetReadDeadline(time.Now().Add(15 * time.Second))
	got, err := io.ReadAll(clientSide)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("half-closed client received %d of %d response bytes (truncated)", len(got), len(payload))
	}

	select {
	case err := <-transportDone:
		if err != nil {
			t.Fatalf("Transport returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Transport did not return after both directions finished")
	}
	transportToClient.Close()
	transportToBackend.Close()
}

// A hard failure on one side must end Transport promptly (no parked
// sibling copy) and surface an error.
func TestTransportHardErrorReturnsPromptly(t *testing.T) {
	backendSide, transportToBackend := tcpPair(t)
	defer backendSide.Close()
	go func() {
		// Backend streams indefinitely.
		buf := make([]byte, 4096)
		for {
			if _, err := backendSide.Write(buf); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	clientSide, transportToClient := tcpPair(t)
	transportDone := make(chan error, 1)
	go func() {
		transportDone <- Transport(transportToClient, transportToBackend)
	}()

	// Client reads a little, then vanishes with an RST.
	buf := make([]byte, 4096)
	clientSide.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(clientSide, buf); err != nil {
		t.Fatalf("client initial read: %v", err)
	}
	clientSide.(*net.TCPConn).SetLinger(0)
	clientSide.Close()

	select {
	case err := <-transportDone:
		if err == nil {
			t.Fatal("Transport returned nil after a hard connection failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Transport did not return after a hard connection failure")
	}
	transportToClient.Close()
	transportToBackend.Close()
}

// Ordinary full-duplex flow: both sides exchange data and close;
// everything arrives and Transport returns cleanly.
func TestTransportBidirectional(t *testing.T) {
	a1, a2 := tcpPair(t)
	b1, b2 := tcpPair(t)
	defer a1.Close()
	defer b1.Close()

	done := make(chan error, 1)
	go func() { done <- Transport(a2, b2) }()

	go func() {
		a1.Write([]byte("ping"))
		a1.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		b1.Write([]byte("pong"))
		b1.(*net.TCPConn).CloseWrite()
	}()

	buf := make([]byte, 4)
	a1.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(a1, buf); err != nil {
		t.Fatalf("a1 read: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("a1 got %q, want pong", buf)
	}
	if _, err := io.ReadFull(b1, buf); err != nil {
		t.Fatalf("b1 read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("b1 got %q, want ping", buf)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Transport: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return")
	}
}

// FIX-1 regression: a session in which neither direction ever moves
// a byte must be torn down once the idle window elapses, with both
// endpoints closed — the shape of an application-dead peer whose
// kernel keeps answering keepalive (audit C §3 / C2 cell 2).
func TestTransportNoProgressTornDown(t *testing.T) {
	const idle = 300 * time.Millisecond

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportToBackend := tcpPair(t)
	defer backendSide.Close()
	// Both peers stay silent forever (but keep the sockets open).
	go io.Copy(io.Discard, backendSide)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- transport(transportToClient, transportToBackend, idle) }()

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
		if elapsed := time.Since(start); elapsed < idle {
			t.Fatalf("Transport returned after %v, before the %v idle window", elapsed, idle)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not tear down a fully stalled session")
	}

	// Both endpoints must be closed, not just one.
	for name, c := range map[string]net.Conn{"client side": transportToClient, "backend side": transportToBackend} {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatalf("%s endpoint still readable after teardown", name)
		} else if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			t.Fatalf("%s endpoint not closed after teardown (read timed out)", name)
		}
	}
}

// FIX-1 regression: one direction reaches clean EOF and the reverse
// direction then stalls forever. The surviving copy must be reaped
// by the no-progress bound instead of parking on Read with the
// FIN-WAIT-2 socket pinned (audit C2 §3, chained graceful exit).
func TestTransportHalfCloseThenStallReaped(t *testing.T) {
	const idle = 300 * time.Millisecond

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportToBackend := tcpPair(t)
	defer backendSide.Close()
	// Backend never sends anything and never closes.
	go io.Copy(io.Discard, backendSide)

	done := make(chan error, 1)
	go func() { done <- transport(transportToClient, transportToBackend, idle) }()

	// Client sends a request and half-closes, then waits silently.
	if _, err := clientSide.Write([]byte("request")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if err := clientSide.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("client CloseWrite: %v", err)
	}

	select {
	case err := <-done:
		if err != ErrNoProgress {
			t.Fatalf("Transport err = %v, want ErrNoProgress", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not reap the stalled reverse direction after half-close")
	}

	// The client must observe the session ending (EOF or reset),
	// not hang on a half-open socket forever.
	clientSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadAll(clientSide); err != nil {
		if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
			t.Fatal("client connection left half-open after teardown")
		}
	}
}

// FIX-1 regression: a slow but continuously progressing session
// (a trickle well inside the idle window, in one direction only)
// must not be killed — progress is progress, however slow.
func TestTransportTrickleNotKilled(t *testing.T) {
	const idle = 300 * time.Millisecond

	clientSide, transportToClient := tcpPair(t)
	defer clientSide.Close()
	backendSide, transportToBackend := tcpPair(t)

	received := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		total := 0
		for total < 8*16 {
			n, err := backendSide.Read(buf)
			if err != nil {
				return
			}
			total += n
		}
		close(received)
	}()

	done := make(chan error, 1)
	go func() { done <- transport(transportToClient, transportToBackend, idle) }()

	// 8 chunks of 16B, one every idle/3 — total span ≈ 2.7x the
	// idle window, each gap far inside it.
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		for i := 0; i < 8; i++ {
			if _, err := clientSide.Write(bytes.Repeat([]byte("x"), 16)); err != nil {
				return
			}
			time.Sleep(idle / 3)
		}
	}()
	<-stop

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not receive the full trickle")
	}
	select {
	case err := <-done:
		t.Fatalf("Transport returned during an actively trickling session: %v", err)
	default:
	}

	// Clean close on both ends ends Transport without error.
	clientSide.Close()
	backendSide.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Transport err = %v, want nil after clean close", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after clean close")
	}
	transportToClient.Close()
	transportToBackend.Close()
}

// FIX-1 regression: a long-lived session with continuous low-rate
// traffic in BOTH directions must survive many idle windows intact.
func TestTransportLongLivedBidirectionalNotKilled(t *testing.T) {
	const idle = 250 * time.Millisecond

	a1, a2 := tcpPair(t)
	b1, b2 := tcpPair(t)
	defer a1.Close()
	defer b1.Close()

	done := make(chan error, 1)
	go func() { done <- transport(a2, b2, idle) }()

	// Echo both ways every idle/4 for ~6 idle windows. The cadence
	// keeps four progress events inside every idle window: with
	// only two, ordinary scheduler jitter on a loaded CI box can
	// starve the test goroutine for a whole window and make the
	// (correct) no-progress teardown look like a false kill.
	deadline := time.Now().Add(6 * idle)
	a1.SetDeadline(time.Now().Add(10 * time.Second))
	b1.SetDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4)
	for time.Now().Before(deadline) {
		if _, err := a1.Write([]byte("ping")); err != nil {
			t.Fatalf("a1 write: %v", err)
		}
		if _, err := io.ReadFull(b1, buf); err != nil {
			t.Fatalf("b1 read: %v", err)
		}
		if _, err := b1.Write([]byte("pong")); err != nil {
			t.Fatalf("b1 write: %v", err)
		}
		if _, err := io.ReadFull(a1, buf); err != nil {
			t.Fatalf("a1 read: %v", err)
		}
		time.Sleep(idle / 4)
	}
	select {
	case err := <-done:
		t.Fatalf("Transport returned during a live bidirectional session: %v", err)
	default:
	}

	a1.Close()
	b1.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Transport did not return after both ends closed")
	}
}
