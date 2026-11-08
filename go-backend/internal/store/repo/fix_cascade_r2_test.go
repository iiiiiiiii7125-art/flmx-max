package repo

import (
	"database/sql"
	"sort"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX-CASCADE（R2-E1 删除级联完整性）repo 层回归：
//   - E1-11 删节点零端口 Forward 整行删除、多入口只摘端口行；
//   - E1-04 删节点清 flow_legacy_batch_dedupe；
//   - E1-05 删节点清 pending_realm_settle:<nodeID>（及孤儿重查标记）；
//   - E1-01 删隧道墓碑终局分流（无台账/已耗尽/台账节点已删 → 终删，
//     活调度 → 保留）；
//   - E1-12 Forward 删除全入口摘除 PausedForwardIDs；
//   - E1-10 删入站回写清理 outbound_id / split_rules / 订阅 CSV；
//   - E1-09(b) 孤儿重查标记的登记/列举/清除。

func seedCascadeForward(t *testing.T, r *Repository, id, userID, tunnelID int64, status int, now int64) {
	t.Helper()
	createRow(t, r, &model.Forward{ID: id, UserID: userID, UserName: "u", Name: "f", TunnelID: tunnelID, RemoteAddr: "203.0.113.1:80", Status: status, CreatedTime: now, UpdatedTime: now})
}

func sortedIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func assertIDSet(t *testing.T, label string, got, want []int64) {
	t.Helper()
	g, w := sortedIDs(got), sortedIDs(want)
	if len(g) != len(w) {
		t.Fatalf("%s: got %v, want %v", label, g, w)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("%s: got %v, want %v", label, g, w)
		}
	}
}

func TestFixCascadeNodeCascadeDeletesZeroPortForwardOnly(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-node-zeroport.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s1", ServerIP: "10.0.0.1", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.Node{ID: 2, Name: "n2", Secret: "s2", ServerIP: "10.0.0.2", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	// 单入口 Forward 51：唯一端口在本节点。
	seedCascadeForward(t, r, 51, 7, 5, 1, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 51, NodeID: 1, Port: 20001})
	createRow(t, r, &model.NftCounterState{ForwardID: 51, NodeID: 1, Protocol: "tcp", Direction: "in", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NftRuleBinding{ForwardID: 51, NodeID: 1, InPort: 20001, TargetAddr: "203.0.113.1:80", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.NodeInboundRelay{ID: 88, InboundID: 99, ForwardID: 51})
	createRow(t, r, &model.UserInbound{ID: 41, UserID: 9, InboundID: 99, RelayID: 88})
	// 多入口 Forward 52：本节点 + 节点 2 各一口。
	seedCascadeForward(t, r, 52, 7, 5, 1, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 52, NodeID: 1, Port: 20002})
	createRow(t, r, &model.ForwardPort{ForwardID: 52, NodeID: 2, Port: 20003})
	createRow(t, r, &model.UserQuota{UserID: 7, PausedForwardIDs: "51,52", CreatedTime: now, UpdatedTime: now})

	outcome, err := r.DeleteNodeCascadeWithOutcome(1)
	if err != nil {
		t.Fatalf("DeleteNodeCascadeWithOutcome: %v", err)
	}
	assertIDSet(t, "E1-11 outcome deleted forwards", outcome.DeletedForwardIDs, []int64{51})
	if n := countWhere(t, r, "forward", "id = ?", 51); n != 0 {
		t.Fatalf("E1-11: zero-port forward 51 must be deleted, left %d", n)
	}
	for _, table := range []string{"nft_counter_state", "nft_rule_binding", "node_inbound_relay"} {
		if n := countWhere(t, r, table, "forward_id = ?", 51); n != 0 {
			t.Fatalf("E1-11: %s rows for deleted forward 51 left %d", table, n)
		}
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 88); n != 0 {
		t.Fatalf("E1-11: grants on deleted forward's relay left %d", n)
	}
	if n := countWhere(t, r, "forward", "id = ?", 52); n != 1 {
		t.Fatalf("E1-11: multi-entry forward 52 must survive, count=%d", n)
	}
	if n := countWhere(t, r, "forward_port", "forward_id = ?", 52); n != 1 {
		t.Fatalf("E1-11: multi-entry forward 52 must keep its node-2 port only, ports=%d", n)
	}
	if n := countWhere(t, r, "forward_port", "node_id = ?", 1); n != 0 {
		t.Fatalf("node 1 ports must all be removed, left %d", n)
	}
	var quota model.UserQuota
	if err := r.DB().Where("user_id = ?", 7).First(&quota).Error; err != nil {
		t.Fatalf("load quota: %v", err)
	}
	if quota.PausedForwardIDs != "52" {
		t.Fatalf("E1-12: paused_forward_ids = %q, want %q", quota.PausedForwardIDs, "52")
	}
}

