package chain

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
	ctxvalue "github.com/go-gost/x/ctx"
)

// D3-04 regression (Router half): Router.Dial/Bind retries re-ran
// route selection with the same context every time, so a retry
// re-selected the node that had just failed — a dead exit was dialled
// first on every retry. Each failed attempt's nodes must join the
// exclude list carried into the next attempt (the hop layer already
// honours ExcludeNodesFromContext; the forward handlers already feed
// it — the Router's own loop did not).

var errStubDial = errors.New("stub dial failure")

type seqRoute struct {
	node *corechain.Node
	ln   net.Listener
}

func (s *seqRoute) Dial(ctx context.Context, network, address string, opts ...corechain.DialOption) (net.Conn, error) {
	return nil, errStubDial
}

func (s *seqRoute) Bind(ctx context.Context, network, address string, opts ...corechain.BindOption) (net.Listener, error) {
	if s.ln != nil {
		return s.ln, nil
	}
	return nil, errStubDial
}

func (s *seqRoute) Nodes() []*corechain.Node { return []*corechain.Node{s.node} }

type seqChainer struct {
	routes []corechain.Route
	seen   [][]string
	i      int
}

func (s *seqChainer) Route(ctx context.Context, network, address string, opts ...corechain.RouteOption) corechain.Route {
	seen := append([]string(nil), ctxvalue.ExcludeNodesFromContext(ctx)...)
	s.seen = append(s.seen, seen)
	rt := s.routes[s.i]
	if s.i < len(s.routes)-1 {
		s.i++
	}
	return rt
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

func TestRouterDialRetriesExcludeFailedNodes(t *testing.T) {
	n1 := corechain.NewNode("node-1", "192.0.2.1:8080")
	n2 := corechain.NewNode("node-2", "192.0.2.2:8080")
	n3 := corechain.NewNode("node-3", "192.0.2.3:8080")
	ch := &seqChainer{routes: []corechain.Route{
		&seqRoute{node: n1}, &seqRoute{node: n2}, &seqRoute{node: n3},
	}}
	r := NewRouter(
		corechain.ChainRouterOption(ch),
		corechain.TimeoutRouterOption(30*time.Second),
		corechain.RetriesRouterOption(2),
	)

	_, err := r.Dial(context.Background(), "tcp", "203.0.113.1:443")
	if !errors.Is(err, errStubDial) {
		t.Fatalf("Dial err = %v, want the stub dial failure", err)
	}
	if len(ch.seen) != 3 {
		t.Fatalf("Route called %d times, want 3 attempts", len(ch.seen))
	}
	if len(ch.seen[0]) != 0 {
		t.Fatalf("attempt 1 exclude list = %v, want empty", ch.seen[0])
	}
	if !containsAll(ch.seen[1], "node-1", "192.0.2.1:8080") {
		t.Fatalf("attempt 2 exclude list = %v, want it to contain attempt 1's node name and addr", ch.seen[1])
	}
	if !containsAll(ch.seen[2], "node-1", "192.0.2.1:8080", "node-2", "192.0.2.2:8080") {
		t.Fatalf("attempt 3 exclude list = %v, want it to contain attempts 1+2 nodes", ch.seen[2])
	}
}

func TestRouterBindRetriesExcludeFailedNodes(t *testing.T) {
	n1 := corechain.NewNode("node-1", "192.0.2.1:8080")
	n2 := corechain.NewNode("node-2", "192.0.2.2:8080")
	okLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer okLn.Close()
	ch := &seqChainer{routes: []corechain.Route{
		&seqRoute{node: n1}, &seqRoute{node: n2, ln: okLn},
	}}
	r := NewRouter(
		corechain.ChainRouterOption(ch),
		corechain.TimeoutRouterOption(30*time.Second),
		corechain.RetriesRouterOption(1),
	)

	ln, err := r.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ln.Close()
	if len(ch.seen) != 2 {
		t.Fatalf("Route called %d times, want 2 attempts", len(ch.seen))
	}
	if !containsAll(ch.seen[1], "node-1", "192.0.2.1:8080") {
		t.Fatalf("bind attempt 2 exclude list = %v, want it to contain attempt 1's node name and addr", ch.seen[1])
	}
}
