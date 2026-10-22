package netguard

import (
	"net"
	"net/netip"
	"testing"
)

// R2 批1 关闭门矩阵：「地址形态 × 调用方策略 × 配置」全格断言。
// 策略列：PublicOnly（普通转发/诊断，allowPrivate 不适用）、
// AllowPrivate 关闭、AllowPrivate 开启（SecureFetch/联邦/监控）、
// HardDenyOnly（notify)、HardDenyOrLoopback（导入主机/probe）。

type matrixRow struct {
	addr        string
	publicOnly  bool
	allowOff    bool // PolicyAllowPrivate, allowPrivate=false
	allowOn     bool // PolicyAllowPrivate, allowPrivate=true
	hardOnly    bool // PolicyHardDenyOnly
	hardOrLoop  bool // PolicyHardDenyOrLoopback
}

// 连带断言调整（R3 批1单独登记）：R3X-010 使 PolicyHardDenyOrLoopback
// 补拒 Reserved/Multicast/LinkLocal 后，下表 fe80::1、224.0.0.1、
// ff02::1、240.0.0.1、255.255.255.255、0.1.2.3、192.0.0.1、
// 198.18.0.1、100::1 共 9 行的 hardOrLoop 期望由 true 改 false；
// 其余策略列（PublicOnly/AllowPrivate/HardDenyOnly）期望一字未动。
var matrix = []matrixRow{
	// 公网
	{"8.8.8.8", true, true, true, true, true},
	{"1.1.1.1", true, true, true, true, true},
	{"2606:4700:4700::1111", true, true, true, true, true},
	// 私网 RFC1918 / ULA
	{"10.0.0.1", false, false, true, true, true},
	{"172.16.0.1", false, false, true, true, true},
	{"192.168.1.1", false, false, true, true, true},
	{"fd00::1", false, false, true, true, true},
	{"fc00::1", false, false, true, true, true},
	// loopback
	{"127.0.0.1", false, false, true, true, false},
	{"::1", false, false, true, true, false},
	// CGNAT（非元数据部分不整段硬拒）
	{"100.64.1.2", false, false, true, true, true},
	{"100.127.255.255", false, false, true, true, true},
	// 链路本地：v4 169.254/16 整段硬拒；v6 fe80::/10 归 ClassLinkLocal，
	// 不在硬拒层，仅严格政策 HardDenyOrLoopback 拒（R3X-010 政策分层；
	// N-01 原拟全政策硬拒，1.4.0 合并时按 1.3.7 已发布裁定调和）
	{"169.254.0.23", false, false, false, false, false},
	{"169.254.1.1", false, false, false, false, false},
	{"fe80::1", false, false, true, true, false},
	// 未指定（硬拒）
	{"0.0.0.0", false, false, false, false, false},
	{"::", false, false, false, false, false},
	// 组播
	{"224.0.0.1", false, false, true, true, false},
	{"ff02::1", false, false, true, true, false},
	// 240/4 与广播
	{"240.0.0.1", false, false, true, true, false},
	{"255.255.255.255", false, false, true, true, false},
	// 其余保留段（agent bypass 基底）
	{"0.1.2.3", false, false, true, true, false},
	{"192.0.0.1", false, false, true, true, false},
	{"198.18.0.1", false, false, true, true, false},
	{"100::1", false, false, true, true, false},
	// 三元数据精确集（硬拒）
	{"169.254.169.254", false, false, false, false, false},
	{"100.100.100.200", false, false, false, false, false},
	{"fd00:ec2::254", false, false, false, false, false},
	// IPv4-mapped
	{"::ffff:8.8.8.8", true, true, true, true, true},
	{"::ffff:10.0.0.1", false, false, true, true, true},
	{"::ffff:127.0.0.1", false, false, true, true, false},
	{"::ffff:169.254.169.254", false, false, false, false, false},
	// NAT64 公共 /96 三类：内嵌公网 / 内嵌私网 / 内嵌元数据
	{"64:ff9b::808:808", true, true, true, true, true},
	{"64:ff9b::a00:1", false, false, true, true, true},
	{"64:ff9b::a9fe:a9fe", false, false, false, false, false},
	{"64:ff9b::6464:64c8", false, false, false, false, false}, // 内嵌 100.100.100.200
	{"64:ff9b::7f00:1", false, false, true, true, false},      // 内嵌 127.0.0.1
	// NAT64 本地 /48 整段（内嵌位不提取，恒不按公网放行）
	{"64:ff9b:1::a9fe:a9fe", false, false, false, false, false},
	{"64:ff9b:1::808:808", false, false, false, false, false},
	// 6to4 两类：内嵌公网 / 内嵌非公网
	{"2002:0808:0808::1", true, true, true, true, true},
	{"2002:a9fe:a9fe::1", false, false, false, false, false},
	{"2002:0a00:0001::1", false, false, true, true, true},
}