func TestFixCascadeNodeCascadeClearsDedupeAndNodeConfigKeys(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-node-keys.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s1", ServerIP: "10.0.0.1", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &model.Node{ID: 2, Name: "n2", Secret: "s2", ServerIP: "10.0.0.2", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, r, &FlowLegacyBatchDedupe{NodeID: 1, BatchHash: "h1", FirstSeen: now})
	createRow(t, r, &FlowLegacyBatchDedupe{NodeID: 2, BatchHash: "h2", FirstSeen: now})
	createRow(t, r, &model.ViteConfig{Name: "pending_realm_settle:1", Value: "{}", Time: now})
	createRow(t, r, &model.ViteConfig{Name: "pending_realm_settle:2", Value: "{}", Time: now})
	if err := r.MarkKernelOrphanRecheck(1, "xray", now); err != nil {
		t.Fatalf("mark orphan recheck: %v", err)
	}

	if err := r.DeleteNodeCascade(1); err != nil {
		t.Fatalf("DeleteNodeCascade: %v", err)
	}
	if n := countWhere(t, r, "flow_legacy_batch_dedupe", "node_id = ?", 1); n != 0 {
		t.Fatalf("E1-04: node cascade left %d dedupe rows", n)
	}
	if n := countWhere(t, r, "flow_legacy_batch_dedupe", "node_id = ?", 2); n != 1 {
		t.Fatalf("E1-04: control node dedupe rows must survive, count=%d", n)
	}
	if n := countWhere(t, r, "vite_config", "name = ?", "pending_realm_settle:1"); n != 0 {
		t.Fatalf("E1-05: node cascade left pending_realm_settle:1")
	}
	if n := countWhere(t, r, "vite_config", "name = ?", "pending_realm_settle:2"); n != 1 {
		t.Fatalf("E1-05: control key pending_realm_settle:2 must survive")
	}
	if n := countWhere(t, r, "vite_config", "name = ?", "kernel_orphan_recheck:1"); n != 0 {
		t.Fatalf("E1-09: node cascade left kernel_orphan_recheck:1")
	}
}

