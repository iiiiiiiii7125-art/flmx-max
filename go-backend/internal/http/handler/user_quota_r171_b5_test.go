package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R171-B5 G-QUOTA-01: manual quota reset must validate the user exists
// (same R2X-012 discipline as userSubscriptionSet). Written red-first:
// on the baseline a phantom userId returns code 0 and leaves an orphan
// user_quota row.

func r171b5CallUserQuotaReset(t *testing.T, h *Handler, userID int64, scope string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"userId":%d,"scope":%q}`, userID, scope)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/quota/reset", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.userQuotaReset(rec, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset response: %v (body %q)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

func TestR171B5QuotaResetPhantomUserRejected(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	nowMs := now.UnixMilli()

	// Phantom user: must fail and must not create an orphan row.
	code, msg := r171b5CallUserQuotaReset(t, h, 999999, "all")
	if code == 0 {
		t.Fatalf("phantom reset returned code 0 (msg %q), want non-zero error", msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_quota WHERE user_id = ?`, 999999); n != 0 {
		t.Fatalf("phantom reset created %d orphan user_quota rows, want 0", n)
	}

	// Real user: reset still succeeds and zeroes usage.
	userID, err := r.CreateUser("r171b5-quotauser", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_used_bytes = ?, monthly_used_bytes = ? WHERE user_id = ?`,
		int64(3)<<30, int64(5)<<30, userID).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
	code, msg = r171b5CallUserQuotaReset(t, h, userID, "all")
	if code != 0 {
		t.Fatalf("real reset returned code %d (msg %q), want 0", code, msg)
	}
	view, err := r.GetUserQuotaView(userID, now)
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	if view.DailyUsedBytes != 0 || view.MonthlyUsedBytes != 0 {
		t.Fatalf("real reset left usage daily=%d monthly=%d, want 0/0", view.DailyUsedBytes, view.MonthlyUsedBytes)
	}
}
