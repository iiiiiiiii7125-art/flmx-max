package udp

import (
	"context"
	"net"
	"testing"

	xbypass "github.com/go-gost/x/bypass"
	bypass_parser "github.com/go-gost/x/config/parsing/bypass"
)

// R3-K1 / A02-001 回归（消费面·生产组合）：Relay 的钉死断言对象
// 必须是生产接线原样组装的 BypassGroup(List("flmx-no-private")),
// 不得再用裸 bypass/stub pinner 充当生产形态——R2 的钉死与备忘
// 测试全用 stub，正是这样漏掉了组合层断裂。本测试只用字面量
// 目标（主机名钉死的逐项一致性由 bypass 包的组合层用例表覆盖，
// 那里可替换包内解析钩子；本包无此钩子且沙箱 DNS 为假解析）。

func TestRelayProductionGroupPinsTargets(t *testing.T) {
	grp := xbypass.BypassGroup(bypass_parser.List(xbypass.NoPrivateBypassName)...)
	r := NewRelay(nil, nil).WithBypass(grp)
	ctx := context.Background()

	// 私网/元数据字面量必须在写出点被拒（修复前：组对象断言
	// 失败，pinTarget 静默原样放行——本断言即红）。
	for _, addr := range []string{"10.0.0.5:53", "192.168.1.1:53", "169.254.169.254:53", "127.0.0.1:53"} {
		if _, err := r.pinTarget(ctx, fakeAddr(addr)); err == nil {
			t.Fatalf("production group: non-public literal %s must be rejected at the relay pin point", addr)
		}
		// 失败不进备忘：同目标再问仍须被拒。
		if _, err := r.pinTarget(ctx, fakeAddr(addr)); err == nil {
			t.Fatalf("production group: rejected literal %s must stay rejected (failures are not memoized)", addr)
		}
	}

	// 公网字面量原样通过（*net.UDPAddr 不被替换）。
	lit := &net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 53}
	got, err := r.pinTarget(ctx, lit)
	if err != nil || got != net.Addr(lit) {
		t.Fatalf("production group: public literal must pass through, got %v err %v", got, err)
	}

	// 公网字面量的字符串形态同样原样通过。
	got, err = r.pinTarget(ctx, fakeAddr("203.0.113.9:53"))
	if err != nil || got.String() != "203.0.113.9:53" {
		t.Fatalf("production group: public literal string must pass through, got %v err %v", got, err)
	}
}
