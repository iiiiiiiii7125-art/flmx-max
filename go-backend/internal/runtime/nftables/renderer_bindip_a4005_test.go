package nftables

import (
	"strings"
	"testing"
)

// FIX-B A4-005（BC-03）：nftables 渲染曾完全忽略 BindIP——设了监听 IP 的
// nft 转发实际在节点全部地址上 DNAT。修复后 prerouting 按 BindIP 做
// daddr 匹配、forward 计数链按 ct original daddr 收紧、RuleHash 纳入
// BindIP（变更监听 IP 必须触发规则代际更新）。
func TestRenderTableBindIPRestrictsDNAT(t *testing.T) {
	script := RenderTable(NodePlan{
		NodeID: 10,
		Rules: []Rule{{
			ForwardID:  42,
			InPort:     24000,
			BindIP:     "192.0.2.10",
			TargetHost: "198.51.100.20",
			TargetPort: 443,
			Protocols:  []string{"tcp", "udp"},
		}},
	})
	for _, want := range []string{
		`tcp ip daddr 192.0.2.10 dport 24000 counter dnat ip to 198.51.100.20:443`,
		`udp ip daddr 192.0.2.10 dport 24000 counter dnat ip to 198.51.100.20:443`,
		`ct original ip daddr 192.0.2.10`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("A4-005: script missing %q:\n%s", want, script)
		}
	}
}

func TestRenderTableBindIPv6(t *testing.T) {
	script := RenderTable(NodePlan{
		NodeID: 10,
		Rules: []Rule{{
			ForwardID:  43,
			InPort:     24001,
			BindIP:     "2001:db8::10",
			TargetHost: "198.51.100.20",
			TargetPort: 443,
			Protocols:  []string{"tcp"},
		}},
	})
	if !strings.Contains(script, `tcp ip6 daddr 2001:db8::10 dport 24001`) {
		t.Fatalf("A4-005: expected ip6 daddr match for v6 bind IP:\n%s", script)
	}
}

func TestRenderTableWithoutBindIPUnchanged(t *testing.T) {
	script := RenderTable(NodePlan{
		NodeID: 10,
		Rules: []Rule{{
			ForwardID:  44,
			InPort:     24002,
			TargetHost: "198.51.100.20",
			TargetPort: 443,
			Protocols:  []string{"tcp"},
		}},
	})
	if !strings.Contains(script, `tcp dport 24002 counter dnat ip to 198.51.100.20:443`) {
		t.Fatalf("no-bind render changed:\n%s", script)
	}
	if strings.Contains(script, "daddr") && strings.Contains(script, "ct original ip daddr") {
		t.Fatalf("no-bind render must not add bind matches:\n%s", script)
	}
}

func TestRuleHashIncludesBindIP(t *testing.T) {
	base := Rule{
		ForwardID:  42,
		InPort:     24000,
		TargetHost: "198.51.100.20",
		TargetPort: 443,
		Protocols:  []string{"tcp", "udp"},
	}
	withBind := base
	withBind.BindIP = "192.0.2.10"
	if RuleHash(base) == RuleHash(withBind) {
		t.Fatalf("A4-005: RuleHash must change when BindIP changes (else inIp edits never re-render)")
	}
}
