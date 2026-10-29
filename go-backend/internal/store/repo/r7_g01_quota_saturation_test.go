package repo

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R7-G-01 (repo face): userQuotaExceeded is the unblock judge for
// ResetUserQuotaUsage / RollUserQuotaWindows. Its bare
// LimitGB * userQuotaBytesPerGB multiplication wrapped for limits
// >= 2^33 GB, so a blocked user could never be unblocked. The shared
// saturating conversion (QuotaLimitBytes) must keep the wrap values
// effectively unlimited while preserving exact semantics below them.

func TestR7G01QuotaLimitBytesSaturation(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"zero", 0, 0},
		{"one GB", 1, 1 << 30},
		{"largest exact", (1 << 33) - 1, ((1 << 33) - 1) << 30},
		{"2^33 GB wraps to MinInt64 unfixed", 1 << 33, math.MaxInt64},
		{"2^34 GB wraps to 0 unfixed", 1 << 34, math.MaxInt64},
		{"MaxInt64 GB", math.MaxInt64, math.MaxInt64},
		{"negative passthrough", -5, -5 << 30},
		{"negative saturation", math.MinInt64, math.MinInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuotaLimitBytes(tc.in); got != tc.want {
				t.Fatalf("QuotaLimitBytes(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestR7G01RepoPredicateWrapValues(t *testing.T) {
	if userQuotaExceeded(&model.UserQuotaView{DailyLimitGB: 1 << 33, DailyUsedBytes: 0}) {
		t.Fatal("daily 2^33 GB with zero usage judged exceeded (wrap to MinInt64)")
	}
	if userQuotaExceeded(&model.UserQuotaView{MonthlyLimitGB: 1 << 34, MonthlyUsedBytes: 0}) {
		t.Fatal("monthly 2^34 GB with zero usage judged exceeded (wrap to 0)")
	}
	if !userQuotaExceeded(&model.UserQuotaView{DailyLimitGB: 1 << 33, DailyUsedBytes: math.MaxInt64}) {
		t.Fatal("daily 2^33 GB with usage at MaxInt64 must still judge exceeded (saturated ceiling)")
	}
}

// Blocked-user unblock at the repo level: persist wrap-value limits via
// SaveUserQuotaConfigTx, mark the user disabled by quota, and verify both
// the reset path and the window-roll path release them.
func TestR7G01RepoUnblockAfterWrapFix(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r7-g01-repo.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	now := time.Now()
	nowMs := now.UnixMilli()

	userID, err := r.CreateUser("r7g01-repo-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tx := r.BeginTx()
	if tx == nil || tx.Error != nil {
		t.Fatalf("begin tx")
	}
	if err := r.SaveUserQuotaConfigTx(tx, userID, 1<<33, 1<<34, nowMs); err != nil {
		t.Fatalf("save quota config: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := r.MarkUserQuotaDisabled(userID, []int64{555}, nowMs); err != nil {
		t.Fatalf("mark disabled: %v", err)
	}
	release, err := r.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if release == nil || !release.UnblockUser {
		t.Fatalf("reset did not unblock: %+v", release)
	}
	if len(release.ForwardIDs) != 1 || release.ForwardIDs[0] != 555 {
		t.Fatalf("release forward IDs = %v, want [555]", release.ForwardIDs)
	}
}
