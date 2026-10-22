package health

import (
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/ws"
)

// R4 批7（R4X-008 面板侧）：节点执行的服务监控补硬拒子集校验
// ValidateNodeMonitorTarget（TCP host:port / ICMP 裸主机两形态）：
// 只判 netguard 硬拒层（字面 + 解析两路，解析失败不拒存）；私网/
// 回环/CGNAT/保留/组播/链路本地一律放行（LAN 监控正当用途，防过修）。
// checker 分发面对存量硬拒目标跳过下发并记结果（不发命令）。

func TestR4B7ValidateNodeMonitorTargetHardDeny(t *testing.T) {
	deny := []struct{ typ, target string }{
		{"tcp", "169.254.169.254:80"},
		{"tcp", "169.254.0.23:443"},
		{"tcp", "100.100.100.200:80"},
		{"tcp", "[fd00:ec2::254]:80"},
		{"tcp", "0.0.0.0:80"},
		{"tcp", "[::]:80"},
		{"tcp", "[64:ff9b::a9fe:a9fe]:80"},   // NAT64 内嵌元数据
		{"tcp", "[2002:a9fe:a9fe::1]:80"},    // 6to4 内嵌元数据
		{"icmp", "169.254.169.254"},
		{"icmp", "0.0.0.0"},
		{"icmp", "fd00:ec2::254"},
	}
	for _, c := range deny {
		if err := ValidateNodeMonitorTarget(c.typ, c.target); err == nil {
			t.Errorf("ValidateNodeMonitorTarget(%s, %q) = nil, want hard-deny error", c.typ, c.target)
		}
	}
	allow := []struct{ typ, target string }{
		{"tcp", "192.168.1.1:443"},  // LAN
		{"tcp", "10.0.0.1:22"},      // 私网
		{"tcp", "127.0.0.1:8080"},   // 回环（节点监控本机服务）
		{"tcp", "[::1]:8080"},
		{"tcp", "100.64.0.1:80"},    // CGNAT
		{"tcp", "[fe80::1]:80"},     // 链路本地（非硬拒层）
		{"tcp", "0.1.2.3:80"},       // 保留段（非硬拒层）
		{"tcp", "224.0.0.1:80"},     // 组播（非硬拒层）
		{"tcp", "8.8.8.8:53"},       // 公网
		{"icmp", "192.168.1.1"},
		{"icmp", "127.0.0.1"},
		{"icmp", "10.0.0.1"},
		{"tcp", "nonexistent.invalid:80"}, // 解析失败不拒存（节点侧 DNS 可能可解析）
		{"icmp", "nonexistent.invalid"},
	}
	for _, c := range allow {
		if err := ValidateNodeMonitorTarget(c.typ, c.target); err != nil {
			t.Errorf("ValidateNodeMonitorTarget(%s, %q) = %v, want nil (防过修)", c.typ, c.target, err)
		}
	}
	// 形态错误仍拒。
	if err := ValidateNodeMonitorTarget("tcp", "192.168.1.1"); err == nil {
		t.Error("tcp target without port must be rejected")
	}
	if err := ValidateNodeMonitorTarget("tcp", "192.168.1.1:99999"); err == nil {
		t.Error("tcp target with out-of-range port must be rejected")
	}
}

// 存量硬拒目标在 checker 分发时跳过：不向节点发命令，结果记失败原因。
func TestR4B7CheckerSkipsHardDeniedStoredTarget(t *testing.T) {
	fake := &fakeCommander{res: ws.CommandResult{Success: true, Data: map[string]interface{}{"success": true}}}
	checker := NewChecker(nil, fake)
	limits := checker.loadServiceMonitorLimits()

	bad := &model.ServiceMonitor{ID: 71, Type: "tcp", Target: "169.254.169.254:80", TimeoutSec: 1, NodeID: 9}
	res := checker.executeCheck(bad, 1000, limits)
	if fake.lastType != "" {
		t.Fatalf("hard-denied stored target must not be dispatched, got command %q", fake.lastType)
	}
	if res.Success != 0 || !strings.Contains(res.ErrorMessage, "不允许") {
		t.Fatalf("hard-denied stored target result = %+v, want failure with 不允许", res)
	}

	// LAN 存量目标照常下发（防过修）。
	fake2 := &fakeCommander{res: ws.CommandResult{Success: true, Data: map[string]interface{}{"success": true}}}
	checker2 := NewChecker(nil, fake2)
	good := &model.ServiceMonitor{ID: 72, Type: "tcp", Target: "192.168.1.1:443", TimeoutSec: 1, NodeID: 9}
	_ = checker2.executeCheck(good, 1000, limits)
	if fake2.lastType != "ServiceMonitorCheck" {
		t.Fatalf("LAN target must be dispatched, got %q", fake2.lastType)
	}
}
