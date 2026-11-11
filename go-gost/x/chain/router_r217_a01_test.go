package chain_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	corelogger "github.com/go-gost/core/logger"
	xchain "github.com/go-gost/x/chain"
	xhop "github.com/go-gost/x/hop"
	xlogger "github.com/go-gost/x/logger"
	xs "github.com/go-gost/x/selector"
)

// AUDIT-R217 A-01 evidence + regression (real chain/hop/selector
// stack; written before the fix, RED on the f8b07fd2 candidate).

var errR217Probe = errors.New("r217 probe dial failure")

// scriptedTransport fails its first failTimes Dial calls, then
// returns one end of a pipe. Shared counters survive Copy().
type scriptedTransport struct {
	dials     atomic.Int64
	failTimes atomic.Int64
	peers     []net.Conn
}

func (s *scriptedTransport) Dial(ctx context.Context, addr string) (net.Conn, error) {
	n := s.dials.Add(1)
	if n <= s.failTimes.Load() {
		return nil, errR217Probe
	}
	c1, c2 := net.Pipe()
	s.peers = append(s.peers, c2)
	return c1, nil
}
func (s *scriptedTransport) Handshake(_ context.Context, conn net.Conn) (net.Conn, error) {
	return conn, nil
}
func (s *scriptedTransport) Connect(_ context.Context, conn net.Conn, _, _ string) (net.Conn, error) {
	return conn, nil
}
func (s *scriptedTransport) Bind(context.Context, net.Conn, string, string, ...connector.BindOption) (net.Listener, error) {
	return nil, errR217Probe
}
func (s *scriptedTransport) Multiplex() bool { return false }
func (s *scriptedTransport) Options() *corechain.TransportOptions {
	return &corechain.TransportOptions{}
}
func (s *scriptedTransport) Copy() corechain.Transporter { return s }
func (s *scriptedTransport) closePeers() {
	for _, p := range s.peers {
		p.Close()
	}
}

// A-01: two candidates, both fail their first dial inside one
// Router.Dial; after both are tried, the D2 fallback must reselect a
// previously failed candidate and really dial it again (relay.md:
// "候选都试过后才会回头重试已失败候选"). Pre-fix this ends in
// ErrEmptyRoute because the fallback reselection is still killed by
// the nodes' FailFilter marks from this same call.
func TestRouterR217FallbackRedialsMarkedCandidates(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())
	tr1 := &scriptedTransport{}
	tr1.failTimes.Store(1) // fails once, then recovers
	tr2 := &scriptedTransport{}
	tr2.failTimes.Store(1 << 30) // stays down
	defer tr1.closePeers()
	defer tr2.closePeers()
	n1 := corechain.NewNode("node-1", "192.0.2.1:8080", corechain.TransportNodeOption(tr1))
	n2 := corechain.NewNode("node-2", "192.0.2.2:8080", corechain.TransportNodeOption(tr2))

	sel := xs.NewSelector[*corechain.Node](
		xs.FIFOStrategy[*corechain.Node](),
		xs.FailFilter[*corechain.Node](1, time.Minute),
	)
	h := xhop.NewHop(xhop.NodeOption(n1, n2), xhop.SelectorOption(sel), xhop.LoggerOption(xlogger.Nop()))
	c := xchain.NewChain("r217-a01")
	c.AddHop(h)

	r := xchain.NewRouter(
		corechain.ChainRouterOption(c),
		corechain.TimeoutRouterOption(10*time.Second),
		corechain.RetriesRouterOption(2),
	)
	conn, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if err != nil {
		t.Fatalf("Dial err = %v, want success via fallback redial of a recovered candidate (dials: node-1=%d node-2=%d)",
			err, tr1.dials.Load(), tr2.dials.Load())
	}
	defer conn.Close()
	if got := tr1.dials.Load(); got != 2 {
		t.Fatalf("node-1 dials = %d, want 2 (first failure + fallback redial)", got)
	}
	if got := tr2.dials.Load(); got != 1 {
		t.Fatalf("node-2 dials = %d, want 1", got)
	}
	// Invariant ④: the candidate that failed earlier in this call
	// keeps its mark; the one that finally succeeded does not gain
	// one from this call.
	if got := n2.Marker().Count(); got < 1 {
		t.Fatalf("node-2 fail count = %d after the call, want ≥1 (failed candidate stays marked)", got)
	}
	if got := n1.Marker().Count(); got != 0 {
		t.Fatalf("node-1 fail count = %d after its success, want 0 (successful candidate is reset)", got)
	}

	// Invariant ③ (cross-connection): a later, independent Dial that
	// fails on every attempt must leave the failed nodes marked.
	tr1.failTimes.Store(1 << 30)
	_, err = r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if err == nil {
		t.Fatal("second Dial unexpectedly succeeded with both candidates down")
	}
	if got := n1.Marker().Count(); got < 1 {
		t.Fatalf("node-1 fail count = %d after a fully failed Dial, want ≥1", got)
	}
	if got := n2.Marker().Count(); got < 1 {
		t.Fatalf("node-2 fail count = %d after a fully failed Dial, want ≥1", got)
	}
}
