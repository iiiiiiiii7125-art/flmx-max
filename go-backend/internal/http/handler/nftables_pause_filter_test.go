package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 暂停过滤（A2-03）：nft 转发暂停不能只删 DNAT 规则——conntrack 里的
// NAT 绑定会让已建立连接继续转发。本组测试钉住 sync 层的封禁集派生
// 与落序：封禁先于 reconcile、恢复后 plan 内端口封禁清零。

func opIndex(ops []string, target string) int {
	for i, op := range ops {
		if op == target {
			return i
		}
	}
	return -1
}

func hasBlockedCall(calls []fakeBlockedCall, port int, blocked bool) bool {
	for _, c := range calls {
		if c.port == port && c.blocked == blocked {
			return true
		}
	}
	return false
}

func TestQuotaPauseNftBlocksPortsBeforeReconcile(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-pause-filter", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)
	f2 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 20001)

	h.pauseForwardRecords([]forwardRecord{*f1, *f2}, time.Now().UnixMilli())

	calls := manager.blockedCallList()
	if !hasBlockedCall(calls, 20000, true) || !hasBlockedCall(calls, 20001, true) {
		t.Fatalf("expected both paused ports blocked, got %+v", calls)
	}
	if hasBlockedCall(calls, 20000, false) || hasBlockedCall(calls, 20001, false) {
		t.Fatalf("paused ports must not be unblocked in the same sync, got %+v", calls)
	}
	ops := manager.opList()
	reconcileAt := opIndex(ops, "reconcile")
	if reconcileAt < 0 {
		t.Fatalf("expected a reconcile, ops=%v", ops)
	}
	for _, blockOp := range []string{"block:20000:true", "block:20001:true"} {
		if at := opIndex(ops, blockOp); at < 0 || at > reconcileAt {
			t.Fatalf("expected %s before reconcile, ops=%v", blockOp, ops)
		}
	}
	for _, f := range []*forwardRecord{f1, f2} {
		fr, _ := h.repo.GetForwardRecord(f.ID)
		if fr.Status != model.ForwardStatusPaused {
			t.Fatalf("expected paused, got %d for %d", fr.Status, f.ID)
		}
	}
}

func TestSyncReblocksPausedForwardAndUnblocksActive(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-reblock", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)
	paused := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 20001)
	if err := h.repo.UpdateForwardStatus(paused.ID, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
		t.Fatalf("pause forward in db: %v", err)
	}

	// 稳态 sync（如节点重启后的启动 reconcile）：DB 中已暂停的转发
	// 必须被重新封禁（封禁表随重启丢失），活跃转发的端口则解除任何
	// 残留封禁。
	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("sync node: %v", err)
	}
	calls := manager.blockedCallList()
	if !hasBlockedCall(calls, 20001, true) {
		t.Fatalf("expected paused port 20001 blocked, got %+v", calls)
	}
	if hasBlockedCall(calls, 20001, false) {
		t.Fatalf("paused port 20001 must stay blocked, got %+v", calls)
	}
	if !hasBlockedCall(calls, 20000, false) {
		t.Fatalf("expected active port 20000 unblocked for convergence, got %+v", calls)
	}
	if hasBlockedCall(calls, 20000, true) {
		t.Fatalf("active port 20000 must not be blocked, got %+v", calls)
	}
	ops := manager.opList()
	if at, rec := opIndex(ops, "block:20001:true"), opIndex(ops, "reconcile"); at < 0 || rec < 0 || at > rec {
		t.Fatalf("expected block of paused port before reconcile, ops=%v", ops)
	}
	if at, rec := opIndex(ops, "block:20000:false"), opIndex(ops, "reconcile"); at < 0 || rec < 0 || at < rec {
		t.Fatalf("expected unblock of active port after reconcile, ops=%v", ops)
	}
}

func TestResumeSyncUnblocksPortAndDoesNotReblock(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-resume-filter", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
		t.Fatalf("pause forward in db: %v", err)
	}

	// D6 恢复意图：DB 仍为 paused，但 include 意图优先——不得先封禁
	// 再解除，sync 后封禁必须清零。
	if err := h.syncNftablesNodeWithOverrides(fixture.nodeID, nil, []*forwardRecord{forward}); err != nil {
		t.Fatalf("resume sync: %v", err)
	}
	calls := manager.blockedCallList()
	if hasBlockedCall(calls, 20000, true) {
		t.Fatalf("resumed port must not be blocked, got %+v", calls)
	}
	if !hasBlockedCall(calls, 20000, false) {
		t.Fatalf("expected resumed port 20000 unblocked, got %+v", calls)
	}
}

func TestSyncBlockFailureAbortsBeforeReconcile(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{blockErr: errors.New("ssh down")}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-block-fail", fixture.nodeID)
	paused := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)
	if err := h.repo.UpdateForwardStatus(paused.ID, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
		t.Fatalf("pause forward in db: %v", err)
	}

	err := h.syncNftablesNode(fixture.nodeID)
	if err == nil || !strings.Contains(err.Error(), "ssh down") {
		t.Fatalf("expected block failure to surface, got %v", err)
	}
	if manager.reconcileCount() != 0 {
		t.Fatalf("reconcile must not run when blocking failed, got %d", manager.reconcileCount())
	}
}

func TestQuotaPauseBlockFailureKeepsActive(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{blockErr: errors.New("ssh down")}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-pause-block-fail", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 20000)

	h.pauseForwardRecords([]forwardRecord{*forward}, time.Now().UnixMilli())

	// 封禁未落地则暂停不成立：DB 保持 active，下个计费周期重试，
	// 不出现「已暂停但已建立连接还在跑」的静默缺口。
	fr, _ := h.repo.GetForwardRecord(forward.ID)
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("expected active when blocking failed, got %d", fr.Status)
	}
	if manager.reconcileCount() != 0 {
		t.Fatalf("reconcile must not run when blocking failed, got %d", manager.reconcileCount())
	}
}
