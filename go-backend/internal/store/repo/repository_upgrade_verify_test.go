package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// A4-V-06 仓储面：落地核验记录的状态机与持久化口径。

func openUpgradeVerifyRepo(t *testing.T, path string) *Repository {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func pendingVerifyRecord(nodeID int64, dispatchedAt, deadlineAt int64) *model.NodeUpgradeVerification {
	return &model.NodeUpgradeVerification{
		NodeID:        nodeID,
		Kind:          "upgrade",
		TargetVersion: "2.1.0",
		ExpectedSHA:   "abc123",
		FromVersion:   "2.0.0 (debian/amd64)",
		DispatchedAt:  dispatchedAt,
		DeadlineAt:    deadlineAt,
		CreatedTime:   dispatchedAt,
		UpdatedTime:   dispatchedAt,
	}
}

func TestUpgradeVerificationUpsertGetAndSupersede(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "uv.db"))
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rec, err := r.GetNodeUpgradeVerification(7)
	if err != nil || rec == nil {
		t.Fatalf("get: rec=%v err=%v", rec, err)
	}
	if rec.Status != NodeUpgradeVerifyPending || rec.TargetVersion != "2.1.0" || rec.ExpectedSHA != "abc123" {
		t.Fatalf("roundtrip mismatch: %+v", rec)
	}

	// 新下发覆盖旧行：目标换版、状态回 pending、旧 observed 清空。
	next := pendingVerifyRecord(7, 3000, 4000)
	next.TargetVersion = "2.2.0"
	next.ObservedVersion = "stale"
	if err := r.UpsertNodeUpgradeVerification(next); err != nil {
		t.Fatalf("supersede upsert: %v", err)
	}
	rec, _ = r.GetNodeUpgradeVerification(7)
	if rec.TargetVersion != "2.2.0" || rec.Status != NodeUpgradeVerifyPending || rec.ObservedVersion != "" || rec.DispatchedAt != 3000 {
		t.Fatalf("supersede mismatch: %+v", rec)
	}

	missing, err := r.GetNodeUpgradeVerification(999)
	if err != nil || missing != nil {
		t.Fatalf("missing node must read (nil, nil): rec=%v err=%v", missing, err)
	}
}

func TestUpgradeVerificationResolveIsConditional(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "uv.db"))
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatal(err)
	}
	won, err := r.ResolveNodeUpgradeVerification(7, NodeUpgradeVerifyPending, NodeUpgradeVerifyLanded, "2.1.0 (debian/amd64)", false, 1500)
	if err != nil || !won {
		t.Fatalf("first resolve must win: won=%v err=%v", won, err)
	}
	// 已 landed 的记录不能被第二方再翻转（告警恰好一次的根）。
	won, err = r.ResolveNodeUpgradeVerification(7, NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed, "", false, 1600)
	if err != nil || won {
		t.Fatalf("second resolve must lose: won=%v err=%v", won, err)
	}
	rec, _ := r.GetNodeUpgradeVerification(7)
	if rec.Status != NodeUpgradeVerifyLanded || rec.ObservedVersion != "2.1.0 (debian/amd64)" || rec.ResolvedAt != 1500 {
		t.Fatalf("resolved record mismatch: %+v", rec)
	}
}

func TestUpgradeVerificationLateLandingFromFailed(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "uv.db"))
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatal(err)
	}
	if won, _ := r.ResolveNodeUpgradeVerification(7, NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed, "2.0.0 (debian/amd64)", false, 2000); !won {
		t.Fatal("fail transition must win")
	}
	won, err := r.ResolveNodeUpgradeVerification(7, NodeUpgradeVerifyFailed, NodeUpgradeVerifyLanded, "2.1.0 (debian/amd64)", true, 3000)
	if err != nil || !won {
		t.Fatalf("late landing must flip failed→landed: won=%v err=%v", won, err)
	}
	rec, _ := r.GetNodeUpgradeVerification(7)
	if rec.Status != NodeUpgradeVerifyLanded || rec.Late != 1 {
		t.Fatalf("late landing mismatch: %+v", rec)
	}
}

func TestUpgradeVerificationDueAndVisibleLists(t *testing.T) {
	r := openUpgradeVerifyRepo(t, filepath.Join(t.TempDir(), "uv.db"))
	// node 7: 已过截止 pending；node 8: 未到截止；node 9: 已 landed。
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(8, 1000, 99999)); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(9, 1000, 2000)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveNodeUpgradeVerification(9, NodeUpgradeVerifyPending, NodeUpgradeVerifyLanded, "2.1.0 (debian/amd64)", false, 1500); err != nil {
		t.Fatal(err)
	}
	due, err := r.ListDueNodeUpgradeVerifications(5000, 200)
	if err != nil || len(due) != 1 || due[0].NodeID != 7 {
		t.Fatalf("due list mismatch: %+v err=%v", due, err)
	}
	visible, err := r.ListVisibleNodeUpgradeVerifications([]int64{7, 8, 9})
	if err != nil || len(visible) != 2 || visible[7] == nil || visible[8] == nil || visible[9] != nil {
		t.Fatalf("visible list must carry pending+failed only: %+v err=%v", visible, err)
	}
	// 置 failed 后仍在可见集（列表面要持续显示未落地）。
	if _, err := r.ResolveNodeUpgradeVerification(7, NodeUpgradeVerifyPending, NodeUpgradeVerifyFailed, "", false, 5000); err != nil {
		t.Fatal(err)
	}
	visible, _ = r.ListVisibleNodeUpgradeVerifications([]int64{7})
	if visible[7] == nil || visible[7].Status != NodeUpgradeVerifyFailed {
		t.Fatalf("failed record must stay visible: %+v", visible[7])
	}
}

func TestUpgradeVerificationPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uv.db")
	r := openUpgradeVerifyRepo(t, path)
	if err := r.UpsertNodeUpgradeVerification(pendingVerifyRecord(7, 1000, 2000)); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// 面板重启等价：新 Repository 实例对同一库文件重开。
	r2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = r2.Close() }()
	rec, err := r2.GetNodeUpgradeVerification(7)
	if err != nil || rec == nil || rec.Status != NodeUpgradeVerifyPending || rec.TargetVersion != "2.1.0" {
		t.Fatalf("pending record must survive reopen: rec=%+v err=%v", rec, err)
	}
}
