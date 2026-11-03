package repo

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 修复轮 9 路 forwardquota：R9-10（I-C2-01，低）。
//
// 配额日/月窗口滚动旧以键不等式触发（repository_user_quota.go 的
// applyUserQuotaWindowRoll，三调用面共用）：面板墙钟回拨跨日/跨
// 月即把窗口键回退、已用量清零，恢复后再清一次；同一回拨经维护
// 轮 RollUserQuotaWindows 还会把已被配额封停的用户误判未超限而
// 主动解禁＋恢复其暂停转发。裁定 §5.7：仅新键 > 已存键才滚动
// （对齐 monthlyFlowDue 的标记单调形态），三调用面同改；回拨时
// 窗口与已用量保持、入账继续计入已存窗口。
//
// 基线（acb03403）红点即 I-C2 探针时序：日窗口真累计 2000 经回
// 拨-恢复两轮只剩 500；封停用户在回拨滚动下产生 UnblockUser。

func fix9QuotaTime(y int, m time.Month, d, hh int) time.Time {
	return time.Date(y, m, d, hh, 0, 0, 0, time.Local)
}

func fix9OpenQuotaRepo(t *testing.T, name string) (*Repository, int64) {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	nowMs := time.Now().UnixMilli()
	userID, err := r.CreateUser(name+"-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return r, userID
}

func fix9QuotaRow(t *testing.T, r *Repository, userID int64) model.UserQuota {
	t.Helper()
	var row model.UserQuota
	if err := r.DB().Where("user_id = ?", userID).First(&row).Error; err != nil {
		t.Fatalf("load quota row: %v", err)
	}
	return row
}

func fix9AddUsage(t *testing.T, r *Repository, userID, used int64, now time.Time) *model.UserQuotaView {
	t.Helper()
	view, err := r.AddUserQuotaUsage(userID, used, now)
	if err != nil {
		t.Fatalf("AddUserQuotaUsage(%d, %d, %v): %v", userID, used, now, err)
	}
	return view
}

// 日窗口：回拨不清零、恢复不再清（I-C2 探针时序的修复后形态）。
func TestFix9R910DayWindowRollbackKeepsUsage(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-day.db")
	nov3 := fix9QuotaTime(2026, 11, 3, 12)
	nov2 := fix9QuotaTime(2026, 11, 2, 12)

	fix9AddUsage(t, r, userID, 1000, nov3)
	row := fix9QuotaRow(t, r, userID)
	if row.DayKey != 20261103 || row.DailyUsedBytes != 1000 || row.MonthlyUsedBytes != 1000 {
		t.Fatalf("premise row = %+v, want dayKey 20261103 daily/monthly 1000", row)
	}

	// 墙钟回拨到 11-02 入账 500：窗口键与已用量保持，新流量计入
	// 已存窗口（基线红点：dayKey 回退 20261102、daily 被清成 500）。
	view := fix9AddUsage(t, r, userID, 500, nov2)
	row = fix9QuotaRow(t, r, userID)
	if row.DayKey != 20261103 || row.DailyUsedBytes != 1500 || row.MonthlyUsedBytes != 1500 {
		t.Fatalf("rollback add must keep stored window, row = %+v, want dayKey 20261103 daily/monthly 1500", row)
	}
	if view.DayKey != 20261103 || view.DailyUsedBytes != 1500 {
		t.Fatalf("rollback view must present stored window, view = %+v", view)
	}

	// 时钟恢复 11-03 再入账 500：不再二次清零，真实累计全额在账
	// （基线终值 500，本格钉 2000）。
	fix9AddUsage(t, r, userID, 500, nov3)
	row = fix9QuotaRow(t, r, userID)
	if row.DailyUsedBytes != 2000 || row.MonthlyUsedBytes != 2000 {
		t.Fatalf("restored-clock add must not re-clear, row = %+v, want daily/monthly 2000", row)
	}
}

// 月窗口：回拨跨月同判（键回退、月用量被清为基线红点）。
func TestFix9R910MonthWindowRollbackKeepsUsage(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-month.db")
	nov1 := fix9QuotaTime(2026, 11, 1, 12)
	oct31 := fix9QuotaTime(2026, 10, 31, 12)

	fix9AddUsage(t, r, userID, 1000, nov1)
	fix9AddUsage(t, r, userID, 500, oct31)
	row := fix9QuotaRow(t, r, userID)
	if row.MonthKey != 202611 || row.MonthlyUsedBytes != 1500 {
		t.Fatalf("month rollback must keep stored month window, row = %+v, want monthKey 202611 monthly 1500", row)
	}
	if row.DayKey != 20261101 || row.DailyUsedBytes != 1500 {
		t.Fatalf("day window must also stay on the stored (later) key, row = %+v", row)
	}
}

// 正常跨日/跨月滚动不回归：新键严格大于已存键照常滚动清零。
func TestFix9R910ForwardRollStillWorks(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-fwd.db")
	fix9AddUsage(t, r, userID, 1000, fix9QuotaTime(2026, 11, 3, 12))
	fix9AddUsage(t, r, userID, 500, fix9QuotaTime(2026, 11, 4, 12))
	row := fix9QuotaRow(t, r, userID)
	if row.DayKey != 20261104 || row.DailyUsedBytes != 500 || row.MonthlyUsedBytes != 1500 {
		t.Fatalf("cross-day roll regressed, row = %+v, want dayKey 20261104 daily 500 monthly 1500", row)
	}
	fix9AddUsage(t, r, userID, 500, fix9QuotaTime(2026, 12, 1, 12))
	row = fix9QuotaRow(t, r, userID)
	if row.MonthKey != 202612 || row.MonthlyUsedBytes != 500 || row.DailyUsedBytes != 500 {
		t.Fatalf("cross-month roll regressed, row = %+v, want monthKey 202612 monthly 500 daily 500", row)
	}
}

// 维护轮：回拨滚动不误解禁封停用户；真跨日滚动照常解禁（设计内
// 释放路径不回归）。
func TestFix9R910MaintenanceRollDoesNotUnblockOnRollback(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-maint.db")
	nov3 := fix9QuotaTime(2026, 11, 3, 12)
	nov2 := fix9QuotaTime(2026, 11, 2, 12)
	nov4 := fix9QuotaTime(2026, 11, 4, 12)
	const gb = int64(1024 * 1024 * 1024)

	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx")
	}
	if err := r.SaveUserQuotaConfigTx(tx, userID, 1, 0, nov3.UnixMilli()); err != nil {
		t.Fatalf("save quota config: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	fix9AddUsage(t, r, userID, 2*gb, nov3)
	if err := r.MarkUserQuotaDisabled(userID, []int64{55}, nov3.UnixMilli()); err != nil {
		t.Fatalf("mark disabled: %v", err)
	}

	// 同日维护轮对照：零解禁（基线即然）。
	releases, err := r.RollUserQuotaWindows(nov3)
	if err != nil {
		t.Fatalf("roll same-day: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("same-day maintenance roll must not release, got %+v", releases)
	}

	// 回拨时刻维护轮：基线红点——回退滚动清零后误判未超限、产生
	// UnblockUser 解禁记录并清 disabled。修复后零释放、行原样。
	releases, err = r.RollUserQuotaWindows(nov2)
	if err != nil {
		t.Fatalf("roll rollback: %v", err)
	}
	if len(releases) != 0 {
		t.Fatalf("rollback maintenance roll must not unblock a quota-disabled user, got %+v", releases)
	}
	row := fix9QuotaRow(t, r, userID)
	if row.DisabledByQuota != 1 || row.DailyUsedBytes != 2*gb || row.DayKey != 20261103 {
		t.Fatalf("rollback roll must leave the row untouched, row = %+v", row)
	}

	// 真跨日维护轮：窗口滚动、用量清零、按设计解禁并交回暂停转发
	// 列表（释放路径不回归）。
	releases, err = r.RollUserQuotaWindows(nov4)
	if err != nil {
		t.Fatalf("roll next-day: %v", err)
	}
	if len(releases) != 1 || !releases[0].UnblockUser || len(releases[0].ForwardIDs) != 1 || releases[0].ForwardIDs[0] != 55 {
		t.Fatalf("genuine next-day roll must release the user with forward 55, got %+v", releases)
	}
	row = fix9QuotaRow(t, r, userID)
	if row.DisabledByQuota != 0 || row.DailyUsedBytes != 0 || row.DayKey != 20261104 {
		t.Fatalf("next-day roll must clear window and disability, row = %+v", row)
	}
}

// 视图归一同向：回拨时视图保留已存窗口（不假清零），且视图只读
// 不落盘；真跨日视图呈现新窗口。
func TestFix9R910QuotaViewIsMonotonicAndReadOnly(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-view.db")
	nov3 := fix9QuotaTime(2026, 11, 3, 12)
	nov2 := fix9QuotaTime(2026, 11, 2, 12)
	nov4 := fix9QuotaTime(2026, 11, 4, 12)

	fix9AddUsage(t, r, userID, 1500, nov3)

	view, err := r.GetUserQuotaView(userID, nov2)
	if err != nil {
		t.Fatalf("get view at rollback time: %v", err)
	}
	if view == nil || view.DayKey != 20261103 || view.DailyUsedBytes != 1500 {
		t.Fatalf("rollback view must present the stored window, view = %+v", view)
	}
	row := fix9QuotaRow(t, r, userID)
	if row.DailyUsedBytes != 1500 || row.DayKey != 20261103 {
		t.Fatalf("view must be read-only, row mutated: %+v", row)
	}

	view, err = r.GetUserQuotaView(userID, nov4)
	if err != nil {
		t.Fatalf("get view next day: %v", err)
	}
	if view == nil || view.DayKey != 20261104 || view.DailyUsedBytes != 0 {
		t.Fatalf("next-day view must present the rolled window, view = %+v", view)
	}
}

// 管理员手动重置前置滚动（三调用面之三）同为单调：回拨时刻重置
// 日窗口只清日用量、不回退窗口键，月窗口原样。
func TestFix9R910ManualResetPreRollIsMonotonic(t *testing.T) {
	r, userID := fix9OpenQuotaRepo(t, "fix9-r910-reset.db")
	nov3 := fix9QuotaTime(2026, 11, 3, 12)
	nov2 := fix9QuotaTime(2026, 11, 2, 12)

	fix9AddUsage(t, r, userID, 1000, nov3)
	if _, err := r.ResetUserQuotaUsage(userID, "daily", nov2); err != nil {
		t.Fatalf("manual reset at rollback time: %v", err)
	}
	row := fix9QuotaRow(t, r, userID)
	if row.DayKey != 20261103 || row.DailyUsedBytes != 0 || row.MonthKey != 202611 || row.MonthlyUsedBytes != 1000 {
		t.Fatalf("manual reset must zero only the daily usage without rolling keys back, row = %+v", row)
	}
}
