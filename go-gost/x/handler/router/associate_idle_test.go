package router

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/core/sd"
	"github.com/go-gost/relay"
	xlogger "github.com/go-gost/x/logger"
)

func init() {
	// NewConnector logs through the core default logger.
	corelogger.SetDefault(xlogger.Nop())
}

// F08 regression: handleAssociate blocked in a bare Read loop with
// no deadline, so a dead connector's association never returned —
// its ConnectorPool entry stayed routable forever and sdRenew kept
// renewing its SD registration without bound. All three must now
// end when the association makes no progress for one window.

type fakeSD struct {
	registers   atomic.Int64
	deregisters atomic.Int64
	renews      atomic.Int64
}

func (f *fakeSD) Register(ctx context.Context, service *sd.Service, opts ...sd.Option) error {
	f.registers.Add(1)
	return nil
}

func (f *fakeSD) Deregister(ctx context.Context, service *sd.Service) error {
	f.deregisters.Add(1)
	return nil
}

func (f *fakeSD) Renew(ctx context.Context, service *sd.Service) error {
	f.renews.Add(1)
	return nil
}

func (f *fakeSD) Get(ctx context.Context, name string) ([]*sd.Service, error) {
	return nil, nil
}

func associateTCPPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	server = <-accepted
	t.Cleanup(func() {
		client.Close()
		server.Close()
	})
	return client, server
}

func setAssociateWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := associateIdleTimeout
	associateIdleTimeout = d
	t.Cleanup(func() { associateIdleTimeout = old })
}

func TestHandleAssociateSilentConnectorIsReaped(t *testing.T) {
	setAssociateWindow(t, 300*time.Millisecond)

	fsd := &fakeSD{}
	h := &routerHandler{
		id:   "test-node",
		pool: NewConnectorPool("test-node"),
	}
	h.md.sd = fsd
	h.md.sdRenewInterval = 20 * time.Millisecond
	h.md.entryPoint = "127.0.0.1:0"

	routerID := relay.NewTunnelID([]byte("0123456789abcdefghij"))
	host := "gw.example"

	_, server := associateTCPPair(t)

	done := make(chan error, 1)
	go func() {
		done <- h.handleAssociate(context.Background(), server, "udp", host, routerID, xlogger.NewLogger())
	}()

	// While the (silent but not yet reaped) association is up, its
	// connector is in the pool and SD renewals are flowing.
	deadline := time.Now().Add(2 * time.Second)
	for h.pool.Get(routerID, host) == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.pool.Get(routerID, host) == nil {
		t.Fatal("connector never entered the pool")
	}

	// The client sends nothing; the association must be reaped
	// with the idle error.
	select {
	case err := <-done:
		if !errors.Is(err, errAssociateIdleTimeout) {
			t.Fatalf("handleAssociate error = %v, want errAssociateIdleTimeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handleAssociate did not return for a silent association")
	}

	// The pool entry is gone and the SD registration was torn down.
	if c := h.pool.Get(routerID, host); c != nil {
		t.Fatal("connector still in pool after the association was reaped")
	}
	if fsd.registers.Load() != 1 {
		t.Fatalf("SD Register calls = %d, want 1", fsd.registers.Load())
	}
	if fsd.deregisters.Load() != 1 {
		t.Fatalf("SD Deregister calls = %d, want 1", fsd.deregisters.Load())
	}
	if fsd.renews.Load() == 0 {
		t.Fatal("SD Renew never ran while the association was alive")
	}

	// Renewals stop with the association: the count stays frozen.
	// A tick that fired just before the deferred cancel lands can
	// still be inside Renew when handleAssociate returns, and under
	// load its increment can land a scheduling quantum after the
	// count is first sampled — so first wait for the count to hold
	// steady across several renew intervals (a loop still running
	// at the renew interval can never hold steady that long), then
	// assert the frozen count exactly as strictly as before.
	frozen := fsd.renews.Load()
	stableDeadline := time.Now().Add(2 * time.Second)
	for {
		time.Sleep(100 * time.Millisecond)
		if got := fsd.renews.Load(); got == frozen {
			break
		} else {
			frozen = got
		}
		if time.Now().After(stableDeadline) {
			t.Fatalf("SD Renew count did not stabilize after teardown (last %d)", frozen)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if got := fsd.renews.Load(); got != frozen {
		t.Fatalf("SD Renew continued after teardown: %d -> %d", frozen, got)
	}
}

func TestHandleAssociatePeerCloseStillEndsCleanly(t *testing.T) {
	// A long window proves the return is caused by the peer
	// closing, not by the idle timeout.
	setAssociateWindow(t, 30*time.Second)

	h := &routerHandler{
		id:   "test-node",
		pool: NewConnectorPool("test-node"),
	}
	routerID := relay.NewTunnelID([]byte("0123456789abcdefghij"))

	client, server := associateTCPPair(t)

	done := make(chan error, 1)
	go func() {
		done <- h.handleAssociate(context.Background(), server, "udp", "gw.example", routerID, xlogger.NewLogger())
	}()

	time.Sleep(100 * time.Millisecond)
	client.Close()

	// The return itself is the assertion: with a 30s window, only
	// the peer close can end the loop this fast. (Whether it
	// surfaces as nil/EOF or as a reset error depends on unread
	// response bytes at close time — both are the pre-existing
	// close semantics.)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleAssociate did not return after the peer closed")
	}
}
