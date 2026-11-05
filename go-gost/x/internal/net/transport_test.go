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

func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
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
