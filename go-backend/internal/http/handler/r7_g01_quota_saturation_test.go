package handler

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R7-G-01: quota GB->byte multiplications wrapped for limits >= 2^33 GB
// (product 2^63 wraps to MinInt64; 2^34 GB wraps to 0), so the enforcement
// predicate judged a user with zero usage as over quota, the repo unblock
// predicate agreed (reset/roll could never unblock), and the notify face
// read the wrapped negative limit as "unlimited" and stayed silent.
// Permanent regression: wrap values must behave as effectively unlimited
// (saturating conversion, same caliber as flowLimitBytes) on every face.

func TestR7G01QuotaPredicateWrapValues(t *testing.T) {
	const gb = int64(1) << 30
	cases := []struct {
		name string
		view *model.UserQuotaView
		want bool
	}{
		// 2^33 GB * 2^30 = 2^63, wrapped to MinInt64 before the fix:
		// 0 >= MinInt64 judged "exceeded" with zero usage.
		{"daily 2^33 GB zero usage", &model.UserQuotaView{DailyLimitGB: 1 << 33, DailyUsedBytes: 0}, false},
		// 2^34 GB * 2^30 = 2^64, wrapped to 0 before the fix:
		// 0 >= 0 judged "exceeded" with zero usage.
		{"monthly 2^34 GB zero usage", &model.UserQuotaView{MonthlyLimitGB: 1 << 34, MonthlyUsedBytes: 0}, false},
		{"daily 2^34 GB zero usage", &model.UserQuotaView{DailyLimitGB: 1 << 34, DailyUsedBytes: 0}, false},
		{"monthly 2^33 GB zero usage", &model.UserQuotaView{MonthlyLimitGB: 1 << 33, MonthlyUsedBytes: 0}, false},
		// Saturated limit is MaxInt64: usage at MaxInt64 still counts as
		// reached (fail-closed at the true ceiling, not wrapped-open).
		{"daily 2^33 GB max usage", &model.UserQuotaView{DailyLimitGB: 1 << 33, DailyUsedBytes: math.MaxInt64}, true},
		// Largest non-wrapping limit keeps exact semantics.
		{"daily 2^33-1 GB zero usage", &model.UserQuotaView{DailyLimitGB: (1 << 33) - 1, DailyUsedBytes: 0}, false},
		{"daily 2^33-1 GB exact usage", &model.UserQuotaView{DailyLimitGB: (1 << 33) - 1, DailyUsedBytes: ((1 << 33) - 1) * gb}, true},
		{"daily 2^33-1 GB one byte below", &model.UserQuotaView{DailyLimitGB: (1 << 33) - 1, DailyUsedBytes: ((1 << 33) - 1) * gb - 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserQuotaExceeded(tc.view); got != tc.want {
				t.Fatalf("isUserQuotaExceeded(%+v) = %v, want %v", tc.view, got, tc.want)
			}
		})
	}
}

// Write-entry level: a huge limit persisted through SaveUserQuotaConfigTx
// (the write path behind userCreate/userUpdate) must not block the user,
// and a user already disabled by quota must become unblockable again via
// ResetUserQuotaUsage once the predicate saturates.
func TestR7G01QuotaWriteEntryAndUnblock(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r7-g01.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	nowMs := now.UnixMilli()

	userID, err := r.CreateUser("r7g01-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx: %v", tx)
	}
	if err := r.SaveUserQuotaConfigTx(tx, userID, 1<<33, 1<<34, nowMs); err != nil {
		t.Fatalf("save quota config: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}

	view, err := r.GetUserQuotaView(userID, now)
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	if view.DailyLimitGB != 1<<33 || view.MonthlyLimitGB != 1<<34 {
		t.Fatalf("persisted limits = %d/%d, want %d/%d", view.DailyLimitGB, view.MonthlyLimitGB, int64(1)<<33, int64(1)<<34)
	}
	reason, err := h.userQuotaBlockReason(userID, nowMs)
	if err != nil {
		t.Fatalf("block reason err: %v", err)
	}
	if reason != "" {
		t.Fatalf("user with wrap-value limits and zero usage is blocked: %q", reason)
	}

	// Simulate the pre-fix state: the user was disabled by quota while the
	// wrapped predicate judged them over quota. After the fix a quota
	// reset must release them (the repo predicate is the unblock judge).
	if err := r.MarkUserQuotaDisabled(userID, []int64{777}, nowMs); err != nil {
		t.Fatalf("mark disabled: %v", err)
	}
	release, err := r.ResetUserQuotaUsage(userID, "daily", now)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if release == nil || !release.UnblockUser {
		t.Fatalf("reset did not unblock user with wrap-value limits: %+v", release)
	}
	view, err = r.GetUserQuotaView(userID, now)
	if err != nil || view == nil {
		t.Fatalf("quota view after reset: %v / %v", view, err)
	}
	if view.DisabledByQuota != 0 {
		t.Fatalf("disabled_by_quota = %d after unblocking reset, want 0", view.DisabledByQuota)
	}
}

// Notify face: with a wrap-value limit and usage at the saturated ceiling
// the reminder must take the "full" branch (pre-fix the wrapped negative
// limit took the limit<=0 "unlimited" branch and stayed silent, disagreeing
// with the enforcement face).
func TestR7G01NotifyFaceAgreesOnWrapValues(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "r7-g01-notify.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	u := &model.User{ID: 42, User: "r7g01-notify"}
	view := &model.UserQuotaView{
		UserID:         42,
		DailyLimitGB:   1 << 33,
		DailyUsedBytes: math.MaxInt64,
	}
	h.checkUserQuotaReminders(u, view)
	if !h.notifier.alerted["qday:42:full"] {
		t.Fatalf("notify face did not raise the daily-full alert for a saturated limit at ceiling usage; alerted=%v", h.notifier.alerted)
	}
}
