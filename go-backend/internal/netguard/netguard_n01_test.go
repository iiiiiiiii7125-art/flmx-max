package netguard

import (
	"net"
	"net/netip"
	"testing"
)

// N-01（1.4.0 合并调和）：fe80::/10（IPv6 链路本地）的拒绝口径。
// 本链原方案是把 fe80::/10 塞进 ClassifyAddr 硬拒层、全政策拒；
// 1.3.7 的 R3X-010 已发布裁定为政策分层——fe80 归 ClassLinkLocal、
// 不入硬拒层，仅严格政策 PolicyHardDenyOrLoopback（导入/probe/
// 订阅面）拒，HardDenyOnly（notify）与 AllowPrivate 维持放行。
// 合并以 1.3.7 为准：本文件的断言与 netguard_r3_b1_test.go 钉版
// 同义；导入/分享面的 fe80 拒绝由严格政策达成，断言在 kernel 与
// handler 的 N-01 测试中保留。

func TestN01LinkLocalV6LayeredPolicy(t *testing.T) {
	for _, s := range []string{"fe80::1", "fe80::", "fe80::dead:beef", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff"} {
		a := netip.MustParseAddr(s)
		r := ClassifyAddr(a)
		if r.HardDeny {
			t.Errorf("ClassifyAddr(%s) hard-denied (reason %q), want not in hard-deny layer (1.3.7 R3X-010 layering)", s, r.HardReason)
		}
		if r.Class != ClassLinkLocal {
			t.Errorf("ClassifyAddr(%s) class = %s, want link_local (classification unchanged)", s, r.Class)
		}
		if HardDenyIP(net.ParseIP(s)) {
			t.Errorf("HardDenyIP(%s) = true, want false (fe80 is not in the hard-deny layer)", s)
		}
		// 严格政策拒（导入/probe/订阅面），allowPrivate 不得覆盖。
		for _, allow := range []bool{false, true} {
			if d := JudgeAddr(a, PolicyHardDenyOrLoopback, allow); d.Allowed {
				t.Errorf("JudgeAddr(%s, HardDenyOrLoopback, allow %v) allowed, want denied (strict policy rejects ClassLinkLocal)", s, allow)
			}
		}
		// 其余政策维持 1.3.7 口径：HardDenyOnly 放行；AllowPrivate
		// 开启放行、关闭按非公网拒；PublicOnly 本就全拒非公网。
		if d := JudgeAddr(a, PolicyHardDenyOnly, false); !d.Allowed {
			t.Errorf("JudgeAddr(%s, HardDenyOnly) rejected, want allowed (notify 口径)", s)
		}
		if d := JudgeAddr(a, PolicyAllowPrivate, true); !d.Allowed {
			t.Errorf("JudgeAddr(%s, AllowPrivate/on) rejected, want allowed", s)
		}
		if d := JudgeAddr(a, PolicyAllowPrivate, false); d.Allowed {
			t.Errorf("JudgeAddr(%s, AllowPrivate/off) allowed, want denied (non-public)", s)
		}
		if d := JudgeAddr(a, PolicyPublicOnly, false); d.Allowed {
			t.Errorf("JudgeAddr(%s, PublicOnly) allowed, want denied (non-public)", s)
		}
	}
}

// 带 zone 形态：分类链先 WithZone("") 规范化，zone 不得成绕过面——
// 规范化后仍归 ClassLinkLocal、严格政策仍拒。
func TestN01LinkLocalV6ZoneStripped(t *testing.T) {
	a, err := netip.ParseAddr("fe80::1%eth0")
	if err != nil {
		t.Fatal(err)
	}
	r := ClassifyAddr(a)
	if r.Class != ClassLinkLocal || r.HardDeny {
		t.Fatalf("ClassifyAddr(fe80::1%%eth0) = class %s hard %v, want link_local/not-hard-deny after zone normalization: %+v", r.Class, r.HardDeny, r)
	}
	if d := JudgeAddr(a, PolicyHardDenyOrLoopback, false); d.Allowed {
		t.Fatal("JudgeAddr(zoned fe80::1, HardDenyOrLoopback) must be denied after zone normalization")
	}
}

// 防过修：fe80::/10 之外不得扩拒——fec0::/10（已废弃 site-local，
// 不在 fe80::/10 内）、ULA fc00::/7、公网 v6、RFC1918 逐值不变；
// v4 169.254/16 维持硬拒。
func TestN01HardDenyBoundariesUnchanged(t *testing.T) {
	for _, s := range []string{"fec0::1", "fd00::1", "fc00::1", "2001:db8::9", "2606:4700:4700::1111", "10.0.0.1"} {
		if r := ClassifyAddr(netip.MustParseAddr(s)); r.HardDeny {
			t.Errorf("ClassifyAddr(%s) hard-denied (reason %s), want unchanged (not hard deny)", s, r.HardReason)
		}
	}
	if r := ClassifyAddr(netip.MustParseAddr("169.254.1.1")); !r.HardDeny {
		t.Error("169.254.1.1 must stay hard-denied")
	}
	if d := JudgeAddr(netip.MustParseAddr("fd00::1"), PolicyHardDenyOrLoopback, false); !d.Allowed {
		t.Error("ULA fd00::1 must stay allowed under HardDenyOrLoopback")
	}
}
