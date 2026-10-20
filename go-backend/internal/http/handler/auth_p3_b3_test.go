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
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// B3 同批 P3 回归测试：F-066（user/groups 归属）、F-068（同毫秒签发边界）、
// F-069（passkey register 限流）。F-067 在 internal/auth 包内单测。

func claimsCtxReq(method, path, remoteAddr, body string, userID int64, roleID int) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: strconv.FormatInt(userID, 10), RoleID: roleID})
	return req.WithContext(ctx)
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, w.Body.String())
	}
	return env
}

// ---- F-066 ----

// 旧缺陷：/api/v1/user/groups 无归属校验，任意登录用户可查任意用户的分组 ID。
func TestUserGroupsOwnershipF066(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	mk := func(name string, roleID int) int64 {
		id, err := r.CreateUser(name, "hash", roleID, now+86400000, 1, 1, 100, 1, 0, now)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return id
	}
	adminID := mk("admin", 0)
	userA := mk("usera", 1)
	userB := mk("userb", 1)
	// userA 属于分组 1（测试夹具直插，与 forward 排序测试的夹具方式一致）。
	if err := r.DB().Exec(`INSERT INTO user_group (id, name, created_time, updated_time, status) VALUES (1, 'g1', ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert group: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_group_user (user_group_id, user_id, created_time) VALUES (1, ?, ?)`, userA, now).Error; err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	call := func(actorID int64, roleID int, targetID int64) envelope {
		w := httptest.NewRecorder()
		h.userGroups(w, claimsCtxReq(http.MethodPost, "/api/v1/user/groups", "",
			`{"id":`+strconv.FormatInt(targetID, 10)+`}`, actorID, roleID))
		return decodeEnvelope(t, w)
	}

	// 他人查询必须 403（修复前返回 code=0 与分组数据 → FAIL）。
	if env := call(userB, 1, userA); env.Code != 403 {
		t.Fatalf("other user's groups must be 403, got code=%d data=%s", env.Code, env.Data)
	}
	// 本人查询照常。
	if env := call(userA, 1, userA); env.Code != 0 || !strings.Contains(string(env.Data), "1") {
		t.Fatalf("self query must succeed with group id, got code=%d data=%s", env.Code, env.Data)
	}
	// 管理员查询照常。
	if env := call(adminID, 0, userA); env.Code != 0 || !strings.Contains(string(env.Data), "1") {
		t.Fatalf("admin query must succeed with group id, got code=%d data=%s", env.Code, env.Data)
	}
}

// ---- F-069 ----

// 旧缺陷：passkeyRegisterBegin/Finish 无限流，已登录用户可灌满 1000 槽
// 共享 challenge 池。修复后与登录侧同一 IP 限流（10/min），超限回 429。
// 限流置于 handler 最前（未达 passkey 配置检查即拦截），故无需 WebAuthn 配置。
func TestPasskeyRegisterRateLimitedF069(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	uid, err := r.CreateUser("passkeyflood", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	cases := []struct {
		name string
		path string
		ip   string
		call func(w http.ResponseWriter, req *http.Request)
	}{
		{"begin", "/api/v1/user/passkey/register/begin", "198.51.100.231:4000", h.passkeyRegisterBegin},
		{"finish", "/api/v1/user/passkey/register/finish", "198.51.100.232:4000", h.passkeyRegisterFinish},
	}
	for _, tc := range cases {
		var last envelope
		for i := 0; i < 12; i++ {
			w := httptest.NewRecorder()
			req := claimsCtxReq(http.MethodPost, tc.path, tc.ip, `{"password":"x","sessionId":"s","credential":{},"name":"n"}`, uid, 1)
			tc.call(w, req)
			last = decodeEnvelope(t, w)
		}
		if last.Code != 429 {
			t.Fatalf("register/%s: 12th request from same IP must be 429, got code=%d msg=%q （修复前恒非 429)", tc.name, last.Code, last.Msg)
		}
	}
}

// ---- F-068 ----

// 旧缺陷：改密/吊销把 PasswordChangedAt 置为 T 后，同一时刻重新登录签发的
// token 因 IatMs<=PasswordChangedAt 被中间件判死（出生即失效）。
// 定性（FINAL/A1-005）：中间件 <= 口径是 M-05 有意行为，不得放宽；
// 修复在签发侧——签发时刻单调推到 PasswordChangedAt+1。
// 本用例把 PasswordChangedAt 置于近未来（模拟同毫秒/时钟贴合的最坏情形），
// 经真实登录路径签发，断言新 token 的 IatMs 严格大于 PasswordChangedAt
// 且能通过中间件状态校验；同时断言恰在 T 签发的 token 仍被拒（M-05 不放宽）。
func TestLoginTokenIssuedAfterPasswordChangedAtF068(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	hash, err := security.HashPassword("f068-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	uid, err := r.CreateUser("f068user", hash, 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	pca := time.Now().UnixMilli() + 2000
	if err := r.UpdateUserPassword(uid, hash, pca); err != nil {
		t.Fatalf("set password_changed_at: %v", err)
	}

	// 真实登录路径签发。
	w := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/login", strings.NewReader(`{"username":"f068user","password":"f068-password"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "198.51.100.233:4000"
	h.login(w, loginReq)
	loginResp := decodeEnvelope(t, w)
	if loginResp.Code != 0 {
		t.Fatalf("login must succeed, got code=%d msg=%q", loginResp.Code, loginResp.Msg)
	}
	var loginData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(loginResp.Data, &loginData); err != nil || loginData.Token == "" {
		t.Fatalf("login token missing: err=%v data=%s", err, loginResp.Data)
	}
	claims, err := auth.ParseClaims(loginData.Token, "test-secret")
	if err != nil {
		t.Fatalf("parse login token: %v", err)
	}
	if claims.IatMs <= pca {
		t.Fatalf("fresh login token born dead: IatMs=%d <= PasswordChangedAt=%d （修复前 FAIL)", claims.IatMs, pca)
	}

	// 中间件状态校验：新 token 放行、恰在 T 签发的旧式 token 仍被拒。
	state := func(userID int64) (*auth.UserAuthState, error) {
		return &auth.UserAuthState{ID: userID, RoleID: 1, Status: 1, PasswordChangedAt: pca}, nil
	}
	mw := middleware.JWT(middleware.AuthOptions{JWTSecret: "test-secret", GetUserAuthState: state})(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("reached")) }))
	// 注意：面板信封恒回 HTTP 200，拒绝体现在 body 的 code 字段。
	probe := func(token string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/package", nil)
		req.Header.Set("Authorization", token)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if body := probe(loginData.Token); body != "reached" {
		t.Fatalf("middleware must accept fresh login token, got body %s", body)
	}
	atT, err := auth.GenerateTokenAt(uid, "f068user", 1, "test-secret", time.UnixMilli(pca))
	if err != nil {
		t.Fatalf("generate at-T token: %v", err)
	}
	if body := probe(atT); !strings.Contains(body, `"code":401`) {
		t.Fatalf("token issued exactly at PasswordChangedAt must stay rejected (M-05), got body %s", body)
	}
}
