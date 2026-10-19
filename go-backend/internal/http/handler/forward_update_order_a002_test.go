package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/repo"
)

// R5-B2 A-002 回归测试：forward/update-order 归属检查。
//
// 旧缺陷：该端点无任何鉴权（连 userRoleFromRequest 都没调），
// 用户 A 可改用户 B 的转发排序；其余 15 个 forward 端点均有检查。

func newForwardOrderTestEnv(t *testing.T) (*Handler, int64, int64) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	userA, err := r.CreateUser("userA", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create userA: %v", err)
	}
	userB, err := r.CreateUser("userB", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create userB: %v", err)
	}
	// 最小 forward 行（归属检查只读 id/user_id）
	for _, u := range []int64{userA, userB} {
		if err := r.DB().Exec(
			`INSERT INTO forward (user_id, user_name, name, tunnel_id, remote_addr, status, inx, created_time, updated_time) VALUES (?, ?, ?, 0, '', 1, 0, ?, ?)`,
			u, "u", "f", now, now,
		).Error; err != nil {
			t.Fatalf("insert forward: %v", err)
		}
	}
	var fwdA, fwdB int64
	if err := r.DB().Raw(`SELECT id FROM forward WHERE user_id = ?`, userA).Scan(&fwdA).Error; err != nil {
		t.Fatalf("select fwdA: %v", err)
	}
	if err := r.DB().Raw(`SELECT id FROM forward WHERE user_id = ?`, userB).Scan(&fwdB).Error; err != nil {
		t.Fatalf("select fwdB: %v", err)
	}
	return h, fwdA, fwdB
}

func orderReqWithClaims(sub string, roleID int, fwdID int64, inx int) *http.Request {
	body := `{"forwards":[{"id":` + strconv.FormatInt(fwdID, 10) + `,"inx":` + strconv.Itoa(inx) + `}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/forward/update-order", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if sub != "" {
		ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
			auth.Claims{Sub: sub, RoleID: roleID})
		req = req.WithContext(ctx)
	}
	return req
}

func decodeOrderResp(t *testing.T, w *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Code, env.Msg
}

// 用户 A 不能改用户 B 的顺序；管理员可以；未登录 401；GET 拒绝。
func TestA002_ForwardUpdateOrderOwnership(t *testing.T) {
	h, fwdA, fwdB := newForwardOrderTestEnv(t)

	// 1. 未登录 → 401
	w := httptest.NewRecorder()
	h.forwardUpdateOrder(w, orderReqWithClaims("", 0, fwdA, 5))
	if code, _ := decodeOrderResp(t, w); code != 401 {
		t.Errorf("unauthenticated: code=%d, want 401", code)
	}

	// 2. 用户 A 改用户 B 的转发 → 转发不存在（不泄露存在性）
	w = httptest.NewRecorder()
	h.forwardUpdateOrder(w, orderReqWithClaims("2", 1, fwdB, 5))
	if code, msg := decodeOrderResp(t, w); msg != "转发不存在" {
		t.Errorf("userA x userB forward: code=%d msg=%q, want 转发不存在", code, msg)
	}

	// 3. 用户 A 改自己的 → 成功
	w = httptest.NewRecorder()
	h.forwardUpdateOrder(w, orderReqWithClaims("2", 1, fwdA, 7))
	if code, _ := decodeOrderResp(t, w); code != 0 {
		t.Errorf("userA own forward: code=%d, want 0", code)
	}

	// 4. 管理员改用户 B 的 → 成功
	w = httptest.NewRecorder()
	h.forwardUpdateOrder(w, orderReqWithClaims("1", 0, fwdB, 9))
	if code, _ := decodeOrderResp(t, w); code != 0 {
		t.Errorf("admin x userB forward: code=%d, want 0", code)
	}

	// 5. GET → 拒绝
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/forward/update-order", nil)
	h.forwardUpdateOrder(w, req)
	if code, _ := decodeOrderResp(t, w); code == 0 {
		t.Error("GET update-order should be rejected")
	}
}
