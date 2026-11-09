package router

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gost/relay"
	xlogger "github.com/go-gost/x/logger"
)

// F-212-C same-family residue (LOOKBACK-final-212 S2-3): the
// association read loop kept the pre-fix predicate "window off ⇒
// a timeout is always a resumable wake" (continue), so a foreign
// fatal timeout on the association conn was swallowed and the
// association never ended — its ConnectorPool entry stayed
// routable and SD renewals kept running. The live shape: the
// connector's host is declared dead by the kernel keepalive
// stamped on agent-owned sockets, and the blocked read fails
// with a timeout-class error (ETIMEDOUT) that the loop itself
// never armed (with the window off it arms no deadline at all).
//
// foreignTimeoutConn models that conn: every Read fails at once
// with a timeout-class error, forever.
type foreignTimeoutConn struct {
	closed atomic.Bool
}

func (c *foreignTimeoutConn) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
}
func (c *foreignTimeoutConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *foreignTimeoutConn) Close() error {
	c.closed.Store(true)
	return nil
}
func (c *foreignTimeoutConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001}
}
func (c *foreignTimeoutConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002}
}
func (c *foreignTimeoutConn) SetDeadline(time.Time) error      { return nil }
func (c *foreignTimeoutConn) SetReadDeadline(time.Time) error  { return nil }
func (c *foreignTimeoutConn) SetWriteDeadline(time.Time) error { return nil }

func TestHandleAssociateForeignTimeoutEndsAssociationWhenWindowOff(t *testing.T) {
	setAssociateWindow(t, -time.Second) // pin the window off

	h := &routerHandler{
		id:   "test-node",
		pool: NewConnectorPool("test-node"),
	}
	routerID := relay.NewTunnelID([]byte("0123456789abcdefghij"))
	host := "gw.example"

	dead := &foreignTimeoutConn{}
	done := make(chan error, 1)
	go func() {
		done <- h.handleAssociate(context.Background(), dead, "udp", host, routerID, xlogger.NewLogger())
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("handleAssociate returned nil for an association whose conn died")
		}
		if errors.Is(err, errAssociateIdleTimeout) {
			t.Fatalf("handleAssociate blamed the idle window (%v); the window is off — the conn's own error must surface", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handleAssociate never returned: foreign timeout swallowed as a resumable wake with the window off (S2-3)")
	}

	// Returning is what reaps the rest: the deferred pool.Del
	// must have dropped the connector entry.
	if c := h.pool.Get(routerID, host); c != nil {
		t.Fatal("connector still in pool after the association unwound")
	}
}
