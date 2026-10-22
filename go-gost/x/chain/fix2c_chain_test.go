package chain

import (
	"context"
	"errors"
	"strings"
	"testing"

	corechain "github.com/go-gost/core/chain"
	corelogger "github.com/go-gost/core/logger"
	xlogger "github.com/go-gost/x/logger"
)

// FIX2-C #7（V6 P3）：ErrEmptyRoute 裸返回时不带任何定位信息，
// 运维只能把 Router 的错误与 Chain.Route 的日志两相对照。现要求
// 错误文本自带链名（errors.Is(ErrEmptyRoute) 语义不变）：Router
// 两处与 chainRoute 的 Dial/Bind 都要包装。
func TestFix2CRouterDialErrorNamesChain(t *testing.T) {
	corelogger.SetDefault(xlogger.Nop())

	c := NewChain("chains_42")
	c.AddHop(fixedHop{node: nil})

	r := NewRouter(corechain.ChainRouterOption(c))
	_, err := r.Dial(context.Background(), "tcp", "127.0.0.1:9")
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute, got %v", err)
	}
	if !strings.Contains(err.Error(), "chains_42") {
		t.Fatalf("dial error must name the chain, got: %v", err)
	}

	_, err = r.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute from Bind, got %v", err)
	}
	if !strings.Contains(err.Error(), "chains_42") {
		t.Fatalf("bind error must name the chain, got: %v", err)
	}
}

func TestFix2CEmptyRouteErrorNamesChain(t *testing.T) {
	c := NewChain("chains_77")
	rt := NewRoute(ChainRouteOption(c))

	_, err := rt.Dial(context.Background(), "tcp", "127.0.0.1:9")
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute, got %v", err)
	}
	if !strings.Contains(err.Error(), "chains_77") {
		t.Fatalf("route dial error must name the chain, got: %v", err)
	}

	_, err = rt.Bind(context.Background(), "tcp", "127.0.0.1:0")
	if !errors.Is(err, ErrEmptyRoute) {
		t.Fatalf("expected ErrEmptyRoute from Bind, got %v", err)
	}
	if !strings.Contains(err.Error(), "chains_77") {
		t.Fatalf("route bind error must name the chain, got: %v", err)
	}
}
