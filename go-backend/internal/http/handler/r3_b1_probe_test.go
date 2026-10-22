package handler

import (
	"net"
	"testing"

	"go-backend/internal/store/model"
)

// R3 批1（R3X-010）probe 面回归。红态（修复前）：probe 对 0.1.2.3
// 不在拨号前拦截、实际发起连接（沙箱透明代理下甚至返回 OK）。

// probe 执行点：0.1.2.3 必须在拨号前以「目标地址不允许」拦下。
// 该错误文案只由拨号前判定产生，故断言文案即「不发起连接」的证据。
func TestR3B1ProbeRejectsReservedBeforeDial(t *testing.T) {
	row := model.NodeInbound{Listen: "0.1.2.3", Port: 8388, Protocol: "ss"}
	p := probeExternalInbound(t.Context(), row)
	if p.OK {
		t.Fatal("probe of 0.1.2.3 must not succeed")
	}
	if p.Error != "目标地址不允许" {
		t.Fatalf("probe of 0.1.2.3 must be blocked before dial with 目标地址不允许, got %q", p.Error)
	}
}

// C-S2 顺手验证：组播别名 ip6-allhosts 经解析（ff02::1）后在 probe
// 执行点被拦（解析结果注入，确定性验证）。
func TestR3B1ProbeRejectsMulticastAliasAfterResolution(t *testing.T) {
	orig := externalProbeLookupIP
	externalProbeLookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("ff02::1")}, nil
	}
	defer func() { externalProbeLookupIP = orig }()

	row := model.NodeInbound{Listen: "ip6-allhosts", Port: 8388, Protocol: "ss"}
	p := probeExternalInbound(t.Context(), row)
	if p.OK || p.Error != "目标地址不允许" {
		t.Fatalf("probe of ip6-allhosts (resolves ff02::1) must be blocked before dial, got %+v", p)
	}
}