func TestFixCascadeTunnelCascadeTombstoneSplit(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-tunnel-tombstone.db")
	now := time.Now().UnixMilli()
	future := now + 3600_000
	createRow(t, r, &model.Node{ID: 1, Name: "n1", Secret: "s1", ServerIP: "10.0.0.1", Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	// 节点 2 刻意不存在（台账节点已删场景）。
	createRow(t, r, &model.Tunnel{ID: 6, Name: "t6", Type: 1, CreatedTime: now, UpdatedTime: now})
	// 61：活调度墓碑 → 保留。
	seedCascadeForward(t, r, 61, 7, 6, model.ForwardStatusPendingDelete, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 61, NodeID: 1, Port: 20061})
	createRow(t, r, &model.NftPendingDelete{ForwardID: 61, NodeID: 1, NextRetryAt: future, CreatedAt: now, UpdatedAt: now})
	// 62：已耗尽墓碑（next_retry_at=0）→ 终删。
	seedCascadeForward(t, r, 62, 7, 6, model.ForwardStatusPendingDelete, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 62, NodeID: 1, Port: 20062})
	createRow(t, r, &model.NftPendingDelete{ForwardID: 62, NodeID: 1, NextRetryAt: 0, CreatedAt: now, UpdatedAt: now})
	// 63：无台账墓碑 → 按 X-P1-1 原口径保留（该形状生产不可达：
	// 状态翻转唯一写入点与台账建行同事务；K8 契约同口径）。
	seedCascadeForward(t, r, 63, 7, 6, model.ForwardStatusPendingDelete, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 63, NodeID: 1, Port: 20063})
	createRow(t, r, &model.NodeInboundRelay{ID: 89, InboundID: 99, ForwardID: 63})
	createRow(t, r, &model.UserInbound{ID: 42, UserID: 9, InboundID: 99, RelayID: 89})
	// 64：台账节点行不存在但仍在活调度内的墓碑 → 保留（节点删
	// 除后重试必失败，收敛走 24h 耗尽路径，见级联注释）。
	seedCascadeForward(t, r, 64, 7, 6, model.ForwardStatusPendingDelete, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 64, NodeID: 1, Port: 20064})
	createRow(t, r, &model.NftPendingDelete{ForwardID: 64, NodeID: 2, NextRetryAt: future, CreatedAt: now, UpdatedAt: now})
	// 65：普通转发 → 级联删除（不入墓碑分流清单）。
	seedCascadeForward(t, r, 65, 7, 6, 1, now)
	createRow(t, r, &model.ForwardPort{ForwardID: 65, NodeID: 1, Port: 20065})
	createRow(t, r, &model.UserQuota{UserID: 7, PausedForwardIDs: "61,62,65", CreatedTime: now, UpdatedTime: now})

	outcome, err := r.DeleteTunnelCascadeWithOutcome(6)
	if err != nil {
		t.Fatalf("DeleteTunnelCascadeWithOutcome: %v", err)
	}
	assertIDSet(t, "E1-01 finalized", outcome.FinalizedPendingForwardIDs, []int64{62})
	assertIDSet(t, "E1-01 retained", outcome.RetainedPendingForwardIDs, []int64{61, 63, 64})
	if n := countWhere(t, r, "tunnel", "id = ?", 6); n != 0 {
		t.Fatalf("tunnel row must be deleted, left %d", n)
	}
	// 保留墓碑：本行、端口、台账全在（D7）。
	if n := countWhere(t, r, "forward", "id = ?", 61); n != 1 {
		t.Fatalf("E1-01: retained tombstone 61 must survive, count=%d", n)
	}
	if n := countWhere(t, r, "forward_port", "forward_id = ?", 61); n != 1 {
		t.Fatalf("E1-01: retained tombstone ports must survive, count=%d", n)
	}
	if n := countWhere(t, r, "nft_pending_delete", "forward_id = ?", 61); n != 1 {
		t.Fatalf("E1-01: retained tombstone ledger must survive, count=%d", n)
	}
	// 保留墓碑 63（无台账）/64（台账节点已删、仍在调度）：本行与
	// 端口行都在；relay 关联仍按全集清（现行口径）。
	for _, fid := range []int64{63, 64} {
		if n := countWhere(t, r, "forward", "id = ?", fid); n != 1 {
			t.Fatalf("E1-01: retained tombstone %d must survive, count=%d", fid, n)
		}
		if n := countWhere(t, r, "forward_port", "forward_id = ?", fid); n != 1 {
			t.Fatalf("E1-01: retained tombstone %d ports must survive, count=%d", fid, n)
		}
		if n := countWhere(t, r, "nft_pending_delete", "forward_id = ?", fid); fid == 64 && n != 1 {
			t.Fatalf("E1-01: retained tombstone 64 ledger must survive, count=%d", n)
		}
	}
	if n := countWhere(t, r, "node_inbound_relay", "forward_id = ?", 63); n != 0 {
		t.Fatalf("E1-01: retained tombstone relay associations are still cleared, left %d", n)
	}
	// 终删墓碑与普通转发：本行与台账全清。
	for _, fid := range []int64{62, 65} {
		if n := countWhere(t, r, "forward", "id = ?", fid); n != 0 {
			t.Fatalf("E1-01: forward %d must be deleted, left %d", fid, n)
		}
		if n := countWhere(t, r, "nft_pending_delete", "forward_id = ?", fid); n != 0 {
			t.Fatalf("E1-01: ledger for forward %d left %d", fid, n)
		}
		if n := countWhere(t, r, "forward_port", "forward_id = ?", fid); n != 0 {
			t.Fatalf("E1-01: ports for forward %d left %d", fid, n)
		}
	}
	if n := countWhere(t, r, "user_inbound", "relay_id = ?", 89); n != 0 {
		t.Fatalf("E1-01: grants on finalized tombstone relay left %d", n)
	}
	var quota model.UserQuota
	if err := r.DB().Where("user_id = ?", 7).First(&quota).Error; err != nil {
		t.Fatalf("load quota: %v", err)
	}
	if quota.PausedForwardIDs != "61" {
		t.Fatalf("E1-12: paused_forward_ids = %q, want %q (retained tombstone stays, deleted forwards pruned)", quota.PausedForwardIDs, "61")
	}
}

