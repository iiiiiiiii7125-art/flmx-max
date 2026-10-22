package repo

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R2X-011 (R2-F-01): gost seq 重放防护 TOCTOU——两次只读 CheckSeq
// 都通过（并发双发在入账事务外完成校验）时，第二次入账必须在
// 事务内被 advanceSeqTx 的 RowsAffected 判定拦下（ErrSeqReplay
// 整 tx 回滚），forward/user/user_tunnel/quota 四面纹丝不动。
// 修复前 advanceSeqTx 不判条件更新的 RowsAffected，第二次入账
// 静默成功，总量翻倍（探针实测 100/200，应 50/100）。
func TestR2B3_SeqReplayTOCTOUSecondApplyRejected(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r2b3-seq.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "r2b3-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "r2b3-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}

	seq := int64(7)
	// 确定性交错：两次只读校验都在任何入账之前完成且都通过——
	// 这正是并发双发在 handler 层形成的窗口（校验在事务外）。
	for i := 0; i < 2; i++ {
		isReplay, isLegacy, err := r.CheckSeq(9, &seq)
		if err != nil || isReplay || isLegacy {
			t.Fatalf("check %d: replay=%v legacy=%v err=%v, want pass", i, isReplay, isLegacy, err)
		}
	}

	deltas := []FlowUploadCounterDelta{{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 50, OutFlow: 100}}
	usages := map[int64]int64{2: 150}
	adv := &FlowSeqAdvance{NodeID: 9, Seq: &seq}

	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, adv); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// 第二次入账（同 seq）：必须以 ErrSeqReplay 失败并整 tx 回滚。
	if _, err := r.ApplyFlowUploadAccounting(deltas, usages, now, adv); !errors.Is(err, ErrSeqReplay) {
		t.Fatalf("second apply with same seq: err=%v, want ErrSeqReplay", err)
	}

	var fwd model.Forward
	if err := r.DB().First(&fwd, 20).Error; err != nil {
		t.Fatal(err)
	}
	if fwd.InFlow != 50 || fwd.OutFlow != 100 {
		t.Fatalf("forward double-accounted: in=%d out=%d, want 50/100", fwd.InFlow, fwd.OutFlow)
	}
	var user model.User
	if err := r.DB().First(&user, 2).Error; err != nil {
		t.Fatal(err)
	}
	if user.InFlow != 50 || user.OutFlow != 100 {
		t.Fatalf("user double-accounted: in=%d out=%d, want 50/100", user.InFlow, user.OutFlow)
	}
	var ut model.UserTunnel
	if err := r.DB().First(&ut, 10).Error; err != nil {
		t.Fatal(err)
	}
	if ut.InFlow != 50 || ut.OutFlow != 100 {
		t.Fatalf("user_tunnel double-accounted: in=%d out=%d, want 50/100", ut.InFlow, ut.OutFlow)
	}
	var quotaCount int64
	if err := r.DB().Table("user_quota").Where("user_id = ?", 2).Count(&quotaCount).Error; err != nil {
		t.Fatal(err)
	}
	if quotaCount != 1 {
		t.Fatalf("quota rows=%d, want 1 (second apply must roll back)", quotaCount)
	}
	var quota model.UserQuota
	if err := r.DB().Where("user_id = ?", 2).First(&quota).Error; err != nil {
		t.Fatal(err)
	}
	if quota.DailyUsedBytes != 150 || quota.MonthlyUsedBytes != 150 {
		t.Fatalf("quota double-accounted: daily=%d monthly=%d, want 150/150", quota.DailyUsedBytes, quota.MonthlyUsedBytes)
	}
	// AdvanceSeq 独立入口同口径：已推进的 seq 再推进亦判重放。
	if err := r.AdvanceSeq(9, &seq, nowMs); !errors.Is(err, ErrSeqReplay) {
		t.Fatalf("AdvanceSeq same seq: err=%v, want ErrSeqReplay", err)
	}
}
