package wrapper

import (
	"io"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	xnet "github.com/go-gost/x/internal/net"
	xstats "github.com/go-gost/x/observer/stats"
)

// tcpPair returns the two ends of a real loopback TCP connection.
func tcpPair(t *testing.T) (a, b net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		ch <- res{c, err}
	}()
	a, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	return a, r.c
}

func countFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("fd counting unavailable: %v", err)
	}
	return len(ents)
}

// TestHalfClosePropagatesThroughWrapperAndTransport is the
// package K regression pin for the CLOSE-WAIT family: a relay /
// forward session whose inbound leg is stats-wrapped. When the
// far side sends FIN, Transport learns EOF on the outbound leg
// and must propagate a half-close back through the wrapper to
// the client; before the wrapper grew CloseWrite forwarding,
// the dst.(closeWriter) assertion silently failed, the client
// never saw EOF, and the FIN'd leg lingered in CLOSE-WAIT for
// the sibling direction's whole remaining lifetime.
func TestHalfClosePropagatesThroughWrapperAndTransport(t *testing.T) {
	client, agentIn := tcpPair(t)
	agentOut, landing := tcpPair(t)
	defer landing.Close()

	wrapped := WrapConn(agentIn, xstats.NewStats(false))

	transportDone := make(chan error, 1)
	go func() { transportDone <- xnet.Transport(wrapped, agentOut) }()

	// Warm-up round trip: data flows end-to-end (and the runtime
	// netpoller allocates its own fds) before the fd baseline is
	// taken, so the baseline counts only session sockets.
	if _, err := client.Write([]byte{0x42}); err != nil {
		t.Fatal(err)
	}
	landing.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := landing.Read(buf); err != nil || buf[0] != 0x42 {
		t.Fatalf("warm-up read: err=%v byte=%v", err, buf)
	}

	fdsBefore := 0
	if runtime.GOOS == "linux" {
		fdsBefore = countFDs(t)
	}

	// Landing half-closes: it will send no more, but stays open
	// to read (the exact shape that used to park the session).
	if err := landing.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}

	// The client must observe EOF promptly through the wrapper.
	client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("client read err = %v, want io.EOF (half-close not propagated through stats wrapper)", err)
	}

	// Client goes away entirely: the whole session must tear
	// down, not park on the remaining direction.
	client.Close()
	select {
	case <-transportDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Transport did not return after client close")
	}
	// Transport returns the legs to its caller; the service
	// closes them (as production does) — the test does the same.
	wrapped.Close()
	agentOut.Close()
	landing.Close()

	// fd baseline: every socket of the session is released.
	if runtime.GOOS == "linux" {
		deadline := time.Now().Add(2 * time.Second)
		for {
			n := countFDs(t)
			if n <= fdsBefore-4 { // the four session sockets are gone
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("fd count = %d, want <= %d (session sockets leaked)", n, fdsBefore-4)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestStatsWrapperHalfCloseUnsupported: a wrapped conn without
// half-close support yields the package's not-supported error,
// not a panic and not a silent nil.
func TestStatsWrapperHalfCloseUnsupported(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	w := WrapConn(a, xstats.NewStats(false))
	cw, ok := w.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("wrapped conn does not expose CloseWrite")
	}
	if err := cw.CloseWrite(); err == nil {
		t.Fatal("CloseWrite over net.Pipe = nil, want not-supported error")
	}
	cr, ok := w.(interface{ CloseRead() error })
	if !ok {
		t.Fatal("wrapped conn does not expose CloseRead")
	}
	if err := cr.CloseRead(); err == nil {
		t.Fatal("CloseRead over net.Pipe = nil, want not-supported error")
	}
}
