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

// S2 P1-1 回归：Surge 规则模式下 rulePolicyGroups 的成员模板引用「自动选择」，
// Surge 分支必须生成同名组，否则组引用悬空（客户端可能整份配置加载失败）。
// 对渲染结果做闭合断言：[Proxy Group] 成员与 [Rule] 目标引用的每个名字，
// 都必须是已定义的代理名、组名或内置策略（DIRECT/REJECT）。
func TestSubscriptionSurgeRulesGroupClosure(t *testing.T) {
	eps := shareFixtures(t)
	opts := SubOptions{Rules: true, RuleGroups: []string{"adblock", "youtube"}}

	body, _, err := Subscription(FormatSurge, eps, opts)
	if err != nil {
		t.Fatal(err)
	}

	defined := map[string]bool{"DIRECT": true, "REJECT": true}
	var groupMembers, ruleTargets []string
	section := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if line == "" {
			continue
		}
		switch section {
		case "[Proxy]":
			if kv := strings.SplitN(line, " = ", 2); len(kv) == 2 {
				defined[strings.TrimSpace(kv[0])] = true
			}
		case "[Proxy Group]":
			kv := strings.SplitN(line, " = ", 2)
			if len(kv) != 2 {
				t.Fatalf("surge 组行格式异常: %s", line)
			}
			defined[strings.TrimSpace(kv[0])] = true
			fields := strings.Split(kv[1], ",")
			// fields[0] 是组类型，其后是成员；url=/interval= 等 key=value 是参数不是成员。
			for _, f := range fields[1:] {
				f = strings.TrimSpace(f)
				if f != "" && !strings.Contains(f, "=") {
					groupMembers = append(groupMembers, f)
				}
			}
		case "[Rule]":
			fields := strings.Split(line, ",")
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			target := fields[len(fields)-1]
			if target == "no-resolve" && len(fields) >= 2 {
				target = fields[len(fields)-2]
			}
			if fields[0] == "FINAL" && len(fields) >= 2 {
				target = fields[1]
			}
			ruleTargets = append(ruleTargets, target)
		}
	}
	for _, m := range groupMembers {
		if !defined[m] {
			t.Errorf("surge 规则模式组引用悬空: 成员 %q 未定义\n%s", m, body)
		}
	}
	for _, tg := range ruleTargets {
		if !defined[tg] {
			t.Errorf("surge 规则模式规则目标悬空: %q 未定义\n%s", tg, body)
		}
	}
}
