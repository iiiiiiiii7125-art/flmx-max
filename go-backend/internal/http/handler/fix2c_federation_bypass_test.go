package handler

import "testing"

// FIX2-C #8（FIX-I 未竟项）：联邦 exit 角色服务须带 bypass
// flmx-no-private，使 agent 在连接时拒绝私网/特殊目标。旧 agent
// 对未知 bypass 名是 fail-open（注册表 wrapper 的 Contains 对未
// 注册名恒 false），所以面板只能向版本已支持内置 bypass 的节点
// 下发该字段；版本未知/过低一律不发（行为与现状一致，不假装已
// 防护）。

func TestFix2CAgentSupportsNoPrivateBypass(t *testing.T) {
	cases := map[string]bool{
		"1.3.3":  true,
		"1.3.2":  false, // 本版 agent 尚无内置 bypass
		"v1.3.3": true,
		"1.4.0":  true,
		"1.2.0":  false,
		"":       false,
		"dev":    false,
		"garbage": false,
	}
	for version, want := range cases {
		if got := agentSupportsNoPrivateBypass(version); got != want {
			t.Fatalf("agentSupportsNoPrivateBypass(%q) = %v, want %v", version, got, want)
		}
	}
}

func TestFix2CExitServiceGetsPrivateGuardOnlyWhenSupported(t *testing.T) {
	service := buildFederationServiceConfig("svc-exit", ":40000", "tls", "exit", "", 1, "")
	applyFederationExitPrivateGuard(service, "exit", "1.3.3")
	if service["bypass"] != flmxNoPrivateBypassName {
		t.Fatalf("exit service on a supporting agent must carry the bypass, got %v", service["bypass"])
	}

	serviceOld := buildFederationServiceConfig("svc-exit", ":40000", "tls", "exit", "", 1, "")
	applyFederationExitPrivateGuard(serviceOld, "exit", "1.3.2")
	if _, ok := serviceOld["bypass"]; ok {
		t.Fatal("exit service must NOT carry the bypass for pre-1.3.3 agents (fail-open on unknown names)")
	}

	serviceMiddle := buildFederationServiceConfig("svc-middle", ":40000", "tls", "middle", "chain-x", 1, "")
	applyFederationExitPrivateGuard(serviceMiddle, "middle", "1.3.3")
	if _, ok := serviceMiddle["bypass"]; ok {
		t.Fatal("middle role services must not get the exit private guard")
	}
}
