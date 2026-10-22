package netguard

import (
	"net/netip"
	"testing"
)

// N-01：fe80::/10（IPv6 链路本地）入硬拒层，与 169.254/16 同口径。
// 此前硬拒集只有 v4 链路本地整段，fe80::/10 属 ClassLinkLocal、
// 不在任一拒绝层，PolicyHardDenyOrLoopback / PolicyHardDenyOnly
// 均放行，与「loopback/link-local 一律拒」的标准口径不一致。
// 分类不变（仍 ClassLinkLocal），仅硬拒子集扩展：fe80 无 zone
// 地址在 Go 拨号下不可路由（链路本地必须带 zone），全部消费方
// （导入/分享、probe、notify、SecureFetch、监控、联邦）都不存在
// 合法 fe80 用途，放行纯属缺口。

func TestN01LinkLocalV6HardDeny(t *testing.T) {
	for _, s := range []string{"fe80::1", "fe80::", "fe80::dead:beef", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff"} {
		a := netip.MustParseAddr(s)
		r := ClassifyAddr(a)
		if !r.HardDeny || r.HardReason != "link_local_segment" {
			t.Errorf("ClassifyAddr(%s) = hard %v reason %q, want hard deny link_local_segment", s, r.HardDeny, r.HardReason)
		}
		if r.Class != ClassLinkLocal {
			t.Errorf("ClassifyAddr(%s) class = %s, want link_local (classification unchanged)", s, r.Class)
		}
		for _, p := range []Policy{PolicyPublicOnly, PolicyAllowPrivate, PolicyHardDenyOnly, PolicyHardDenyOrLoopback} {
			for _, allow := range []bool{false, true} {
				if d := JudgeAddr(a, p, allow); d.Allowed {
					t.Errorf("JudgeAddr(%s, policy %d, allow %v) allowed, want denied (hard deny precedes allowlist)", s, p, allow)
				}
			}
		}
	}
}

// 带 zone 形态：分类链先 WithZone("") 规范化，zone 不得成绕过面。
func TestN01LinkLocalV6ZoneStripped(t *testing.T) {
	a, err := netip.ParseAddr("fe80::1%eth0")
	if err != nil {
		t.Fatal(err)
	}
	if r := ClassifyAddr(a); !r.HardDeny {
		t.Fatalf("ClassifyAddr(fe80::1%%eth0) must be hard-denied after zone normalization: %+v", r)
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
