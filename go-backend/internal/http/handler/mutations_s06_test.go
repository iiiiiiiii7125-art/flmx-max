package handler

import (
	"testing"
)

// S-06: 隧道更换入口节点后，旧节点规则必须被清理
// 修复前：cleanupTunnelForwardRuntimesOnRemovedEntryNodes 只调 deleteForwardServicesOnNode，
//
//	nftables 模式下不生效，离线节点静默跳过。
//
// 修复后：nftables 模式下对旧节点做 reconcile（声明式同步移除多余规则）；
//
//	离线节点错误不再静默，由 S-04 机制处理。
//
// X02-001（审计闭环 Round 1 / C17）：TestS06_NftModeUsesReconcile 原为纯
// t.Log 桩、零断言（其名义回归面实际由 forward_change_tunnel_nft_
// a4006_test.go 的真断言守门）。现改为真断言：直接驱动 S-06 的生产函数
// cleanupTunnelForwardRuntimesOnRemovedEntryNodes，断言旧节点被 reconcile
// 且迁走的转发不在旧节点 plan 中；并加阴性对照（入口未变时不得 reconcile）。
func TestS06_RemovedNodesGetCleanup(t *testing.T) {
	// 验证 diffInt64s 正确计算差集
	old := []int64{1, 2, 3}
	new := []int64{2, 3, 4}
	removed := diffInt64s(old, new)
	if len(removed) != 1 || removed[0] != 1 {
		t.Errorf("expected removed=[1], got %v", removed)
	}
}

func TestS06_NftModeUsesReconcile(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-s06", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 22001)

	// 模拟隧道换入口已落库后的状态：forward_port 已迁到新入口节点，
	// 旧节点只剩数据面规则待清理（与 A4-006 的模拟口径一致）。
	if err := h.repo.DB().Exec(`DELETE FROM forward_port WHERE forward_id = ?`, forward.ID).Error; err != nil {
		t.Fatalf("simulate port replacement: %v", err)
	}
	manager.reconcileHit = 0

	// 入口集合未变化时，清理函数不得触碰任何节点（阴性对照）。
	h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(tunnelID, []int64{fixture.nodeID}, []int64{fixture.nodeID})
	if manager.reconcileHit != 0 {
		t.Fatalf("S-06: no removed entry node must mean no reconcile, got %d hits", manager.reconcileHit)
	}

	// 旧节点被移出入口集合：必须对旧节点 reconcile（而非 gost 的删服务），
	// 且收敛后的 plan 不再含已迁走的转发。
	const newEntryNodeID = int64(9999)
	h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(tunnelID, []int64{fixture.nodeID}, []int64{newEntryNodeID})
	if manager.reconcileHit != 1 {
		t.Fatalf("S-06: expected old nft node to be reconciled once, got %d hits", manager.reconcileHit)
	}
	if n := planRuleCountForForward(manager, forward.ID); n != 0 {
		t.Fatalf("S-06: moved forward %d still in old node plan (%d rules)", forward.ID, n)
	}
}
