package chain

import (
	"context"
	"net"
	"testing"
	"time"

	corechain "github.com/go-gost/core/chain"
)

// D3-07 regression: Router.Dial/Bind created the per-attempt timeout
// context with `defer cancel()` inside the retry loop, so a
// *successful* attempt's context was cancelled the moment Dial/Bind
// returned — while the returned conn/listener was created under that
// context. Context-retaining transports (gRPC/QUIC/mux sessions built
// over the dialled conn) observed the cancellation after a nominally
// successful dial. The cancel must instead live exactly as long as
// the returned conn/listener: released when it is closed, and
// released immediately for failed attempts.

type captureRoute struct {
	dialCtx context.Context
	bindCtx context.Context
	conn    net.Conn
	ln      net.Listener
}

func (s *captureRoute) Dial(ctx context.Context, network, address string, opts ...corechain.DialOption) (net.Conn, error) {
	s.dialCtx = ctx
	return s.conn, nil
}

func (s *captureRoute) Bind(ctx context.Context, network, address string, opts ...corechain.BindOption) (net.Listener, error) {
	s.bindCtx = ctx
	return s.ln, nil
}

func (s *captureRoute) Nodes() []*corechain.Node {
	return []*corechain.Node{corechain.NewNode("stub", "127.0.0.1:1")}
}

type stubChainer struct{ route corechain.Route }

func (s stubChainer) Route(ctx context.Context, network, address string, opts ...corechain.RouteOption) corechain.Route {
	return s.route
}

type stubListener struct{ closed chan struct{} }

func (l *stubListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *stubListener) Close() error              { close(l.closed); return nil }
func (l *stubListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1} }

func TestRouterDialSuccessDoesNotCancelDialContext(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	route := &captureRoute{conn: client}
	r := NewRouter(
		corechain.ChainRouterOption(stubChainer{route: route}),
		corechain.TimeoutRouterOption(30*time.Second),
		corechain.RetriesRouterOption(0),
	)

	conn, err := r.Dial(context.Background(), "tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if route.dialCtx == nil {
		t.Fatal("route never dialled")
	}
	if err := route.dialCtx.Err(); err != nil {
		t.Fatalf("dial context cancelled after successful Dial returned: %v", err)
	}
	conn.Close()
	if err := route.dialCtx.Err(); err != context.Canceled {
		t.Fatalf("dial context not released when the conn closed: %v", err)
	}
}

func TestRouterBindSuccessDoesNotCancelBindContext(t *testing.T) {
	ln := &stubListener{closed: make(chan struct{})}
	route := &captureRoute{ln: ln}
	r := NewRouter(
		corechain.ChainRouterOption(stubChainer{route: route}),
		corechain.TimeoutRouterOption(30*time.Second),
		corechain.RetriesRouterOption(0),
	)

	got, err := r.Bind(context.Background(), "tcp", "127.0.0.1:8080")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if route.bindCtx == nil {
		t.Fatal("route never bound")
	}
	if err := route.bindCtx.Err(); err != nil {
		t.Fatalf("bind context cancelled after successful Bind returned: %v", err)
	}
	got.Close()
	if err := route.bindCtx.Err(); err != context.Canceled {
		t.Fatalf("bind context not released when the listener closed: %v", err)
	}
}
