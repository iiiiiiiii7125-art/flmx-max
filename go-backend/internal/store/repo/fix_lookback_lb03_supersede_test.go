package repo

import (
	"path/filepath"
	"testing"
)

// LB-03 仓储面：无目标回退接管结案（SupersedeNodeUpgradeVerification）
// 的状态机口径——pending/failed 可结案、landed 不可改写、无行/重复
// 结案为无操作、理由与结案时刻落库、新下发覆盖时理由清空。

func TestFixLookbackLB03SupersedePendingAndFailed(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "lb03.db"))

	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatalf("upsert pending: %v", err)
	}
	closed, err := r.SupersedeNodeUpgradeVerification(7, "被无目标回退接管（回退回执无 toVersion，落地未经核验）", 3000)
	if err != nil || !closed {
		t.Fatalf("supersede pending must win: closed=%v err=%v", closed, err)
	}
	rec, err := r.GetNodeUpgradeVerification(7)
	if err != nil || rec == nil {
		t.Fatalf("get after supersede: rec=%v err=%v", rec, err)
	}
	if rec.Status != NodeUpgradeVerifySuperseded || rec.CloseReason == "" || rec.ResolvedAt != 3000 {
		t.Fatalf("superseded record mismatch: %+v", rec)
	}
	// 重复结案无操作（终态）。
	if closed, err := r.SupersedeNodeUpgradeVerification(7, "again", 4000); err != nil || closed {
		t.Fatalf("second supersede must be a no-op: closed=%v err=%v", closed, err)
	}

	// failed 行同样可结案（清 standing 红态）。
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(8, 1000, 2000)); err != nil {
		t.Fatalf("upsert node8: %v", err)
	}
	if won, err := r.ResolveNodeUpgradeVerification(8, NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed, "", false, 2500); err != nil || !won {
		t.Fatalf("fail node8: won=%v err=%v", won, err)
	}
	if closed, err := r.SupersedeNodeUpgradeVerification(8, "reason-8", 3500); err != nil || !closed {
		t.Fatalf("supersede failed must win: closed=%v err=%v", closed, err)
	}
	if rec, _ := r.GetNodeUpgradeVerification(8); rec.Status != NodeUpgradeVerifySuperseded || rec.CloseReason != "reason-8" {
		t.Fatalf("failed->superseded mismatch: %+v", rec)
	}
}

func TestFixLookbackLB03SupersedeNeverTouchesLandedOrMissing(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "lb03b.db"))

	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(9, 1000, 2000)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if won, err := r.ResolveNodeUpgradeVerification(9, NodeUpgradeVerifyPending, NodeUpgradeVerifyLanded, "2.1.0", false, 1500); err != nil || !won {
		t.Fatalf("land node9: won=%v err=%v", won, err)
	}
	if closed, err := r.SupersedeNodeUpgradeVerification(9, "must not apply", 3000); err != nil || closed {
		t.Fatalf("landed must not be superseded: closed=%v err=%v", closed, err)
	}
	if rec, _ := r.GetNodeUpgradeVerification(9); rec.Status != NodeUpgradeVerifyLanded || rec.CloseReason != "" {
		t.Fatalf("landed record must stay untouched: %+v", rec)
	}

	// 无行节点：无操作、无错误。
	if closed, err := r.SupersedeNodeUpgradeVerification(404, "nobody", 3000); err != nil || closed {
		t.Fatalf("missing record supersede must be a no-op: closed=%v err=%v", closed, err)
	}

	// 结案后新下发覆盖：回 pending 且旧理由清空（不得把接管语义
	// 带进新一轮核验）。
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatalf("upsert node7: %v", err)
	}
	if _, err := r.SupersedeNodeUpgradeVerification(7, "old reason", 3000); err != nil {
		t.Fatalf("supersede node7: %v", err)
	}
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 5000, 6000)); err != nil {
		t.Fatalf("re-dispatch upsert: %v", err)
	}
	if rec, _ := r.GetNodeUpgradeVerification(7); rec.Status != NodeUpgradeVerifyPending || rec.CloseReason != "" {
		t.Fatalf("re-dispatched record must restart clean: %+v", rec)
	}
}
