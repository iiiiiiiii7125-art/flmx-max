package udp

import (
	"net"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// D3-08 regression: when a session's receive queue is full, listenLoop
// drops the datagram. The pooled read buffer backing that datagram must
// be returned to bufpool on the drop path (previously it was neither
// queued nor returned, so the drop path allocated a fresh buffer per
// packet under exactly the overload condition the pool exists for).
// sync.Pool returns are not externally observable, so this test pins
// the branch itself: the drop path is exercised and the listener and
// session must stay fully functional afterwards.
func TestListenerQueueFullDropKeepsListenerAlive(t *testing.T) {
	requireLoopbackUDP(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		Backlog:        4,
		ReadQueueSize:  1,
		ReadBufferSize: 2048,
		TTL:            30 * time.Second,
		Logger:         xlogger.Nop(),
	})
	defer ln.Close()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	send := func(s string) {
		t.Helper()
		if _, err := client.Write([]byte(s)); err != nil {
			t.Fatalf("write %q: %v", s, err)
		}
	}

	// First datagram creates the session and occupies the single
	// queue slot.
	send("one")
	acceptc := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			acceptc <- c
		}
	}()
	var sess net.Conn
	select {
	case sess = <-acceptc:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept timed out")
	}
	defer sess.Close()

	// Second datagram hits the full queue: drop path.
	send("two")
	// Give listenLoop a moment to process (and drop) it.
	time.Sleep(200 * time.Millisecond)

	// Drain the queued datagram, then prove the listener still
	// delivers subsequent datagrams on the same session.
	buf := make([]byte, 64)
	sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := sess.(net.PacketConn).ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom #1: %v", err)
	}
	if got := string(buf[:n]); got != "one" {
		t.Fatalf("ReadFrom #1 = %q, want %q", got, "one")
	}

	send("three")
	n, _, err = sess.(net.PacketConn).ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom #2: %v", err)
	}
	if got := string(buf[:n]); got != "three" {
		t.Fatalf("ReadFrom #2 = %q, want %q (dropped datagram must not wedge the session)", got, "three")
	}
}