func TestNetguardMatrix(t *testing.T) {
	for _, row := range matrix {
		a := netip.MustParseAddr(row.addr)
		checks := []struct {
			name    string
			policy  Policy
			allow   bool
			want    bool
		}{
			{"PublicOnly", PolicyPublicOnly, false, row.publicOnly},
			{"AllowPrivate/off", PolicyAllowPrivate, false, row.allowOff},
			{"AllowPrivate/on", PolicyAllowPrivate, true, row.allowOn},
			{"HardDenyOnly", PolicyHardDenyOnly, false, row.hardOnly},
			{"HardDenyOrLoopback", PolicyHardDenyOrLoopback, false, row.hardOrLoop},
		}
		for _, c := range checks {
			d := JudgeAddr(a, c.policy, c.allow)
			if d.Allowed != c.want {
				t.Errorf("Judge(%s, %s, allow=%v) = %v (reason %s, class %s, hard %v), want %v",
					row.addr, c.name, c.allow, d.Allowed, d.Reason, d.Result.Class, d.Result.HardDeny, c.want)
			}
		}
		// net.IP 入口与 netip 入口必须同判。
		ip := net.ParseIP(row.addr)
		if got := Judge(ip, PolicyPublicOnly, false).Allowed; got != row.publicOnly {
			t.Errorf("Judge(net.IP %s, PublicOnly) = %v, want %v", row.addr, got, row.publicOnly)
		}
	}
}

func TestNetguardClassifyTranslations(t *testing.T) {
	r := ClassifyAddr(netip.MustParseAddr("64:ff9b::a9fe:a9fe"))
	if !r.Translated || r.Translation != "nat64" || r.Effective.String() != "169.254.169.254" || !r.HardDeny {
		t.Fatalf("NAT64 metadata classify wrong: %+v", r)
	}
	r = ClassifyAddr(netip.MustParseAddr("64:ff9b:1::808:808"))
	if !r.LocalNAT64 || !r.HardDeny || r.Translated {
		t.Fatalf("local NAT64 must be whole-segment hard deny without extraction: %+v", r)
	}
	r = ClassifyAddr(netip.MustParseAddr("2002:a9fe:a9fe::1"))
	if !r.Translated || r.Translation != "6to4" || r.Effective.String() != "169.254.169.254" || !r.HardDeny {
		t.Fatalf("6to4 metadata classify wrong: %+v", r)
	}
	r = ClassifyAddr(netip.MustParseAddr("::ffff:169.254.169.254"))
	if r.Effective.String() != "169.254.169.254" || !r.HardDeny {
		t.Fatalf("mapped metadata classify wrong: %+v", r)
	}
	if got := EffectiveIP(net.ParseIP("64:ff9b::808:808")); got.String() != "8.8.8.8" {
		t.Fatalf("EffectiveIP(NAT64 public) = %v, want 8.8.8.8", got)
	}
	if !HardDenyIP(net.ParseIP("169.254.0.23")) || HardDenyIP(net.ParseIP("100.64.1.2")) {
		t.Fatal("hard deny segment boundaries wrong")
	}
	if Classify(nil).HardDeny != true || Judge(nil, PolicyHardDenyOnly, false).Allowed {
		t.Fatal("nil IP must be hard-denied")
	}
}
