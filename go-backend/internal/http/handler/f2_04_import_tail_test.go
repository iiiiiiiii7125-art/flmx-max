package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// TestBackupImportTailReevaluatesQuotaF204 (F2-04, import tail):
// rows restored over their quota — legal positive counters, e.g. a
// user whose restored daily usage exceeds the restored daily
// limit, or a subscription over its traffic limit — must be
// re-evaluated BEFORE the forward repush pushes the restored state
// live. Pre-fix the tail only repushed, so over-quota restored
// forwards and subscription credentials went live as active.
func TestBackupImportTailReevaluatesQuotaF204(t *testing.T) {
	h, r := newPanelsecHandler(t)

	now := time.Now()
	dayKey := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
	monthKey := int64(now.Year()*100 + int(now.Month()))

	seed := func(row interface{}) {
		t.Helper()
		if err := r.DB().Create(row).Error; err != nil {
			t.Fatalf("seed %T: %v", row, err)
		}
	}
	seed(&model.User{ID: 501, User: "victim", RoleID: 1, Status: 1, CreatedTime: now.UnixMilli()})
	seed(&model.UserQuota{UserID: 501, DailyLimitGB: 1, DailyUsedBytes: 2 << 30,
		DayKey: dayKey, MonthKey: monthKey, CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()})
	seed(&model.Tunnel{ID: 601, Name: "t601", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli(), Status: 1})
	seed(&model.Forward{ID: 801, UserID: 501, UserName: "victim", Name: "f801", TunnelID: 601,
		RemoteAddr: "1.1.1.1:80", Status: model.ForwardStatusActive, CreatedTime: now.UnixMilli()})
	seed(&model.Subscription{ID: 1201, Name: "sub1201", Kind: model.SubscriptionShare,
		Token: strings.Repeat("d", 32), Enabled: 1, AppliedActive: 1,
		UploadBytes: 4096, DownloadBytes: 4096, TrafficLimit: 1024,
		CreatedTime: now.UnixMilli(), UpdatedTime: now.UnixMilli()})

	result := &model.ImportResult{}
	backup := &model.BackupData{Version: "1.0"}
	h.runBackupImportTailHooks(result, backup, []string{"users", "inbounds"})

	var fwd model.Forward
	if err := r.DB().First(&fwd, 801).Error; err != nil {
		t.Fatalf("load forward: %v", err)
	}
	if fwd.Status != model.ForwardStatusPaused {
		t.Fatalf("F2-04: over-quota restored forward not paused by import tail: status=%d, want paused(%d)",
			fwd.Status, model.ForwardStatusPaused)
	}
	var sub model.Subscription
	if err := r.DB().First(&sub, 1201).Error; err != nil {
		t.Fatalf("load subscription: %v", err)
	}
	if sub.AppliedActive != 0 {
		t.Fatalf("F2-04: over-limit restored subscription still applied-active after import tail: %d, want 0", sub.AppliedActive)
	}
}
