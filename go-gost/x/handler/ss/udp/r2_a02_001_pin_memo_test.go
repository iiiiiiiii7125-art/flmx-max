package ss

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/go-gost/core/bypass"
	"github.com/go-gost/core/handler"
	netudp "github.com/go-gost/x/internal/net/udp"
)

// R2-A02-001 回归（ss/udp 面）：pinUDPTarget 的逐包解析必须按关联
// （relayPacket 一次调用 = 一个备忘）收敛，且备忘不得跨关联复用。
// 口径与 internal/net/udp 的 PinMemo 同：同关联同目标只解析一次、
// 结果恒定；新关联重新解析；失败不缓存、逐包 fail-closed 重试。

type ssRotatingPinner struct {
	mu    sync.Mutex
	calls map[string]int
}

func (p *ssRotatingPinner) Contains(context.Context, string, string, ...bypass.Option) bool {
	return false
}
func (p *ssRotatingPinner) IsWhitelist() bool { return false }

func (p *ssRotatingPinner) PinDialTarget(_ context.Context, _, addr string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[addr]++
	switch addr {
	case "rot.test:53":
		if p.calls[addr]%2 == 1 {
			return "203.0.113.1:53", nil
		}
		return "203.0.113.2:53", nil
	case "flaky.test:53":
		if p.calls[addr] == 1 {
			return "", errors.New("transient resolver failure")
		}
		return "203.0.113.3:53", nil
	default:
		return addr, nil
	}
}

func (p *ssRotatingPinner) callCount(addr string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[addr]
}

type ssFakeAddr string

func (a ssFakeAddr) Network() string { return "udp" }
func (a ssFakeAddr) String() string  { return string(a) }

func TestSSPinUDPTargetMemoizedPerAssociation(t *testing.T) {
	pinner := &ssRotatingPinner{}
	h := &ssuHandler{options: handler.Options{Bypass: pinner}}
	ctx := context.Background()

	// 一个关联 = 一个 memo（relayPacket 内创建）。
	memo := &netudp.PinMemo{}
	first, err := h.pinUDPTarget(ctx, memo, ssFakeAddr("rot.test:53"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := h.pinUDPTarget(ctx, memo, ssFakeAddr("rot.test:53"))
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != first.String() {
			t.Fatalf("packet %d drifted within association: got %v, want %v", i, got, first)
		}
	}
	if n := pinner.callCount("rot.test:53"); n != 1 {
		t.Fatalf("PinDialTarget called %d times within one association, want 1", n)
	}

	// 新关联（新 memo）重新解析。
	memo2 := &netudp.PinMemo{}
	if _, err := h.pinUDPTarget(ctx, memo2, ssFakeAddr("rot.test:53")); err != nil {
		t.Fatal(err)
	}
	if n := pinner.callCount("rot.test:53"); n != 2 {
		t.Fatalf("new association did not re-pin: calls = %d, want 2", n)
	}

	// 失败不缓存：同 memo 内首呼失败、次呼重试成功、其后走备忘。
	if _, err := h.pinUDPTarget(ctx, memo, ssFakeAddr("flaky.test:53")); err == nil {
		t.Fatal("first flaky pin must fail")
	}
	got, err := h.pinUDPTarget(ctx, memo, ssFakeAddr("flaky.test:53"))
	if err != nil || got.String() != "203.0.113.3:53" {
		t.Fatalf("flaky retry = %v, %v; want 203.0.113.3:53", got, err)
	}
	if _, err := h.pinUDPTarget(ctx, memo, ssFakeAddr("flaky.test:53")); err != nil {
		t.Fatal(err)
	}
	if n := pinner.callCount("flaky.test:53"); n != 2 {
		t.Fatalf("flaky PinDialTarget calls = %d, want 2", n)
	}

	// 字面量目标保持原样通过（*net.UDPAddr 不被替换）。
	lit := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 53}
	got, err = h.pinUDPTarget(ctx, memo, lit)
	if err != nil || got != net.Addr(lit) {
		t.Fatalf("literal target must pass through, got %v err %v", got, err)
	}
}
