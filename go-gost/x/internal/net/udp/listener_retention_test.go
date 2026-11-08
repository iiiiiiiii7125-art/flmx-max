package udp

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	xlogger "github.com/go-gost/x/logger"
)

// R2-P1-F01 regression: the byte budget must meter what a queued
// datagram actually retains — the capacity of the pooled buffer
// it pins — not its payload length. A 64B datagram read into the
// default 65535B read buffer pins a 65536B pool buffer; under the
// old len-metering 1024 of those sailed under the 4MiB cap while
// retaining 64MiB. Admission must now stop at the budget divided
// by the pinned capacity. Two pinned sizes are exercised: the
// default read buffer's class (65536) and the top pool class
// (66560, for ReadBufferSize in (65536, 66560]).
func TestSessionQueueChargesPinnedBufferCap(t *testing.T) {
	laddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}
	raddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10002}

	for _, pinned := range []int{65536, 66560} {
		// Packet depth far above what the byte budget admits, so
		// the byte cap is provably the binding constraint.
		c := newConn(nil, laddr, raddr, 4096, false)

		backing := make([]byte, pinned)
		small := backing[:64]

		accepted := 0
		for i := 0; i < 200; i++ {
			err := c.WriteQueue(small)
			if err == nil {
				accepted++
				continue
			}
			if !errors.Is(err, errQueueFull) {
				t.Fatalf("pinned %d: WriteQueue #%d: unexpected error %v", pinned, i, err)
			}
		}
		if want := DefaultReadQueueBytes / pinned; accepted != want {
			t.Fatalf("pinned %d: accepted %d datagrams, want exactly %d (byte budget %d)",
				pinned, accepted, want, DefaultReadQueueBytes)
		}
		if c.qBytes != accepted*pinned {
			t.Fatalf("pinned %d: accounted queue bytes = %d, want %d (charged at pinned capacity)",
				pinned, c.qBytes, accepted*pinned)
		}
		if c.qBytes > DefaultReadQueueBytes {
			t.Fatalf("pinned %d: accounted queue bytes %d exceeds the budget %d", pinned, c.qBytes, DefaultReadQueueBytes)
		}

		// Dequeue releases the pinned capacity, not the payload
		// length: one dequeue frees the full charge.
		if _, err := c.readDatagram(); err != nil {
			t.Fatalf("pinned %d: readDatagram: %v", pinned, err)
		}
		if want := (accepted - 1) * pinned; c.qBytes != want {
			t.Fatalf("pinned %d: queue bytes after one dequeue = %d, want %d", pinned, c.qBytes, want)
		}
		for c.qBytes > 0 {
			if _, err := c.readDatagram(); err != nil {
				t.Fatalf("pinned %d: drain readDatagram: %v", pinned, err)
			}
		}
		c.Close()
	}

	// A datagram whose backing array really is small charges only
	// its own capacity — the meter follows the backing array,
	// not a fixed per-datagram tax.
	c := newConn(nil, laddr, raddr, 4096, false)
	defer c.Close()
	plain := make([]byte, 100)
	if err := c.WriteQueue(plain); err != nil {
		t.Fatalf("WriteQueue plain 100B: %v", err)
	}
	if c.qBytes != 100 {
		t.Fatalf("accounted queue bytes = %d, want 100 (cap of the queued slice)", c.qBytes)
	}
}

