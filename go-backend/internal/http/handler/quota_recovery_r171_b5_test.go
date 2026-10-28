package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R171-B5 I-01: a quota-recovery notice must not be lost when the channel
// fails at unblock time. Written red-first: on the baseline the notice
// is a bare h.notify with no pending mark, so the failure leaves nothing
// for the next check round to resend.
//
// Flow under test (mirrors probe-i.log TestR10I_QuotaRecoveryNotify-
// FailureLeavesNoRearm): exceed notice delivered while the channel is
// healthy -> channel fails -> window roll unblocks the user -> recovery
// notice is pending, not lost -> channel recovers -> next
// checkUserReminders round resends it, exactly once, paired with the
// exceed notice.

type r171b5CountingWebhook struct {
	srv        *httptest.Server
	fail       atomic.Bool
	attempts   atomic.Int32
	mu         sync.Mutex
	deliveries []notifyMessage
}

func newR171B5CountingWebhook(t *testing.T) *r171b5CountingWebhook {
	t.Helper()
	w := &r171b5CountingWebhook{}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		w.attempts.Add(1)
		if w.fail.Load() {
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}
		var m notifyMessage
		_ = json.NewDecoder(req.Body).Decode(&m)
		w.mu.Lock()
		w.deliveries = append(w.deliveries, m)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *r171b5CountingWebhook) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.deliveries)
}

func (w *r171b5CountingWebhook) titles() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.deliveries))
	for i, m := range w.deliveries {
		out[i] = m.Title
	}
	return out
}

func (w *r171b5CountingWebhook) waitDeliveries(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.count() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("webhook deliveries = %d (%v), want >= %d", w.count(), w.titles(), want)
}

func (w *r171b5CountingWebhook) waitAttempts(t *testing.T, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if w.attempts.Load() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("webhook attempts = %d, want >= %d", w.attempts.Load(), want)
}

