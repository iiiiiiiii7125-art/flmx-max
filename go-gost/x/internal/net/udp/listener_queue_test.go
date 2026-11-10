package udp

import (
	"fmt"
	"net"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// FIX-U regression: callers on the Bind paths (chain, relay and
// socks connectors) hand core's BindOptions.Backlog /
// UDPDataQueueSize through un-defaulted. NewListener must converge
// a non-positive ReadQueueSize / Backlog onto DefaultReadQueueSize
// / DefaultBacklog — previously the zero reached newConn verbatim
// and built an unbuffered channel that discarded every burst
// datagram ("recv queue is full"). An explicitly configured size
// must be kept verbatim.
func TestNewListenerDefaultsQueueSizes(t *testing.T) {
	requireLoopbackUDP(t)
	acceptOne := func(t *testing.T, cfg *ListenConfig) *conn {
		t.Helper()
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ListenPacket: %v", err)
		}
		cfg.Addr = pc.LocalAddr()
		if cfg.Logger == nil {
			cfg.Logger = xlogger.Nop()
		}
		ln := NewListener(pc, cfg)
		t.Cleanup(func() { ln.Close() })

		client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatalf("DialUDP: %v", err)
		}
		t.Cleanup(func() { client.Close() })
		if _, err := client.Write([]byte("ping")); err != nil {
			t.Fatalf("write: %v", err)
		}

		acceptc := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				acceptc <- c
			}
		}()
		select {
		case c := <-acceptc:
			return c.(*conn)
		case <-time.After(5 * time.Second):
			t.Fatal("Accept timed out")
			return nil
		}
	}

	sess := acceptOne(t, &ListenConfig{ReadBufferSize: 2048, TTL: 30 * time.Second})
	if got := cap(sess.rc); got != 1024 {
		t.Fatalf("defaulted session queue cap = %d, want %d", got, 1024)
	}

	sess = acceptOne(t, &ListenConfig{ReadQueueSize: 7, ReadBufferSize: 2048, TTL: 30 * time.Second})
	if got := cap(sess.rc); got != 7 {
		t.Fatalf("explicit session queue cap = %d, want 7 (explicit value must be kept)", got)
	}
}

// FIX-U regression (burst absorption): with the queue size left
// un-defaulted by the caller (the Bind-path shape), a zero-gap
// burst of 300 datagrams from a single source must be absorbed in
// full — nothing discarded — and delivered in order once the
// consumer starts reading. On the pre-fix code the queue was
// unbuffered, so virtually the whole burst was dropped.
func TestListenerZeroGapBurstAbsorbed(t *testing.T) {
	requireLoopbackUDP(t)
	const burst = 300

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
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

	payload := func(i int) string {
		return fmt.Sprintf("burst-%04d", i)
	}

	// First datagram creates the session.
	if _, err := client.Write([]byte(payload(0))); err != nil {
		t.Fatalf("write #0: %v", err)
	}
	acceptc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
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

	// Fire the rest of the burst zero-gap while nobody reads.
	for i := 1; i < burst; i++ {
		if _, err := client.Write([]byte(payload(i))); err != nil {
			t.Fatalf("write #%d: %v", i, err)
		}
	}

	// Wait until listenLoop has queued the whole burst (bounded
	// poll on the queue length instead of a fixed sleep).
	sc := sess.(*conn)
	deadline := time.Now().Add(10 * time.Second)
	for len(sc.rc) < burst {
		if time.Now().After(deadline) {
			t.Fatalf("queued %d of %d burst datagrams; %d discarded (recv queue overflow)",
				len(sc.rc), burst, burst-len(sc.rc))
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Drain and verify count, order and content.
	sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	for i := 0; i < burst; i++ {
		n, _, err := sess.(net.PacketConn).ReadFrom(buf)
		if err != nil {
			t.Fatalf("ReadFrom #%d: %v", i, err)
		}
		if got, want := string(buf[:n]), payload(i); got != want {
			t.Fatalf("ReadFrom #%d = %q, want %q", i, got, want)
		}
	}
}
