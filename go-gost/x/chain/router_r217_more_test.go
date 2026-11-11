package chain

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	corelogger "github.com/go-gost/core/logger"
	"github.com/go-gost/core/resolver"
	xlogger "github.com/go-gost/x/logger"
)

// AUDIT-R217 make-up tests: B-03 (D3 resolve failures join the retry
// loop) and B-04 (the Bind twin of the retry skeleton gets at least
// one retry assertion of its own).

// flakyResolver fails its first Resolve call and answers afterwards.
type flakyResolver struct {
	calls atomic.Int64
	err   error
}

func (r *flakyResolver) Resolve(_ context.Context, _, _ string, _ ...resolver.Option) ([]net.IP, error) {
	if r.calls.Add(1) == 1 {
		return nil, r.err
	}
	return []net.IP{net.ParseIP("127.0.0.1")}, nil
}

// B-03: a router-level resolve failure is not terminal — the same
// Dial retries, resolves again, and succeeds (router.go D3 branch).
func TestRouterR217ResolveFailureRetries(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())
	errResolve := errors.New("r217 stub resolve failure")
	res := &flakyResolver{err: errResolve}
	node := corechain.NewNode("node-1", "192.0.2.1:8080")
	client, server := net.Pipe()
	defer server.Close()
	rt := &retryRoute{node: node, fn: func(call int, ctx context.Context) (net.Conn, error) {
		return client, nil
	}}
	r := NewRouter(
		corechain.ChainRouterOption(&retryChainer{route: rt}),
		corechain.TimeoutRouterOption(5*time.Second),
		corechain.RetriesRouterOption(2),
		corechain.ResolverRouterOption(res),
	)

	conn, err := r.Dial(context.Background(), "tcp", "r217.test:443")
	if err != nil {
		t.Fatalf("Dial err = %v, want success after the resolve retry", err)
	}
	defer conn.Close()
	if got := res.calls.Load(); got < 2 {
		t.Fatalf("Resolve calls = %d, want ≥2 (first failure must be retried inside the same Dial)", got)
	}
	if len(rt.attempts) != 1 {
		t.Fatalf("route dials = %d, want 1 (only the post-resolve attempt dials)", len(rt.attempts))
	}
}

// bindRoute is a fake chain.Route whose Bind behaviour is scripted
// per call index; it records the attempt contexts it is handed.
type bindRoute struct {
	node  *corechain.Node
	fn    func(call int, ctx context.Context) (net.Listener, error)
	calls []context.Context
}

func (s *bindRoute) Dial(ctx context.Context, network, address string, opts ...corechain.DialOption) (net.Conn, error) {
	return nil, errRetryProbe
}

func (s *bindRoute) Bind(ctx context.Context, network, address string, opts ...corechain.BindOption) (net.Listener, error) {
	s.calls = append(s.calls, ctx)
	return s.fn(len(s.calls)-1, ctx)
}

func (s *bindRoute) Nodes() []*corechain.Node { return []*corechain.Node{s.node} }

// B-04: Bind runs the same retry skeleton as Dial. Two candidates
// fail in turn; the third attempt's plain selection is empty (both
// excluded), the D2 fallback reselects with only the hard exclude
// and the reselected node's second Bind succeeds. Attempt 1's
// context deadline must be an envelope slice (≈E/3), not the full
// envelope.
func TestRouterR217BindRetryFallback(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())
	okLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer okLn.Close()

	n1 := corechain.NewNode("node-1", "192.0.2.1:8080")
	n2 := corechain.NewNode("node-2", "192.0.2.2:8080")
	rtA := &bindRoute{node: n1, fn: func(call int, ctx context.Context) (net.Listener, error) {
		return nil, errRetryProbe
	}}
	rtB := &bindRoute{node: n2, fn: func(call int, ctx context.Context) (net.Listener, error) {
		if call == 0 {
			return nil, errRetryProbe
		}
		return okLn, nil
	}}
	ch := &retryChainer{fn: func(exclude []string, call int) corechain.Route {
		switch {
		case call >= 3:
			return rtB // the fallback reselection (hard exclude only)
		case containsAll(exclude, "node-1", "node-2"):
			return nil // both excluded: forces the D2 fallback
		case containsAll(exclude, "node-1"):
			return rtB
		default:
			return rtA
		}
	}}
	r := retryTestRouter(ch, 10*time.Second, 2)

	ln, err := r.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Bind err = %v, want success via the fallback reselection", err)
	}
	ln.Close()
	if len(rtA.calls) != 1 {
		t.Fatalf("node-1 binds = %d, want 1", len(rtA.calls))
	}
	if len(rtB.calls) != 2 {
		t.Fatalf("node-2 binds = %d, want 2 (failure + fallback redial success)", len(rtB.calls))
	}
	if len(ch.seen) < 4 {
		t.Fatalf("route selections = %d, want ≥4 (3 attempts + the fallback reselection)", len(ch.seen))
	}
	dl, ok := rtA.calls[0].Deadline()
	if !ok {
		t.Fatal("bind attempt 1 context has no deadline")
	}
	if s := time.Until(dl); s < 2500*time.Millisecond || s > 4200*time.Millisecond {
		t.Fatalf("bind attempt 1 slice = %v, want ≈ E/3 = 3.33s of the 10s envelope", s)
	}
}
