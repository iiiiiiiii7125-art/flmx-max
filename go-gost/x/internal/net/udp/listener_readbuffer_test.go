package udp

import (
	"net"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// R18-D4-04 regression: NewListener must default ReadBufferSize like
// it already defaults TTL, ReadQueueSize and Backlog. With
// ReadBufferSize 0 the read loop used a zero-length buffer, so every
// arriving datagram was consumed and discarded as an empty read — a
// silently payload-less UDP listener with no error anywhere.
func TestListenerZeroReadBufferSizeStillDeliversPayload(t *testing.T) {
	requireLoopbackUDP(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		Backlog:        4,
		ReadQueueSize:  16,
		ReadBufferSize: 0, // the defect: must fall back to MaxDatagramSize
		TTL:            30 * time.Second,
		Logger:         xlogger.Nop(),
	})
	defer ln.Close()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	const payload = "hello-udp-payload"
	if _, err := client.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
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

	buf := make([]byte, 64)
	_ = sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := sess.(net.PacketConn).ReadFrom(buf)
	if err != nil {
		t.Fatalf("ReadFrom: %v", err)
	}
	if got := string(buf[:n]); got != payload {
		t.Fatalf("ReadFrom = %q (%d bytes), want %q", got, n, payload)
	}
}