func TestFixCascadeForwardDeletePrunesPausedIDs(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-forward-paused.db")
	now := time.Now().UnixMilli()
	createRow(t, r, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	seedCascadeForward(t, r, 51, 7, 5, 1, now)
	createRow(t, r, &model.UserQuota{UserID: 7, PausedForwardIDs: "51,52", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserQuota{UserID: 8, PausedForwardIDs: "52", CreatedTime: now, UpdatedTime: now})

	if err := r.DeleteForwardCascade(51); err != nil {
		t.Fatalf("DeleteForwardCascade: %v", err)
	}
	var q7, q8 model.UserQuota
	if err := r.DB().Where("user_id = ?", 7).First(&q7).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Where("user_id = ?", 8).First(&q8).Error; err != nil {
		t.Fatal(err)
	}
	if q7.PausedForwardIDs != "52" {
		t.Fatalf("E1-12: paused_forward_ids = %q, want %q", q7.PausedForwardIDs, "52")
	}
	if q8.PausedForwardIDs != "52" {
		t.Fatalf("E1-12: unrelated quota must be untouched, got %q", q8.PausedForwardIDs)
	}
}

func TestFixCascadeInboundDeleteCleansReferences(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-inbound-refs.db")
	now := time.Now().UnixMilli()
	seedNodeWithInbound(t, r, 1, 11, 21, now)
	seedNodeWithInbound(t, r, 2, 12, 99, now)
	// 入站 99 链式指向 21，且分流规则一条指向 21、一条直连。
	if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", 99).Updates(map[string]interface{}{
		"outbound_id": 21,
		"split_rules": `[{"action":"proxy","outboundId":21},{"action":"direct"}]`,
	}).Error; err != nil {
		t.Fatalf("seed refs: %v", err)
	}
	createRow(t, r, &model.Subscription{ID: 3, Name: "s3", Token: "tok", Kind: "share", InboundIDs: "21,99", CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.SubUser{ID: 4, Name: "su", Token: "tok2", UUID: "u", Password: "p", InboundIDs: "21", CreatedTime: now, UpdatedTime: now})

	if err := r.DeleteNodeInbound(21); err != nil {
		t.Fatalf("DeleteNodeInbound: %v", err)
	}
	var survivor model.NodeInbound
	if err := r.DB().Where("id = ?", 99).First(&survivor).Error; err != nil {
		t.Fatalf("load survivor inbound: %v", err)
	}
	// E1-10 分面口径：outbound_id / split_rules 的悬空是 fail-closed
	// 语义载体（构建期渲染 block / Via 无 host 丢弃），回写清零会
	// 把丢弃改成直连/改道——保留悬空逐字不变。
	if survivor.OutboundID != 21 {
		t.Fatalf("E1-10: outbound_id = %d, want 21 retained (fail-closed dangling)", survivor.OutboundID)
	}
	if survivor.SplitRules != `[{"action":"proxy","outboundId":21},{"action":"direct"}]` {
		t.Fatalf("E1-10: split_rules = %s, want verbatim retention (fail-closed dangling)", survivor.SplitRules)
	}
	var sub model.Subscription
	if err := r.DB().Where("id = ?", 3).First(&sub).Error; err != nil {
		t.Fatal(err)
	}
	if sub.InboundIDs != "99" {
		t.Fatalf("E1-10: subscription inbound_ids = %q, want %q", sub.InboundIDs, "99")
	}
	var su model.SubUser
	if err := r.DB().Where("id = ?", 4).First(&su).Error; err != nil {
		t.Fatal(err)
	}
	if su.InboundIDs != "" {
		t.Fatalf("E1-10: sub_user inbound_ids = %q, want empty", su.InboundIDs)
	}
}

func TestFixCascadeKernelDeleteCleansReferences(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-kernel-refs.db")
	now := time.Now().UnixMilli()
	seedNodeWithInbound(t, r, 1, 11, 21, now)
	seedNodeWithInbound(t, r, 2, 12, 99, now)
	if err := r.DB().Model(&model.NodeInbound{}).Where("id = ?", 99).Update("outbound_id", 21).Error; err != nil {
		t.Fatalf("seed refs: %v", err)
	}
	createRow(t, r, &model.Subscription{ID: 3, Name: "s3", Token: "tok", Kind: "share", InboundIDs: "21,99", CreatedTime: now, UpdatedTime: now})

	if err := r.DeleteNodeKernel(11); err != nil {
		t.Fatalf("DeleteNodeKernel: %v", err)
	}
	var survivor model.NodeInbound
	if err := r.DB().Where("id = ?", 99).First(&survivor).Error; err != nil {
		t.Fatalf("load survivor inbound: %v", err)
	}
	if survivor.OutboundID != 21 {
		t.Fatalf("E1-10: outbound_id = %d, want 21 retained (fail-closed dangling)", survivor.OutboundID)
	}
	var sub model.Subscription
	if err := r.DB().Where("id = ?", 3).First(&sub).Error; err != nil {
		t.Fatal(err)
	}
	if sub.InboundIDs != "99" {
		t.Fatalf("E1-10: subscription inbound_ids = %q, want %q", sub.InboundIDs, "99")
	}
}

func TestFixCascadeKernelOrphanRecheckLifecycle(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-orphan-marker.db")
	now := time.Now().UnixMilli()
	if err := r.MarkKernelOrphanRecheck(7, "xray", now); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := r.MarkKernelOrphanRecheck(7, "singbox", now); err != nil {
		t.Fatalf("mark second type: %v", err)
	}
	if err := r.MarkKernelOrphanRecheck(7, "xray", now); err != nil {
		t.Fatalf("mark duplicate type: %v", err)
	}
	types, err := r.KernelOrphanRecheckTypes(7)
	if err != nil {
		t.Fatalf("types: %v", err)
	}
	if len(types) != 2 || types[0] != "singbox" || types[1] != "xray" {
		t.Fatalf("E1-09: orphan types = %v, want [singbox xray] (merged, deduped, sorted)", types)
	}
	ids, err := r.ListKernelOrphanRecheckNodeIDs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	assertIDSet(t, "E1-09 marked nodes", ids, []int64{7})
	if err := r.ClearKernelOrphanRecheck(7); err != nil {
		t.Fatalf("clear: %v", err)
	}
	ids, err = r.ListKernelOrphanRecheckNodeIDs()
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("E1-09: marker must be gone after clear, got %v", ids)
	}
}

func TestFixCascadeDeleteStaleFlowLegacyBatchDedupe(t *testing.T) {
	r := openCascadeRepo(t, "fix-cascade-dedupe-sweep.db")
	now := time.Now().UnixMilli()
	stale := now - FlowLegacyBatchDedupeRetention - 1000
	createRow(t, r, &FlowLegacyBatchDedupe{NodeID: 1, BatchHash: "old", FirstSeen: stale})
	createRow(t, r, &FlowLegacyBatchDedupe{NodeID: 1, BatchHash: "fresh", FirstSeen: now})
	createRow(t, r, &FlowLegacyBatchDedupe{NodeID: 2, BatchHash: "old2", FirstSeen: stale})

	deleted, err := r.DeleteStaleFlowLegacyBatchDedupe(now - FlowLegacyBatchDedupeRetention)
	if err != nil {
		t.Fatalf("DeleteStaleFlowLegacyBatchDedupe: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("E1-04: sweep deleted %d rows, want 2", deleted)
	}
	if n := countWhere(t, r, "flow_legacy_batch_dedupe", "batch_hash = ?", "fresh"); n != 1 {
		t.Fatalf("E1-04: in-window row must survive, count=%d", n)
	}
}