// R2-P1-F01: fitDatagramBuffer must swap a datagram into its own
// bufpool size class whenever that at least halves the pinned
// capacity, and must keep the zero-copy path (same backing array)
// whenever it cannot.
func TestFitDatagramBuffer(t *testing.T) {
	mkbuf := func(capacity, n int, fill byte) ([]byte, []byte) {
		b := make([]byte, capacity)
		payload := make([]byte, n)
		for i := range payload {
			payload[i] = fill
		}
		copy(b, payload)
		return b, payload
	}

	// Tiny datagram in a full-size read buffer: 66560 -> 128.
	b, payload := mkbuf(66560, 100, 'a')
	out := fitDatagramBuffer(b, 100)
	if len(out) != 100 || cap(out) != 128 {
		t.Fatalf("100B datagram: len/cap = %d/%d, want 100/128", len(out), cap(out))
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("100B datagram content changed by the swap")
	}

	// Mid-size datagram: 66560 -> 32768 (next class at or above
	// the payload, still at most half the original).
	b, payload = mkbuf(66560, 20000, 'b')
	out = fitDatagramBuffer(b, 20000)
	if len(out) != 20000 || cap(out) != 32768 {
		t.Fatalf("20000B datagram: len/cap = %d/%d, want 20000/32768", len(out), cap(out))
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("20000B datagram content changed by the swap")
	}

	// Near-full datagram: no swap, same backing array (zero copy).
	b, _ = mkbuf(66560, 65535, 'c')
	out = fitDatagramBuffer(b, 65535)
	if cap(out) != 66560 || &out[0] != &b[0] {
		t.Fatalf("65535B datagram: cap = %d, want 66560 on the original backing array", cap(out))
	}

	// Datagram over half the buffer: the swap could not halve
	// the pinned capacity, so the original is kept.
	b, _ = mkbuf(66560, 40000, 'd')
	out = fitDatagramBuffer(b, 40000)
	if cap(out) != 66560 || &out[0] != &b[0] {
		t.Fatalf("40000B datagram: cap = %d, want 66560 on the original backing array", cap(out))
	}

	// Small read buffer (2048) with an MTU-class datagram: the
	// payload's class (2048) cannot halve the pin, keep original.
	b, _ = mkbuf(2048, 1500, 'e')
	out = fitDatagramBuffer(b, 1500)
	if cap(out) != 2048 || &out[0] != &b[0] {
		t.Fatalf("1500B datagram in 2048B buffer: cap = %d, want 2048 on the original backing array", cap(out))
	}

	// Small read buffer with a small datagram: swap 2048 -> 1024.
	b, payload = mkbuf(2048, 600, 'f')
	out = fitDatagramBuffer(b, 600)
	if len(out) != 600 || cap(out) != 1024 {
		t.Fatalf("600B datagram in 2048B buffer: len/cap = %d/%d, want 600/1024", len(out), cap(out))
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("600B datagram content changed by the swap")
	}
}

