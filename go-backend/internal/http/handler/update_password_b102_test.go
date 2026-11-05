package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// B1-02: updatePassword 对超过 72 字节的新密码必须具名拒绝
// （bcrypt 截断面），而不是静默截断落库；恰在上限内的长密码
// 正常改密。
func TestB102UpdatePasswordRejectsOver72ByteNewPassword(t *testing.T) {
	resetLoginLimitersForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	hash, err := security.HashPassword("old-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := r.CreateUser("b102-updatepw", hash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const ip = "198.51.100.77:4000"

	longNew := strings.Repeat("n", 73)
	body := `{"newUsername":"b102-updatepw","currentPassword":"old-pass-1","newPassword":"` + longNew + `","confirmPassword":"` + longNew + `"}`
	w := httptest.NewRecorder()
	h.updatePassword(w, claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", ip, body, uid, 1))
	env := decodeEnvelope(t, w)
	if env.Code == 0 {
		t.Fatal("73-byte new password must be rejected, got success")
	}
	if !strings.Contains(env.Msg, "72") {
		t.Fatalf("rejection must name the 72-byte limit, got msg %q", env.Msg)
	}
	// 存储哈希不得被改动（仍是旧密码）。
	u, err := r.GetUserByID(uid)
	if err != nil || u == nil {
		t.Fatalf("get user: %v", err)
	}
	if ok, _ := security.VerifyPassword(u.Pwd, "old-pass-1"); !ok {
		t.Fatal("stored password must be unchanged after rejected over-long change")
	}
}
