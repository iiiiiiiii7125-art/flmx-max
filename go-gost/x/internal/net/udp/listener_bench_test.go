package udp

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// BenchmarkListenerUDPLoopback measures the receive hot path end
// to end: loopback datagrams in through listenLoop + WriteQueue,
// drained by a consumer as fast as it can read. It exists as the
// before/after yardstick for queue-accounting changes (P1-02):
// admission accounting may only add integer add/compare level
// cost per datagram.
func BenchmarkListenerUDPLoopback(b *testing.B) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		ReadBufferSize: 2048,
		TTL:            time.Minute,
		Logger:         xlogger.Nop(),
	})
	defer ln.Close()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		b.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("init")); err != nil {
		b.Fatalf("init write: %v", err)
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
	case <-time.After(10 * time.Second):
		b.Fatal("Accept timed out")
	}

	var received atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			_, _, err := sess.(net.PacketConn).ReadFrom(buf)
			if err != nil {
				return
			}
			received.Add(1)
		}
	}()

	payload := make([]byte, 512)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Write(payload); err != nil {
			b.Fatalf("write #%d: %v", i, err)
		}
	}
	b.StopTimer()

	sess.Close()
	<-done
	b.ReportMetric(float64(received.Load()), "recv/op")
}
