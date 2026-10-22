package health

import "testing"

// R2 批1（R2X-027 / R2X-006 监控面）：监控判定两层——硬拒先于 allowlist；
// NAT64 按内嵌地址判定。红态（修复前）：allow_private 下元数据/未指定被
// allowlist 放行、NAT64 形态被当公网 v6 放行。

func TestR2B1MonitorHardDenyBeforeAllowlist(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "true")
	t.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "0.0.0.0/0")
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "")
	for _, target := range []string{
		"169.254.169.254:80", "169.254.0.23:80", "0.0.0.0:80", "100.100.100.200:80", "[fd00:ec2::254]:80",
	} {
		if err := ValidateMonitorTCPTarget(target); err == nil {
			t.Fatalf("ValidateMonitorTCPTarget(%q) must be hard-denied even with allow_private", target)
		}
	}
	// allow_private 下普通私网维持可放行（内网监控是该通道设立目的）。
	if err := ValidateMonitorTCPTarget("10.0.0.1:80"); err != nil {
		t.Fatalf("10.0.0.1 with allow_private must stay allowed: %v", err)
	}
}

func TestR2B1MonitorNAT64Forms(t *testing.T) {
	t.Setenv("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS", "")
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "")
	t.Setenv("FLVX_FETCH_PRIVATE_ALLOWLIST", "")
	for _, target := range []string{
		"[64:ff9b::a9fe:a9fe]:80", // NAT64 内嵌元数据
		"[64:ff9b::a00:1]:80",     // NAT64 内嵌 10.0.0.1
		"[64:ff9b:1::808:808]:80", // 本地 NAT64 整段
		"[2002:a9fe:a9fe::1]:80",  // 6to4 内嵌元数据
	} {
		if err := ValidateMonitorTCPTarget(target); err == nil {
			t.Fatalf("ValidateMonitorTCPTarget(%q) must be rejected by default", target)
		}
	}
	// NAT64 内嵌公网按公网处理（防过修）。
	if err := ValidateMonitorTCPTarget("[64:ff9b::808:808]:80"); err != nil {
		t.Fatalf("NAT64 embedded public must stay allowed: %v", err)
	}
	if err := ValidateMonitorTCPTarget("[2002:0808:0808::1]:80"); err != nil {
		t.Fatalf("6to4 embedded public must stay allowed: %v", err)
	}
}
