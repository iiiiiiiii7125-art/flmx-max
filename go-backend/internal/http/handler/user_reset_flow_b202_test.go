package handler

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// B2-02: userResetFlow must surface a failed reset write instead of
// always returning OK. The repo methods used to discard DB errors by
// signature, so a locked/broken DB produced a success envelope while
// the counters stayed untouched. Sabotage triggers make the UPDATE
// fail deterministically after the existence pre-check passes.

func TestB202ResetFlowWriteFailureSurfaces(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()

	userID, err := r.CreateUser("b202-flowuser", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.InsertUserTunnel(userID, 1, nil, 0, 0, 0, 0, 1); err != nil {
		t.Fatalf("insert user tunnel: %v", err)
	}
	var utID int64
	if err := r.DB().Raw(`SELECT id FROM user_tunnel WHERE user_id = ?`, userID).Scan(&utID).Error; err != nil {
		t.Fatalf("fetch user_tunnel id: %v", err)
	}

	// type=1: user UPDATE fails -> non-zero envelope, not OK.
	if err := r.DB().Exec(`CREATE TRIGGER fail_user_update BEFORE UPDATE ON "user" BEGIN SELECT RAISE(ABORT, 'forced update failure'); END`).Error; err != nil {
		t.Fatalf("create user trigger: %v", err)
	}
	code, msg := r2g01CallUserResetFlow(t, h, userID, 1)
	if code == 0 {
		t.Fatalf("type=1 reset with failing write returned code 0 (msg %q), want non-zero", msg)
	}
	if err := r.DB().Exec(`DROP TRIGGER fail_user_update`).Error; err != nil {
		t.Fatalf("drop user trigger: %v", err)
	}

	// type=2: user_tunnel UPDATE fails -> non-zero envelope, not OK.
	if err := r.DB().Exec(`CREATE TRIGGER fail_ut_update BEFORE UPDATE ON user_tunnel BEGIN SELECT RAISE(ABORT, 'forced update failure'); END`).Error; err != nil {
		t.Fatalf("create user_tunnel trigger: %v", err)
	}
	code, msg = r2g01CallUserResetFlow(t, h, utID, 2)
	if code == 0 {
		t.Fatalf("type=2 reset with failing write returned code 0 (msg %q), want non-zero", msg)
	}
}
