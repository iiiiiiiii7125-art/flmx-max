package netguard

import (
	"net/netip"
	"testing"
)

// R3 批1（R3X-010）：PolicyHardDenyOrLoopback 补拒 Reserved/Multicast
// 与 fe80::/10（ClassLinkLocal）。红态（修复前）：下列保留/组播/链路
// 本地字面量在该策略下全部 Allowed=true。
func TestR3B1HardDenyOrLoopbackRejectsReservedMulticastLinkLocal(t *testing.T) {
	deny := []string{
		"0.1.2.3", "0.0.0.1", // 0.0.0.0/8 保留段（F-023 口径）
		"224.0.0.1", "ff02::1", // 组播
		"240.0.0.1", "198.18.0.1", "192.0.0.1", "100::1", // 其余保留段
		"fe80::1", "fe80::70dc:1", // fe80::/10 链路本地
		"64:ff9b::1:203",    // NAT64 内嵌 0.1.2.3
		"2002:0001:0203::1", // 6to4 内嵌 0.1.2.3
		"::ffff:224.0.0.1",  // mapped 组播
	}
	for _, s := range deny {
		a := netip.MustParseAddr(s)
		if d := JudgeAddr(a, PolicyHardDenyOrLoopback, false); d.Allowed {
			t.Errorf("JudgeAddr(%s, HardDenyOrLoopback) must be rejected (class=%s), got allowed", s, d.Result.Class)
		}
	}
}

// 防过修对照：Private / CGNAT / 公网 / ULA 私网在该策略下仍放行
// （合法导入/探测对象，严禁顺手禁 Private/CGNAT）。
func TestR3B1HardDenyOrLoopbackStillAllowsPrivateCGNATPublic(t *testing.T) {
	allow := []string{
		"10.0.0.5", "192.168.1.10", "172.16.0.9", // RFC1918
		"100.64.0.1", "100.127.0.1", // CGNAT
		"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", // 公网
		"fd00::1", "fc00::1", // ULA 私网
		"2001:db8::1", "192.0.2.10", // 文档段在分类器口径下为 public（既有矩阵在位）
	}
	for _, s := range allow {
		a := netip.MustParseAddr(s)
		if d := JudgeAddr(a, PolicyHardDenyOrLoopback, false); !d.Allowed {
			t.Errorf("JudgeAddr(%s, HardDenyOrLoopback) must stay allowed, got rejected reason=%s class=%s", s, d.Reason, d.Result.Class)
		}
	}
}

// 防过修对照：其余策略对 Reserved/Multicast 的既有口径不得被本批
// 牵动——HardDenyOnly（notify）仍只看硬拒层、AllowPrivate 开启仍
// 放行、PublicOnly 本就全拒非公网。
func TestR3B1OtherPoliciesUnchangedForReservedMulticast(t *testing.T) {
	for _, s := range []string{"0.1.2.3", "224.0.0.1", "fe80::1"} {
		a := netip.MustParseAddr(s)
		if d := JudgeAddr(a, PolicyHardDenyOnly, false); !d.Allowed {
			t.Errorf("JudgeAddr(%s, HardDenyOnly) must stay allowed (notify 口径), got %s", s, d.Reason)
		}
		if d := JudgeAddr(a, PolicyAllowPrivate, true); !d.Allowed {
			t.Errorf("JudgeAddr(%s, AllowPrivate/on) must stay allowed, got %s", s, d.Reason)
		}
		if d := JudgeAddr(a, PolicyPublicOnly, false); d.Allowed {
			t.Errorf("JudgeAddr(%s, PublicOnly) must stay rejected", s)
		}
	}
}
