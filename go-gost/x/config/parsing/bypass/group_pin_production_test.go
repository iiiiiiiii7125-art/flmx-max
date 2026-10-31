package bypass

import (
	"context"
	"testing"

	"github.com/go-gost/core/bypass"
	xbypass "github.com/go-gost/x/bypass"
)

// R3-K1 / A02-001 回归（生产组合形态）：本测试按生产接线原样组装
// ——config/parsing/service 的 handler.BypassOption(
// xbypass.BypassGroup(List(cfg.Bypass, cfg.Bypasses...)...))——
// 断言组对象实现 DialTargetPinner 且钉死行为成立。此前全部钉死
// 测试都用裸 bypass 或手写 stub pinner，组合层断裂因此两轮未被
// 测出；本门钉在生产组合上，禁止再以裸对象/stub 充当生产形态。
func productionGroup(names ...string) bypass.Bypass {
	if len(names) == 0 {
		names = []string{xbypass.NoPrivateBypassName}
	}
	return xbypass.BypassGroup(List(names[0], names[1:]...)...)
}

func TestProductionGroupImplementsDialTargetPinner(t *testing.T) {
	grp := productionGroup()
	if _, ok := grp.(xbypass.DialTargetPinner); !ok {
		t.Fatal("BypassGroup(List(\"flmx-no-private\")) must implement DialTargetPinner: " +
			"internal/net/udp.Relay and handler/ss/udp assert this interface on the handler bypass, " +
			"which production wiring always wraps in a BypassGroup")
	}
}

func TestProductionGroupPinsLiteralTargets(t *testing.T) {
	pinner, ok := productionGroup().(xbypass.DialTargetPinner)
	if !ok {
		t.Fatal("production group must implement DialTargetPinner")
	}
	ctx := context.Background()

	// 公网字面量原样通过。
	got, err := pinner.PinDialTarget(ctx, "udp", "203.0.113.9:443")
	if err != nil || got != "203.0.113.9:443" {
		t.Fatalf("public literal through production group: got %q err %v", got, err)
	}

	// 私网/回环/元数据字面量在落点被拒（修复前组对象断言失败，
	// 这些目标会被逐包面静默原样放行）。
	for _, addr := range []string{
		"127.0.0.1:80", "10.0.0.5:53", "192.168.1.1:443",
		"169.254.169.254:80", "[::1]:53",
	} {
		if _, err := pinner.PinDialTarget(ctx, "udp", addr); err == nil {
			t.Fatalf("non-public literal %s must be rejected through the production group", addr)
		}
	}

	// 空目标（UDP 关联的空地址）原样放行，逐包目标另行钉死。
	got, err = pinner.PinDialTarget(ctx, "udp", "")
	if err != nil || got != "" {
		t.Fatalf("empty target through production group: got %q err %v", got, err)
	}
}

func TestProductionGroupMixedMembersStillPin(t *testing.T) {
	// 组内混有非 pinner 成员（注册表 wrapper 形态的普通 bypass）
	// 时，pinner 成员的钉死不得被稀释。
	grp := productionGroup(xbypass.NoPrivateBypassName, "k1-r3-unregistered-member")
	pinner, ok := grp.(xbypass.DialTargetPinner)
	if !ok {
		t.Fatal("mixed production group must implement DialTargetPinner")
	}
	ctx := context.Background()
	if _, err := pinner.PinDialTarget(ctx, "udp", "192.168.0.8:443"); err == nil {
		t.Fatal("private literal must be rejected through a mixed production group")
	}
	got, err := pinner.PinDialTarget(ctx, "udp", "203.0.113.9:443")
	if err != nil || got != "203.0.113.9:443" {
		t.Fatalf("public literal through mixed production group: got %q err %v", got, err)
	}
	// Contains 拦截判定与钉死成对存在：同一组对私网字面量仍拦截。
	if !grp.Contains(ctx, "udp", "192.168.0.8:443") {
		t.Fatal("production group must still intercept private literals via Contains")
	}
}
