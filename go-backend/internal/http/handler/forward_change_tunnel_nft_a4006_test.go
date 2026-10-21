package handler

import (
	"testing"
)

// FIX-B A4-006：转发换隧道后，被移除的旧 nft 节点规则无人清理——
// deleteForwardServicesOnNodeBatch 只分流 realm 与 gost DeleteService，
// 无 nft 分支，旧 DNAT 与 binding 残留、继续转发并继续计入该转发流量。
// 修复：forward 所属（旧）隧道为 nft 模式时对旧节点 reconcile（声明式收敛）。
func TestDeleteForwardServicesOnNodeBatchReconcilesNftNode(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-change", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 22000)

	// 模拟换隧道已落库后的状态：forward_port 已替换到新隧道入口节点，
	// 旧节点上只剩数据面规则待清理（Batch 收到的是换隧道前的 forward 快照）。
	if err := h.repo.DB().Exec(`DELETE FROM forward_port WHERE forward_id = ?`, forward.ID).Error; err != nil {
		t.Fatalf("simulate port replacement: %v", err)
	}
	manager.reconcileHit = 0

	if err := h.deleteForwardServicesOnNodeBatch(forward, fixture.nodeID); err != nil {
		t.Fatalf("cleanup should succeed via reconcile, got %v", err)
	}
	if manager.reconcileHit != 1 {
		t.Fatalf("A4-006: expected old nft node to be reconciled once, got %d hits", manager.reconcileHit)
	}
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			t.Fatalf("A4-006: moved forward %d still in old node plan", forward.ID)
		}
	}
}
