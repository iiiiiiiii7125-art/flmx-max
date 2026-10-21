package chain

import (
	"context"
	"errors"
	"net"
	"testing"

	corechain "github.com/go-gost/core/chain"
	"github.com/go-gost/core/connector"
	corehop "github.com/go-gost/core/hop"
	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// stubTransport is a no-op corechain.Transporter for building chain nodes
// in route-construction tests.
type stubTransport struct{}

func (stubTransport) Dial(context.Context, string) (net.Conn, error) { return nil, nil }
func (stubTransport) Handshake(_ context.Context, conn net.Conn) (net.Conn, error) {
	return conn, nil
}
func (stubTransport) Connect(_ context.Context, conn net.Conn, _, _ string) (net.Conn, error) {
	return conn, nil
}
func (stubTransport) Bind(context.Context, net.Conn, string, string, ...connector.BindOption) (net.Listener, error) {
	return nil, nil
}
func (stubTransport) Multiplex() bool                      { return false }
func (stubTransport) Options() *corechain.TransportOptions { return &corechain.TransportOptions{} }
func (stubTransport) Copy() corechain.Transporter          { return stubTransport{} }

type fixedHop struct {
	node *corechain.Node
}

func (h fixedHop) Select(context.Context, ...corehop.SelectOption) *corechain.Node {
	return h.node
}

func testNode(name string) *corechain.Node {
	return corechain.NewNode(name, "127.0.0.1:1", corechain.TransportNodeOption(stubTransport{}))
}

// D1-H4: a hop whose nodes are all unavailable must fail the whole route.
// Returning a partial route silently bypasses the remaining hops.
func TestChainRouteFailsClosedWhenMiddleHopUnavailable(t *testing.T) {
	c := NewChain("two-hop")
	c.AddHop(fixedHop{node: testNode("hop1")})
	c.AddHop(fixedHop{node: nil})

	if route := c.Route(context.Background(), "tcp", "example.com:443"); route != nil {
		t.Fatalf("expected nil route when a middle hop has no available node, got %v nodes", len(route.Nodes()))
	}
}

// D1-H4: when the first hop is entirely down the route must fail, not
// degrade to a direct connection.
func TestChainRouteFailsClosedWhenFirstHopUnavailable(t *testing.T) {
	c := NewChain("two-hop")
	c.AddHop(fixedHop{node: nil})
	c.AddHop(fixedHop{node: testNode("hop2")})

	if route := c.Route(context.Background(), "tcp", "example.com:443"); route != nil {
		t.Fatalf("expected nil route when the first hop has no available node, got %v nodes", len(route.Nodes()))
	}
}

func TestChainRouteCompleteWhenAllHopsAvailable(t *testing.T) {
	c := NewChain("two-hop")
	c.AddHop(fixedHop{node: testNode("hop1")})
	c.AddHop(fixedHop{node: testNode("hop2")})

	route := c.Route(context.Background(), "tcp", "example.com:443")
	if route == nil {
		t.Fatal("expected a route when all hops are available")
	}
	if len(route.Nodes()) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(route.Nodes()))
	}
}

// D1-H4: an empty chain route must not fall back to a direct connection.
func TestEmptyChainRouteDialFailsClosed(t *testing.T) {
	rt := NewRoute()
	conn, err := rt.Dial(context.Background(), "tcp", "127.0.0.1:9")
	if conn != nil {
		conn.Close()
		t.Fatal("expected no connection from an empty route")
	}
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute, got %v", err)
	}
}

func TestEmptyChainRouteBindFailsClosed(t *testing.T) {
	rt := NewRoute()
	ln, err := rt.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if ln != nil {
		ln.Close()
		t.Fatal("expected no listener from an empty route")
	}
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute, got %v", err)
	}
}

// D1-H4: Router.Dial with a configured chain whose hop is down must fail
// closed (mirrors Router.Bind), not dial directly.
func TestRouterDialFailsClosedWhenChainHopUnavailable(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	c := NewChain("down-chain")
	c.AddHop(fixedHop{node: nil})

	r := NewRouter(corechain.ChainRouterOption(c))
	conn, err := r.Dial(context.Background(), "tcp", "127.0.0.1:9")
	if conn != nil {
		conn.Close()
		t.Fatal("expected no connection when the chain hop is unavailable")
	}
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute, got %v", err)
	}
}
