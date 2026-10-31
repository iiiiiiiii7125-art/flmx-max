package ss

import (
	"context"
	"net"
	"testing"

	"github.com/go-gost/core/handler"
	xbypass "github.com/go-gost/x/bypass"
	bypass_parser "github.com/go-gost/x/config/parsing/bypass"
	netudp "github.com/go-gost/x/internal/net/udp"
)

// R3-K1 / A02-001 回归（消费面·生产组合）：ss-udp 的 pinUDPTarget
// 断言对象必须是生产接线原样组装的 BypassGroup(
// List("flmx-no-private"))，不得再用裸 bypass/stub pinner 充当
// 生产形态。字面量口径同 internal/net/udp 的生产组合用例。

func TestSSProductionGroupPinsTargets(t *testing.T) {
	grp := xbypass.BypassGroup(bypass_parser.List(xbypass.NoPrivateBypassName)...)
	h := &ssuHandler{options: handler.Options{Bypass: grp}}
	ctx := context.Background()
	memo := &netudp.PinMemo{}

	// 私网/元数据字面量必须在写出点被拒（修复前组对象断言失败、
	// 静默原样放行——本断言即红）；失败不进备忘、逐问仍拒。
	for _, addr := range []string{"10.0.0.5:53", "192.168.1.1:53", "169.254.169.254:53", "127.0.0.1:53"} {
		if _, err := h.pinUDPTarget(ctx, memo, ssFakeAddr(addr)); err == nil {
			t.Fatalf("production group: non-public literal %s must be rejected at the ss-udp pin point", addr)
		}
		if _, err := h.pinUDPTarget(ctx, memo, ssFakeAddr(addr)); err == nil {
			t.Fatalf("production group: rejected literal %s must stay rejected (failures are not memoized)", addr)
		}
	}

	// 公网字面量原样通过。
	lit := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 53}
	got, err := h.pinUDPTarget(ctx, memo, lit)
	if err != nil || got != net.Addr(lit) {
		t.Fatalf("production group: public literal must pass through, got %v err %v", got, err)
	}
}
