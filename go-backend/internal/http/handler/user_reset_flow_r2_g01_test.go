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

// R2-G-01: userResetFlow must reject unknown ids instead of returning
// success (same R2X-012 discipline as userQuotaReset / G-QUOTA-01).
// Written red-first: on the baseline both phantom type=1 and type=2
// return code 0.

func r2g01CallUserResetFlow(t *testing.T, h *Handler, id int64, typeVal int) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"id":%d,"type":%d}`, id, typeVal)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/reset-flow", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.userResetFlow(rec, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode reset-flow response: %v (body %q)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

func TestR2G01ResetFlowPhantomRejected(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()

	// Phantom user (type=1): must fail, no write.
	code, msg := r2g01CallUserResetFlow(t, h, 999999, 1)
	if code == 0 {
		t.Fatalf("phantom type=1 reset returned code 0 (msg %q), want non-zero error", msg)
	}

	// Phantom user_tunnel row (type=2): must fail, no write.
	code, msg = r2g01CallUserResetFlow(t, h, 999999, 2)
	if code == 0 {
		t.Fatalf("phantom type=2 reset returned code 0 (msg %q), want non-zero error", msg)
	}

	// Real user: reset still succeeds and zeroes user + tunnel flows.
	userID, err := r.CreateUser("r2g01-flowuser", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`UPDATE "user" SET in_flow = ?, out_flow = ? WHERE id = ?`,
		int64(12345), int64(67890), userID).Error; err != nil {
		t.Fatalf("seed user flow: %v", err)
	}
	if err := r.InsertUserTunnel(userID, 1, nil, 0, 0, 0, 0, 1); err != nil {
		t.Fatalf("insert user tunnel: %v", err)
	}
	var utID int64
	if err := r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = ?`, userID).Scan(&utID).Error; err != nil {
		t.Fatalf("fetch user_tunnel id: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_tunnel SET in_flow = ?, out_flow = ? WHERE id = ?`,
		int64(111), int64(222), utID).Error; err != nil {
		t.Fatalf("seed tunnel flow: %v", err)
	}

	// type=2 on the real tunnel row zeroes only that row's flows.
	code, msg = r2g01CallUserResetFlow(t, h, utID, 2)
	if code != 0 {
		t.Fatalf("real type=2 reset returned code %d (msg %q), want 0", code, msg)
	}
	var flowRow struct {
		InFlow  int64
		OutFlow int64
	}
	if err := r.DB().Raw(`SELECT in_flow, out_flow FROM user_tunnel WHERE id = ?`, utID).Scan(&flowRow).Error; err != nil {
		t.Fatalf("read tunnel flow: %v", err)
	}
	if flowRow.InFlow != 0 || flowRow.OutFlow != 0 {
		t.Fatalf("real type=2 reset left tunnel flow in=%d out=%d, want 0/0", flowRow.InFlow, flowRow.OutFlow)
	}

	// type=1 on the real user zeroes the user row (and its tunnel rows).
	code, msg = r2g01CallUserResetFlow(t, h, userID, 1)
	if code != 0 {
		t.Fatalf("real type=1 reset returned code %d (msg %q), want 0", code, msg)
	}
	if err := r.DB().Raw(`SELECT in_flow, out_flow FROM "user" WHERE id = ?`, userID).Scan(&flowRow).Error; err != nil {
		t.Fatalf("read user flow: %v", err)
	}
	if flowRow.InFlow != 0 || flowRow.OutFlow != 0 {
		t.Fatalf("real type=1 reset left user flow in=%d out=%d, want 0/0", flowRow.InFlow, flowRow.OutFlow)
	}
}
