package kernel

import (
	"strings"
	"testing"
)

// P2-4 回归保护：opts.Rules == false 时，4 种格式的输出必须与改造前逐字节一致。
// 基准用 golden 文件固化（testdata/ 下），改造后运行此测试即做 diff 比对。
func TestSubscriptionRulesOffRegression(t *testing.T) {
	eps := shareFixtures(t)
	opts := SubOptions{Rules: false}
	cases := []struct {
		format string
		golden string
	}{
		{FormatClash, "p2-clash-rulesoff.golden.yaml"},
		{FormatShadowrocket, "p2-shadowrocket-rulesoff.golden.yaml"},
		{FormatLoon, "p2-loon-rulesoff.golden.txt"},
		{FormatQX, "p2-qx-rulesoff.golden.txt"},
	}
	for _, c := range cases {
		body, _, err := Subscription(c.format, eps, opts)
		if err != nil {
			t.Fatalf("%s: %v", c.format, err)
		}
		assertGolden(t, c.golden, body)
	}
}

// P2 验收：Rules=true 时 Loon/QX/Shadowrocket 应输出完整配置（含策略组与规则），
// 而非纯节点列表；Clash 应包含 proxy-groups 与 rules。
func TestSubscriptionRulesOnConfig(t *testing.T) {
	eps := shareFixtures(t)
	opts := SubOptions{Rules: true, RuleGroups: []string{"adblock", "youtube"}}

	loon, _, err := Subscription(FormatLoon, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loon, "[Proxy Group]") || !strings.Contains(loon, "[Rule]") {
		t.Fatalf("loon rules=true 应输出完整配置（含策略组与规则）:\n%s", loon[:500])
	}

	qx, _, err := Subscription(FormatQX, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(qx, "[policy]") || !strings.Contains(qx, "[filter_remote]") {
		t.Fatalf("qx rules=true 应输出完整配置:\n%s", qx[:500])
	}

	sr, _, err := Subscription(FormatShadowrocket, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sr, "proxy-groups:") || !strings.Contains(sr, "rules:") {
		t.Fatalf("shadowrocket rules=true 应输出 Clash 配置（含 proxy-groups 与 rules）:\n%s", sr[:500])
	}

	clash, _, err := Subscription(FormatClash, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(clash, "proxy-groups:") || !strings.Contains(clash, "rules:") {
		t.Fatalf("clash rules=true 应包含 proxy-groups 与 rules:\n%s", clash[:800])
	}
}
