package chain

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	corechain "github.com/go-gost/core/chain"
	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// R2-F3-07：选路全灭的错误带链名（chain <name>: empty route），但节
// 点已选出后在拨号阶段（resolve/transport Dial/Handshake/最终
// Connect）失败时只返回裸传输错误——链名仅在 metrics 标签与选路日
// 志里。节点刚死、失败标记未达选择阈值的那批连接无法按链名定位。
// 现拨号阶段错误同口径带链名（%w 包装，errors.Is 语义不变）。

var errDialStageProbe = errors.New("dial stage probe failure")

type dialFailTransport struct{ stubTransport }

func (dialFailTransport) Dial(context.Context, string) (net.Conn, error) {
	return nil, errDialStageProbe
}

type handshakeFailTransport struct{ stubTransport }

func (handshakeFailTransport) Dial(context.Context, string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func (handshakeFailTransport) Handshake(context.Context, net.Conn) (net.Conn, error) {
	return nil, errDialStageProbe
}

type connectFailTransport struct{ stubTransport }

func (connectFailTransport) Dial(context.Context, string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func (connectFailTransport) Handshake(_ context.Context, conn net.Conn) (net.Conn, error) {
	return conn, nil
}

func (connectFailTransport) Connect(_ context.Context, _ net.Conn, _, _ string) (net.Conn, error) {
	return nil, errDialStageProbe
}

func dialStageRoute(t *testing.T, chainName string, tr corechain.Transporter) *chainRoute {
	t.Helper()
	c := NewChain(chainName)
	c.AddHop(fixedHop{node: corechain.NewNode("hop-a", "127.0.0.1:1", corechain.TransportNodeOption(tr))})
	rt := c.Route(context.Background(), "tcp", "example.com:443")
	if rt == nil || len(rt.Nodes()) != 1 {
		t.Fatalf("expected a one-node route, got %v", rt)
	}
	route, ok := rt.(*chainRoute)
	if !ok {
		t.Fatalf("expected *chainRoute, got %T", rt)
	}
	return route
}

func TestDialStageErrorsNameChain(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	cases := []struct {
		name string
		tr   corechain.Transporter
	}{
		{"transport dial fails", dialFailTransport{}},
		{"handshake fails", handshakeFailTransport{}},
		{"final connect fails", connectFailTransport{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := dialStageRoute(t, "chains_90", tc.tr)
			_, err := rt.Dial(context.Background(), "tcp", "example.com:443")
			if !errors.Is(err, errDialStageProbe) {
				t.Fatalf("expected the probe error, got %v", err)
			}
			if !strings.Contains(err.Error(), "chain chains_90:") {
				t.Fatalf("R2-F3-07: dial-stage error must name the chain, got: %v", err)
			}
		})
	}

	// Bind 走同一 connect() 拨号阶段，同口径带链名。
	rt := dialStageRoute(t, "chains_91", dialFailTransport{})
	_, err := rt.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if !errors.Is(err, errDialStageProbe) {
		t.Fatalf("expected the probe error from Bind, got %v", err)
	}
	if !strings.Contains(err.Error(), "chain chains_91:") {
		t.Fatalf("R2-F3-07: bind-stage error must name the chain, got: %v", err)
	}

	// 无链名的 route（未挂 Chain）保持裸错误，不虚构前缀。
	bare := NewRoute()
	bare.addNode(corechain.NewNode("hop-a", "127.0.0.1:1", corechain.TransportNodeOption(dialFailTransport{})))
	_, err = bare.Dial(context.Background(), "tcp", "example.com:443")
	if !errors.Is(err, errDialStageProbe) {
		t.Fatalf("expected the probe error, got %v", err)
	}
	if strings.Contains(err.Error(), "chain ") {
		t.Fatalf("chainless route must not gain a chain prefix, got: %v", err)
	}
}
