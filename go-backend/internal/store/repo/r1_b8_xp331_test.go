package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// X-P3-31 (V-08): seq 只读校验与推进分离——CheckSeq 不得推进
// last_seq/legacy 计数；入账失败时 seq 保持未推进，同 seq 重报可入账。
func TestR1B8_XP331_CheckSeqReadOnlyThenAdvance(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "xp331-seq.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	seq5 := int64(5)

	isReplay, isLegacy, err := r.CheckSeq(9, &seq5)
	if err != nil || isReplay || isLegacy {
		t.Fatalf("fresh node seq=5: replay=%v legacy=%v err=%v", isReplay, isLegacy, err)
	}
	// 只读：校验后不得留下任何 node_seq 状态。
	var count int64
	if err := r.DB().Model(&NodeSeq{}).Where("node_id = ?", 9).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("CheckSeq 不得写入 node_seq（推进须在入账后）, rows=%d", count)
	}

	if err := r.AdvanceSeq(9, &seq5, now); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if isReplay, _, err := r.CheckSeq(9, &seq5); err != nil || !isReplay {
		t.Fatalf("seq=5 推进后应判重放: replay=%v err=%v", isReplay, err)
	}
	seq4 := int64(4)
	if isReplay, _, err := r.CheckSeq(9, &seq4); err != nil || !isReplay {
		t.Fatalf("seq=4 应判重放: replay=%v err=%v", isReplay, err)
	}
	seq6 := int64(6)
	if isReplay, _, err := r.CheckSeq(9, &seq6); err != nil || isReplay {
		t.Fatalf("seq=6 不应判重放: replay=%v err=%v", isReplay, err)
	}

	// legacy：只读校验不计数，AdvanceSeq(nil) 才计数。
	if _, isLegacy, err := r.CheckSeq(9, nil); err != nil || !isLegacy {
		t.Fatalf("nil seq 应为 legacy: legacy=%v err=%v", isLegacy, err)
	}
	var row NodeSeq
	if err := r.DB().Where("node_id = ?", 9).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.LegacyCount != 0 {
		t.Fatalf("CheckSeq(nil) 不得计 legacy, got %d", row.LegacyCount)
	}
	if err := r.AdvanceSeq(9, nil, now); err != nil {
		t.Fatalf("advance legacy: %v", err)
	}
	if err := r.DB().Where("node_id = ?", 9).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.LegacyCount != 1 || row.LastSeq != 5 {
		t.Fatalf("legacy 推进后 legacy_count=%d last_seq=%d, want 1/5", row.LegacyCount, row.LastSeq)
	}
}

// X-P3-31 (V-08): 总量+quota+seq 单事务——quota 段失败时总量不得
// 先行落账、seq 不得推进（旧实现两段分开写，一段失败另一段继续，
// 总量与 quota 分叉且 seq 已消费、同 seq 重报被判重放丢账）。
func TestR1B8_XP331_ApplyFlowUploadAccountingAtomicWithSeq(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "xp331-acct.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "xp331-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "xp331-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER xp331_fail_quota_insert BEFORE INSERT ON user_quota BEGIN SELECT RAISE(FAIL, 'xp331 quota blocked'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	seq := int64(7)
	deltas := []FlowUploadCounterDelta{{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 50, OutFlow: 100}}
	usages := map[int64]int64{2: 150}

	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, &FlowSeqAdvance{NodeID: 9, Seq: &seq}); err == nil {
		t.Fatalf("quota 注入失败时入账应报错")
	}
	var fwd model.Forward
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 0 || fwd.OutFlow != 0 {
		t.Fatalf("quota 失败后总量不得落账（单 tx 回滚）, got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}
	if isReplay, _, err := r.CheckSeq(9, &seq); err != nil || isReplay {
		t.Fatalf("入账失败后 seq 不得推进（同 seq 应可重报）: replay=%v err=%v", isReplay, err)
	}

	// 去除注入后同 seq 重报：整批入账且 seq 推进。
	if err := r.DB().Exec(`DROP TRIGGER xp331_fail_quota_insert`).Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	views, err := r.ApplyFlowUploadAccounting(deltas, usages, now, &FlowSeqAdvance{NodeID: 9, Seq: &seq})
	if err != nil {
		t.Fatalf("重报入账: %v", err)
	}
	if views[2] == nil {
		t.Fatalf("应返回 user 2 的 quota view")
	}
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 100 {
		t.Fatalf("重报后总量应入账, got in=%d out=%d", fwd.InFlow, fwd.OutFlow)
	}
	var quotaCount int64
	if err := r.DB().Table("user_quota").Where("user_id = ?", 2).Count(&quotaCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaCount != 1 {
		t.Fatalf("重报后 quota 应落账, rows=%d", quotaCount)
	}
	if isReplay, _, err := r.CheckSeq(9, &seq); err != nil || !isReplay {
		t.Fatalf("入账成功后 seq 应已推进（重放判定）: replay=%v err=%v", isReplay, err)
	}
}
