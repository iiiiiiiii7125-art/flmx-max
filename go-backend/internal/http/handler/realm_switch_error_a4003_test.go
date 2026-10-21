package handler

import (
	"testing"

	"go-backend/internal/store/repo"
)

// FIX-B A4-003：switchTunnelEngine 原先全程吞错——DB 已切引擎、数据面硬
// 失败（节点不存在/agent 不支持 realm 等）时用户仍收到「成功」。修复后
// 硬失败聚合返回；节点离线仍按自愈语义不算失败（重连对账收敛）。
func TestSwitchTunnelEngineSurfacesHardFailure(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-switch-err.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.13")
	tunnelID := seedTunnelForNftables(t, h, "switch-err-tunnel", nodeID)
	_ = seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20000)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	// 目标节点不存在 → realm 下发硬失败，必须从返回值可见（旧实现吞掉）。
	if err := h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{999999}); err == nil {
		t.Fatalf("A4-003: switch to realm with missing node must surface an error")
	}
}

func TestSwitchTunnelEngineOfflineNodeIsNotHardFailure(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-switch-offline.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.14")
	tunnelID := seedTunnelForNftables(t, h, "switch-offline-tunnel", nodeID)
	_ = seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20000)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	// 节点无 agent 会话（离线）：删除/下发都被自愈语义吸收，不报硬失败，
	// 且 realm 内核行照常建立（与 TestRealmEngineSwitchConverges 同口径）。
	if err := h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID}); err != nil {
		t.Fatalf("offline node switch must not be a hard failure, got %v", err)
	}
}
