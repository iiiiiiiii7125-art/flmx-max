package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// L8-F-01（Round 5）回归：openAPISubStore 补种子默认口令门。
//
// 基线缺陷：裸口令鉴权面 openAPISubStore 未消费 isStoredDefaultPassword
// ——login 命中种子默认口令只签发受限 token、passkey 登录同口径，
// 本面却在口令验过后直接给出完整能力，是「账号仍用默认口令 ⇒ 不得
// 获得完整能力」机制族的第三面遗漏。修复后：管理员存储口令仍为种子
// 默认值（两个候选字面量任一）时命中即拒；改密后恢复；非管理员账号
// 即使口令字面量与种子候选相同也不受此门影响（判定本身仅管理员，
// 与 login 口径一致，防过修）。

type subStoreGateEnv struct {
	h    *Handler
	repo *repo.Repository
}

func newSubStoreGateEnv(t *testing.T) *subStoreGateEnv {
	t.Helper()
	resetSubStoreLimiterForTest()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &subStoreGateEnv{h: New(r, "test-secret"), repo: r}
}

func (e *subStoreGateEnv) addUser(t *testing.T, name, password string, roleID int) {
	t.Helper()
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	now := time.Now().UnixMilli()
	if _, err := e.repo.CreateUser(name, hash, roleID, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

// call 以 POST body 正式用法调 sub_store（tunnel=-1 本人用量面）。
// 成功路径回纯文本用量头（非 JSON 信封），故只回 header 与 body 原文，
// 由各用例按「header 是否下发 + body 文案」断言。
func (e *subStoreGateEnv) call(t *testing.T, username, password string, ipOctet int) (string, string) {
	t.Helper()
	body := `{"user":` + strconv.Quote(username) + `,"password":` + strconv.Quote(password) + `,"tunnel":"-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/open_api/sub_store", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("198.51.100.%d:4000", ipOctet)
	w := httptest.NewRecorder()
	e.h.openAPISubStore(w, req)
	return w.Header().Get("subscription-userinfo"), w.Body.String()
}

// 管理员仍用种子默认口令（两个候选）→ 拒，且不得下发 subscription-userinfo。
func TestSubStoreSeedDefaultPasswordAdminRejected(t *testing.T) {
	env := newSubStoreGateEnv(t)
	env.addUser(t, "r5gate-admin-at", "admin@user", 0)
	env.addUser(t, "r5gate-admin-under", "admin_user", 0)
	for i, tc := range []struct{ user, pass string }{
		{"r5gate-admin-at", "admin@user"},
		{"r5gate-admin-under", "admin_user"},
	} {
		header, body := env.call(t, tc.user, tc.pass, 20+i)
		if header != "" {
			t.Fatalf("default-password admin %s must be rejected, header=%q body=%s", tc.user, header, body)
		}
		if !strings.Contains(body, "默认密码") {
			t.Fatalf("rejection for %s must name the default-password cause, body=%s", tc.user, body)
		}
	}
}

// 管理员口令错 → 仍是通用「鉴权失败」，不得误报默认密码门（门在验密之后）。
func TestSubStoreWrongPasswordStaysGeneric(t *testing.T) {
	env := newSubStoreGateEnv(t)
	env.addUser(t, "r5gate-admin-wrong", "admin@user", 0)
	header, body := env.call(t, "r5gate-admin-wrong", "not-the-password", 30)
	if header != "" {
		t.Fatalf("wrong password must fail, header=%q", header)
	}
	if strings.Contains(body, "默认密码") {
		t.Fatalf("wrong password must not trip the default-password gate message, body=%s", body)
	}
}

// 管理员已改密（非默认口令）→ 正常下发本人用量头。
func TestSubStoreChangedPasswordAdminAllowed(t *testing.T) {
	env := newSubStoreGateEnv(t)
	env.addUser(t, "r5gate-admin-changed", "r5-changed-pass", 0)
	header, body := env.call(t, "r5gate-admin-changed", "r5-changed-pass", 40)
	if header == "" {
		t.Fatalf("changed-password admin must succeed, header=%q body=%s", header, body)
	}
}

// 普通用户口令字面量恰与种子候选相同 → 不受此门影响（判定仅管理员）。
func TestSubStoreNonAdminWithSeedLiteralPasswordAllowed(t *testing.T) {
	env := newSubStoreGateEnv(t)
	env.addUser(t, "r5gate-plain", "admin@user", 1)
	header, body := env.call(t, "r5gate-plain", "admin@user", 50)
	if header == "" {
		t.Fatalf("non-admin must not be gated, header=%q body=%s", header, body)
	}
}
