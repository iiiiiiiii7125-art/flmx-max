package handler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// Minute-level maintenance loop coverage: the reset/expiry round used
// to run once a day at 00:00:05; it now runs once at startup and then
// ticks every minute. These tests pin the three behaviors that change
// buys: the startup pass, prompt expiry disable, and monthly-reset
// catch-up staying exactly-once under the higher frequency.

func openMaintenanceLoopRepo(t *testing.T, name string) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func seedMaintenanceLoopUser(t *testing.T, r *repo.Repository, id int64, expMs int64) {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(?, 'maint_user', 'x', 1, ?, 100, 0, 0, 0, 1, ?, ?, 1)
	`, id, expMs, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func waitForUserStatus(t *testing.T, r *repo.Repository, id int64, want int, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if got := mustQueryInt(t, r, `SELECT status FROM user WHERE id = ?`, id); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("user %d status did not become %d within %s (got %d)", id, want, deadline,
		mustQueryInt(t, r, `SELECT status FROM user WHERE id = ?`, id))
}

func startMaintenanceLoop(t *testing.T, h *Handler) (context.CancelFunc, chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.jobsWG.Add(1) // production pairs this with StartBackgroundJobs' Add
	go func() {
		defer close(done)
		h.runResetAndExpiryLoop(ctx)
	}()
	return cancel, done
}

func stopMaintenanceLoop(t *testing.T, cancel context.CancelFunc, done chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance loop did not exit after cancel")
	}
}

func TestResetAndExpiryIntervalIsOneMinute(t *testing.T) {
	if resetAndExpiryInterval != time.Minute {
		t.Fatalf("expected maintenance interval of one minute, got %s", resetAndExpiryInterval)
	}
}

// The startup pass must catch up an already-expired user without
// waiting for the first tick: with the interval parked at an hour,
// only the startup run can disable the user.
func TestResetAndExpiryLoopRunsOnceAtStartup(t *testing.T) {
	r := openMaintenanceLoopRepo(t, "maint-startup.db")
	h := New(r, "secret")
	seedMaintenanceLoopUser(t, r, 2, time.Now().Add(-time.Minute).UnixMilli())

	old := resetAndExpiryInterval
	resetAndExpiryInterval = time.Hour
	defer func() { resetAndExpiryInterval = old }()

	cancel, done := startMaintenanceLoop(t, h)
	defer stopMaintenanceLoop(t, cancel, done)

	waitForUserStatus(t, r, 2, 0, 3*time.Second)
}

// A user expiring shortly after startup must be disabled by a tick
// soon after the expiry moment, not at the next daily boundary.
func TestResetAndExpiryLoopDisablesShortlyAfterExpiry(t *testing.T) {
	r := openMaintenanceLoopRepo(t, "maint-tick.db")
	h := New(r, "secret")
	seedMaintenanceLoopUser(t, r, 2, time.Now().Add(300*time.Millisecond).UnixMilli())

	old := resetAndExpiryInterval
	resetAndExpiryInterval = 20 * time.Millisecond
	defer func() { resetAndExpiryInterval = old }()

	cancel, done := startMaintenanceLoop(t, h)
	defer stopMaintenanceLoop(t, cancel, done)

	waitForUserStatus(t, r, 2, 0, 5*time.Second)
}

// Missed reset day (panel down on the 15th) is caught up by the first
// run on the 16th; repeated runs within the same period — the minute
// loop runs this job ~1440x a day — must not reset again or move the
// marker; the next period still resets.
func TestResetAndExpiryJobMonthlyCatchUpExactlyOnceUnderMinuteCadence(t *testing.T) {
	r := openMaintenanceLoopRepo(t, "maint-monthly.db")
	h := New(r, "secret")

	created := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	seedR11B6MonthlyResetRows(t, r, 15, created)

	firstRun := time.Date(2026, 3, 16, 0, 0, 5, 0, time.UTC)
	h.runResetAndExpiryJob(firstRun)

	userIn, userOut, utIn, utOut := r11B6Flows(t, r)
	if userIn != 0 || userOut != 0 || utIn != 0 || utOut != 0 {
		t.Fatalf("expected catch-up reset to zero flows, got user %d/%d ut %d/%d", userIn, userOut, utIn, utOut)
	}
	userMarker := mustQueryInt(t, r, `SELECT flow_last_reset_time FROM user WHERE id = 2`)
	if int64(userMarker) != firstRun.UnixMilli() {
		t.Fatalf("expected user reset marker %d, got %d", firstRun.UnixMilli(), userMarker)
	}
	utMarker := mustQueryInt(t, r, `SELECT flow_last_reset_time FROM user_tunnel WHERE id = 10`)
	if int64(utMarker) != firstRun.UnixMilli() {
		t.Fatalf("expected user_tunnel reset marker %d, got %d", firstRun.UnixMilli(), utMarker)
	}

	// Same-period repeats at minute cadence: no second reset, marker
	// untouched.
	if err := r.DB().Exec(`UPDATE user SET in_flow = 500, out_flow = 600 WHERE id = 2`).Error; err != nil {
		t.Fatalf("refill user flow: %v", err)
	}
	h.runResetAndExpiryJob(firstRun.Add(time.Minute))
	h.runResetAndExpiryJob(time.Date(2026, 3, 17, 0, 0, 5, 0, time.UTC))
	userIn, userOut, _ = mustQueryInt64Int64Int(t, r, `SELECT in_flow, out_flow, status FROM user WHERE id = 2`)
	if userIn != 500 || userOut != 600 {
		t.Fatalf("expected same-period flows untouched, got %d/%d", userIn, userOut)
	}
	userMarker = mustQueryInt(t, r, `SELECT flow_last_reset_time FROM user WHERE id = 2`)
	if int64(userMarker) != firstRun.UnixMilli() {
		t.Fatalf("expected marker to stay %d, got %d", firstRun.UnixMilli(), userMarker)
	}

	// Next period: due again, reset lands and the marker advances.
	nextRun := time.Date(2026, 4, 15, 0, 0, 5, 0, time.UTC)
	h.runResetAndExpiryJob(nextRun)
	userIn, userOut, _ = mustQueryInt64Int64Int(t, r, `SELECT in_flow, out_flow, status FROM user WHERE id = 2`)
	if userIn != 0 || userOut != 0 {
		t.Fatalf("expected next-period reset to zero flows, got %d/%d", userIn, userOut)
	}
	userMarker = mustQueryInt(t, r, `SELECT flow_last_reset_time FROM user WHERE id = 2`)
	if int64(userMarker) != nextRun.UnixMilli() {
		t.Fatalf("expected marker to advance to %d, got %d", nextRun.UnixMilli(), userMarker)
	}
}

// Quota reconcile backstop: a user who is over quota but was never
// disabled (the accounting-driven enforcement attempt failed, e.g.
// the node was offline, and no further accounting arrived) is picked
// up by the maintenance round; a second round is a no-op (the
// disabled_at stamp from the first successful enforcement stands).
func TestRunResetAndExpiryJobQuotaReconcileRetriesMissedEnforcement(t *testing.T) {
	r := openMaintenanceLoopRepo(t, "maint-quota-reconcile.db")
	h := New(r, "secret")
	// Real wall-clock time: enforcement re-reads the quota view under
	// its lock with time.Now(), which normalizes usage to the current
	// window — a fabricated "now" would zero the seeded usage there.
	now := time.Now()
	nowMs := now.UnixMilli()
	dayKey := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
	monthKey := int64(now.Year()*100 + int(now.Month()))

	if err := r.DB().Exec(`
		INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status)
		VALUES(2, 'quota-retry-user', 'x', 1, 0, 99999, 0, 0, 0, 1, ?, ?, 1)
	`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// Over the daily limit, window keys current (so the window roll
	// does not release anything), never disabled: the missed
	// enforcement the reconcile exists to retry.
	if err := r.DB().Exec(`
		INSERT INTO user_quota(user_id, daily_limit_gb, monthly_limit_gb, daily_used_bytes, monthly_used_bytes, day_key, month_key, disabled_by_quota, disabled_at, paused_forward_ids, created_time, updated_time)
		VALUES(2, 10, 0, ?, 0, ?, ?, 0, 0, '', ?, ?)
	`, 11*int64(1024*1024*1024), dayKey, monthKey, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user quota: %v", err)
	}

	h.runResetAndExpiryJob(now)

	disabled := mustQueryInt(t, r, `SELECT disabled_by_quota FROM user_quota WHERE user_id = 2`)
	if disabled != 1 {
		t.Fatalf("expected reconcile to disable over-quota user, got disabled_by_quota=%d", disabled)
	}
	disabledAt := mustQueryInt(t, r, `SELECT disabled_at FROM user_quota WHERE user_id = 2`)
	if disabledAt == 0 {
		t.Fatal("expected disabled_at to be stamped by the reconcile enforcement")
	}

	// Second round a minute later: enforcement is incremental, so
	// nothing is re-marked.
	h.runResetAndExpiryJob(now.Add(time.Minute))
	disabledAtAfter := mustQueryInt(t, r, `SELECT disabled_at FROM user_quota WHERE user_id = 2`)
	if disabledAtAfter != disabledAt {
		t.Fatalf("expected repeat reconcile to be a no-op, disabled_at moved %d -> %d", disabledAt, disabledAtAfter)
	}
}
