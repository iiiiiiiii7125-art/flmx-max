package service

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/go-gost/core/metadata"
	xlogger "github.com/go-gost/x/logger"
	proxyproto "github.com/pires/go-proxyproto"
)

// OPEN-1: a proxyproto.Listener reads the PROXY header lazily —
// the first RemoteAddr() call waits up to ReadHeaderTimeout for a
// client that connected but sends nothing. That call used to run
// inline in the accept loop, so one silent client stalled every
// later Accept for the whole timeout, several stacking. The fix
// moves the address fold (and the rest of the per-connection
// setup) into the connection goroutine; the loop only Accepts.
//
// The test pins the behaviour, not the timeout value: a silent
// connection sits on a proxyproto listener with a 2s header
// deadline, and a second connection carrying a valid PROXY header
// must reach the handler well inside that deadline (500ms). On
// the old code the accept loop is parked on the silent
// connection's RemoteAddr() and the second connection is not even
// Accepted until the 2s deadline expires, so the assertion fails.

// proxyprotoListener adapts a net.Listener (here a
// proxyproto.Listener) to the core listener.Listener interface
// NewService takes.
type proxyprotoListener struct {
	net.Listener
}

func (l *proxyprotoListener) Init(metadata.Metadata) error { return nil }

func TestAcceptLoopNotBlockedBySilentProxyConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ppLn := &proxyprotoListener{Listener: &proxyproto.Listener{
		Listener:          ln,
		ReadHeaderTimeout: 2 * time.Second,
	}}
	h := &gateHandler{release: make(chan struct{})}
	svc := NewService("open1-accept", ppLn, h,
		LoggerOption(xlogger.NewLogger()))
	go func() { _ = svc.Serve() }()
	defer func() {
		close(h.release)
		_ = svc.Close()
	}()

	// Silent client: connects, sends nothing. The accept loop (old
	// code) would park on its RemoteAddr() for the full 2s header
	// deadline; the fixed loop hands it to a goroutine at once.
	silent, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial silent: %v", err)
	}
	defer silent.Close()
	// Let the loop pick the silent connection up first, so the
	// second connection cannot be accepted ahead of it.
	time.Sleep(200 * time.Millisecond)

	second, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial second: %v", err)
	}
	defer second.Close()
	if _, err := fmt.Fprintf(second, "PROXY TCP4 203.0.113.99 198.51.100.7 45678 4001\r\n"); err != nil {
		t.Fatalf("write PROXY header: %v", err)
	}

	// The silent connection cannot reach the handler before its
	// own 2s header deadline in either version, so a handled
	// connection inside 500ms must be the second one — i.e. the
	// accept loop stayed free.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.handled.Load() >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("second connection not handled within 500ms of its PROXY header (silent connection still stalls the accept loop)")
}
