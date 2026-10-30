package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// R2 B02-001: updatePassword 的改名面此前既不 TrimSpace 也无限长，与
// create/update（asString 隐式 trim + O5-002 的 1-100 rune 校验）口径
// 分裂：带尾空格名、101 rune 名可原样落库，同一逻辑名随写入面分裂。
// 修复后：改名落库为 trim 形态；超长名点名拒绝且不落库。

func TestR2K1UpdatePasswordNormalizesAndBoundsUsername(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	hash, err := security.HashPassword("old-pass-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	userID, err := r.CreateUser("orig-name", hash, 1, now+86400000, 100, 1, 10, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	call := func(newUsername, newPwd string) envelope {
		t.Helper()
		raw, err := json.Marshal(map[string]string{
			"newUsername":     newUsername,
			"currentPassword": "old-pass-1",
			"newPassword":     newPwd,
			"confirmPassword": newPwd,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w := httptest.NewRecorder()
		h.updatePassword(w, claimsCtxReq(http.MethodPost, "/api/v1/user/updatePassword", "", string(raw), userID, 1))
		return decodeEnvelope(t, w)
	}

	storedName := func() string {
		t.Helper()
		u, err := r.GetUserByID(userID)
		if err != nil || u == nil {
			t.Fatalf("get user: %v / %v", u, err)
		}
		return u.User
	}

	// 101 rune：拒绝且不落库（密码也不得被改动，用原密码继续后续调用验证）。
	longName := strings.Repeat("a", 101)
	if env := call(longName, "new-pass-1"); env.Code == 0 {
		t.Fatal("updatePassword with 101-rune username accepted, want rejection")
	}
	if got := storedName(); got != "orig-name" {
		t.Fatalf("username persisted as %q after rejected rename, want orig-name", got)
	}

	// 带尾空格：落库为 trim 形态（注意此时密码已被上一步……未改动，
	// currentPassword 仍为 old-pass-1）。
	if env := call("renamed ", "new-pass-1"); env.Code != 0 {
		t.Fatalf("updatePassword rename with trailing space rejected: code=%d msg=%s", env.Code, env.Msg)
	}
	if got := storedName(); got != "renamed" {
		t.Fatalf("username persisted as %q, want trimmed %q", got, "renamed")
	}
	if u, err := r.GetUserByUsername("renamed"); err != nil || u == nil {
		t.Fatalf("trimmed username not login-resolvable: %v / %v", u, err)
	}
}