func r171b5SetupQuotaRecoveryHandler(t *testing.T, wh *r171b5CountingWebhook) (*Handler, *repo.Repository) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	for k, v := range map[string]string{"notify_enabled": "true", "notify_webhook_url": wh.srv.URL} {
		if err := r.DB().Exec(`INSERT INTO vite_config(name, value, time) VALUES(?, ?, ?)`, k, v, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	return New(r, "unit-test-secret"), r
}

// r171b5SeedBlockedQuotaUser creates a non-admin user whose daily quota
// (1GB) is exceeded (3GiB used) and already disabled by quota.
func r171b5SeedBlockedQuotaUser(t *testing.T, r *repo.Repository, now time.Time) int64 {
	t.Helper()
	nowMs := now.UnixMilli()
	userID, err := r.CreateUser("r171b5-recovery", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 1, monthly_limit_gb = 0,
		daily_used_bytes = ?, monthly_used_bytes = 0, disabled_by_quota = 1, disabled_at = ?, paused_forward_ids = ''
		WHERE user_id = ?`, int64(3)<<30, nowMs, userID).Error; err != nil {
		t.Fatalf("seed blocked quota: %v", err)
	}
	return userID
}

func r171b5PendingRecoveryUserIDs(t *testing.T, h *Handler) []int64 {
	t.Helper()
	pending := h.loadPendingQuotaRecoveries()
	ids := make([]int64, 0, len(pending))
	for _, p := range pending {
		ids = append(ids, p.UserID)
	}
	return ids
}

func TestR171B5QuotaRecoveryResendAfterChannelFailure(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r171b5SetupQuotaRecoveryHandler(t, wh)
	now := time.Now()
	userID := r171b5SeedBlockedQuotaUser(t, r, now)

	// 1. Channel healthy: the exceed notice is delivered (pairing half 1).
	h.checkUserReminders(now)
	wh.waitDeliveries(t, 1, 5*time.Second)
	if titles := wh.titles(); titles[0] != "用户日配额已用完" {
		t.Fatalf("titles = %v, want first 用户日配额已用完", titles)
	}

	// 2. Channel fails; the quota window rolls and unblocks the user.
	wh.fail.Store(true)
	y := now.AddDate(0, 0, -1)
	yesterdayDayKey := int64(y.Year()*10000 + int(y.Month())*100 + y.Day())
	if err := r.DB().Exec(`UPDATE user_quota SET day_key = ? WHERE user_id = ?`, yesterdayDayKey, userID).Error; err != nil {
		t.Fatalf("backdate day_key: %v", err)
	}
	h.resetUserQuotaWindows(time.Now())
	view, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil || view.DisabledByQuota != 0 {
		t.Fatalf("user must be unblocked after window roll, view = %+v err = %v", view, err)
	}
	// The failed send (initial + one retry 3s later) must leave the
	// recovery pending instead of dropping it.
	wh.waitAttempts(t, 3, 12*time.Second) // 1 exceed + 2 recovery attempts
	if got := wh.count(); got != 1 {
		t.Fatalf("failing channel must not record deliveries, got %d (%v)", got, wh.titles())
	}
	ids := r171b5PendingRecoveryUserIDs(t, h)
	if len(ids) != 1 || ids[0] != userID {
		t.Fatalf("pending recovery user IDs = %v, want [%d]", ids, userID)
	}

	// 3. Channel recovers: the next check round resends the recovery
	// (pairing half 2), exactly once, and clears the pending mark.
	wh.fail.Store(false)
	h.checkUserReminders(time.Now())
	wh.waitDeliveries(t, 2, 5*time.Second)
	titles := wh.titles()
	if titles[0] != "用户日配额已用完" || titles[1] != "用户配额已恢复" {
		t.Fatalf("titles = %v, want [用户日配额已用完 用户配额已恢复]", titles)
	}
	if ids := r171b5PendingRecoveryUserIDs(t, h); len(ids) != 0 {
		t.Fatalf("pending recovery not cleared after resend: %v", ids)
	}

	// 4. A further check round must not duplicate the recovery.
	h.checkUserReminders(time.Now().Add(time.Minute))
	time.Sleep(300 * time.Millisecond)
	if got := wh.count(); got != 2 {
		t.Fatalf("recovery duplicated: deliveries = %d (%v), want 2", got, wh.titles())
	}
}

// Manual reset is the second unblock path: a failing channel at manual
// reset must also leave the recovery pending for the next check round.
func TestR171B5QuotaRecoveryManualResetPending(t *testing.T) {
	wh := newR171B5CountingWebhook(t)
	h, r := r171b5SetupQuotaRecoveryHandler(t, wh)
	now := time.Now()
	userID := r171b5SeedBlockedQuotaUser(t, r, now)

	wh.fail.Store(true)
	code, msg := r171b5CallUserQuotaReset(t, h, userID, "all")
	if code != 0 {
		t.Fatalf("manual reset returned code %d (msg %q), want 0", code, msg)
	}
	wh.waitAttempts(t, 2, 12*time.Second)
	if got := wh.count(); got != 0 {
		t.Fatalf("failing channel must not record deliveries, got %d", got)
	}
	ids := r171b5PendingRecoveryUserIDs(t, h)
	if len(ids) != 1 || ids[0] != userID {
		t.Fatalf("pending recovery user IDs = %v, want [%d]", ids, userID)
	}

	wh.fail.Store(false)
	h.checkUserReminders(time.Now())
	wh.waitDeliveries(t, 1, 5*time.Second)
	if titles := wh.titles(); titles[0] != "用户配额已恢复" {
		t.Fatalf("titles = %v, want [用户配额已恢复]", titles)
	}
	if ids := r171b5PendingRecoveryUserIDs(t, h); len(ids) != 0 {
		t.Fatalf("pending recovery not cleared after resend: %v", ids)
	}
}