// R2-P1-F01 end to end: at the production default read buffer
// size (65535B), a small-packet flood against a stalled consumer
// must fill the queue to its PACKET depth, not stop at ~63: the
// right-sized buffers make the per-datagram charge track the
// payload's size class, so the retained total stays provably
// inside the byte budget while burst absorption is unchanged.
// Everything past the depth is dropped and counted on the
// aggregated drop counter.
func TestListenerStalledSmallFloodFillsPacketDepthWithinByteBudget(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		ReadBufferSize: 65535,
		TTL:            30 * time.Second,
		Logger:         xlogger.Nop(),
	})
	defer ln.Close()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	payload := bytes.Repeat([]byte("x"), 32)
	if _, err := client.Write(payload); err != nil {
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
	sc := sess.(*conn)

	// Nobody reads the session. Flood well past the packet depth.
	const flood = 3000
	for i := 1; i < flood; i++ {
		if _, err := client.Write(payload); err != nil {
			t.Fatalf("flood write #%d: %v", i, err)
		}
	}

	// The queue must saturate at the packet-count depth: under
	// the old len-metering it would also reach 1024 but retain
	// 64MiB; under cap-metering without the right-size swap it
	// would stop at 64.
	deadline := time.Now().Add(10 * time.Second)
	for len(sc.rc) < DefaultReadQueueSize {
		if time.Now().After(deadline) {
			t.Fatalf("queued %d of %d burst datagrams, want the full packet depth %d",
				len(sc.rc), flood, DefaultReadQueueSize)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Retained (accounted) bytes: 1024 datagrams in the 128B
	// size class — provably inside the byte budget.
	sc.qmu.Lock()
	charged := sc.qBytes
	sc.qmu.Unlock()
	if want := DefaultReadQueueSize * 128; charged != want {
		t.Fatalf("accounted queue bytes = %d, want %d (1024 datagrams x 128B class)", charged, want)
	}
	if charged > DefaultReadQueueBytes {
		t.Fatalf("accounted queue bytes %d exceeds the budget %d", charged, DefaultReadQueueBytes)
	}

	// Overflow past the depth is dropped and counted, not
	// silently vanished.
	dropped := ln.(*listener).dropped
	deadline = time.Now().Add(10 * time.Second)
	for dropped.Total() < 100 {
		if time.Now().After(deadline) {
			t.Fatalf("counted %d dropped datagrams, want >= 100 of the overflow", dropped.Total())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Drain the full depth: content intact.
	sess.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	for i := 0; i < DefaultReadQueueSize; i++ {
		n, _, err := sess.(net.PacketConn).ReadFrom(buf)
		if err != nil {
			t.Fatalf("ReadFrom #%d: %v", i, err)
		}
		if !bytes.Equal(buf[:n], payload) {
			t.Fatalf("ReadFrom #%d content mismatch (%d bytes)", i, n)
		}
	}

	// Stop the source, then read until the queue is empty
	// (listenLoop may still be feeding socket-buffered flood
	// datagrams into the freed slots while the drain above runs):
	// once nothing is left, the budget must be fully released.
	client.Close()
	for {
		sess.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := sess.(net.PacketConn).ReadFrom(buf); err != nil {
			break
		}
	}
	if got := len(sc.rc); got != 0 {
		t.Fatalf("queue length after full drain = %d, want 0", got)
	}
	sc.qmu.Lock()
	charged = sc.qBytes
	sc.qmu.Unlock()
	if charged != 0 {
		t.Fatalf("accounted queue bytes after full drain = %d, want 0", charged)
	}
}

// R2-P1-F01 end to end, oversized shape: maximum-size datagrams
// (65507B, the largest IPv4 UDP payload) against a stalled
// consumer at the default read buffer size must stop at the byte
// budget — 64 datagrams pinning their full 65536B buffers, the
// budget met exactly (the len-metered code admitted the same
// count here, but its meter claimed 65507B per datagram while
// each retained a full 65536B buffer; the small-packet shape in
// TestSessionQueueChargesPinnedBufferCap is where the counts
// diverged 16x).
func TestListenerStalledLargeFloodStopsAtByteBudget(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	ln := NewListener(pc, &ListenConfig{
		Addr:           pc.LocalAddr(),
		ReadBufferSize: 65535,
		TTL:            30 * time.Second,
		Logger:         xlogger.Nop(),
	})
	defer ln.Close()

	client, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	defer client.Close()

	payload := make([]byte, 65507)
	if _, err := client.Write(payload); err != nil {
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
	sc := sess.(*conn)

	want := DefaultReadQueueBytes / 65536
	// Nobody reads. Keep offering datagrams until the queue
	// saturates at the byte budget; the kernel may drop some in
	// flight, so send in waves until saturation is observed.
	deadline := time.Now().Add(15 * time.Second)
	for len(sc.rc) < want {
		for i := 0; i < 8; i++ {
			if _, err := client.Write(payload); err != nil {
				t.Fatalf("flood write: %v", err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d maximum-size datagrams, want saturation at %d (byte budget)", len(sc.rc), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(sc.rc); got != want {
		t.Fatalf("queued %d maximum-size datagrams, want exactly %d", got, want)
	}
	sc.qmu.Lock()
	charged := sc.qBytes
	sc.qmu.Unlock()
	if charged != want*65536 || charged > DefaultReadQueueBytes {
		t.Fatalf("accounted queue bytes = %d, want %d (<= budget %d)", charged, want*65536, DefaultReadQueueBytes)
	}
}
