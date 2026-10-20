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
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F-001 回归测试：默认订阅 token 只给被分配的用户（用户 2026-10-20 裁决）。
//
// 旧缺陷：inboundSubInfo 读分支对任意登录用户无条件返回默认订阅 token，
// 未分配用户拿到 token 后可不带 ?user= 拉全量节点，绕过 1.0.12 订阅分配。
// 放行条件（X1 §7 回归点）：管理员 ∨ 被分配该默认订阅（user_subscription）
// ∨ 有旧入站分配（user_inbound，存量 ?user=+sig 回退链拼链接依赖此 path）。

type subInfoTestEnv struct {
	h          *Handler
	repo       *repo.Repository
	adminID    int64
	assignedID int64
	legacyID   int64
	plainID    int64
	sub        *model.Subscription
}

func newSubInfoTestEnv(t *testing.T) *subInfoTestEnv {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()

	mk := func(name string, roleID int) int64 {
		id, err := r.CreateUser(name, "hash", roleID, now+86400000, 1, 1, 100, 1, 0, now)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return id
	}
	env := &subInfoTestEnv{
		h:          New(r, "test-secret"),
		repo:       r,
		adminID:    mk("admin", 0),
		assignedID: mk("assigned", 1),
		legacyID:   mk("legacy", 1),
		plainID:    mk("plain", 1),
	}
	sub, err := r.DefaultSubscription(now)
	if err != nil {
		t.Fatalf("default subscription: %v", err)
	}
	if sub.Token == "" {
		t.Fatalf("default subscription token is empty")
	}
	env.sub = sub
	if err := r.SetUserSubscriptions(env.assignedID, []int64{sub.ID}); err != nil {
		t.Fatalf("assign subscription: %v", err)
	}
	// 旧模型分配：只有 user_inbound 行（inbound 不必真实存在，本检查只读分配行）。
	if err := r.SetUserInbounds(env.legacyID, []int64{999}); err != nil {
		t.Fatalf("assign legacy inbound: %v", err)
	}
	return env
}

func subInfoReq(userID int64, roleID int, reset bool) *http.Request {
	body := `{}`
	if reset {
		body = `{"reset":true}`
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/sub/info", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: strconv.FormatInt(userID, 10), RoleID: roleID})
	return req.WithContext(ctx)
}

type subInfoResp struct {
	Code int `json:"code"`
	Data struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	} `json:"data"`
	Raw string `json:"-"`
}

func callSubInfo(t *testing.T, h *Handler, userID int64, roleID int, reset bool) subInfoResp {
	t.Helper()
	w := httptest.NewRecorder()
	h.inboundSubInfo(w, subInfoReq(userID, roleID, reset))
	var resp subInfoResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode sub/info resp: %v (body=%s)", err, w.Body.String())
	}
	resp.Raw = w.Body.String()
	return resp
}

// 未分配普通用户：响应不得包含默认订阅 token（修复前 FAIL：token 被无条件返回）。
func TestSubInfoUnassignedUserGetsNoDefaultToken(t *testing.T) {
	env := newSubInfoTestEnv(t)
	resp := callSubInfo(t, env.h, env.plainID, 1, false)
	if strings.Contains(resp.Raw, env.sub.Token) {
		t.Fatalf("unassigned user received default subscription token: %s", resp.Raw)
	}
	if resp.Code == 0 {
		t.Fatalf("unassigned user read must not succeed, got code=0 body=%s", resp.Raw)
	}
}

// 被分配默认订阅的用户：照常拿到 token 与 path（回归点 ①）。
func TestSubInfoAssignedUserGetsDefaultToken(t *testing.T) {
	env := newSubInfoTestEnv(t)
	resp := callSubInfo(t, env.h, env.assignedID, 1, false)
	if resp.Code != 0 || resp.Data.Token != env.sub.Token {
		t.Fatalf("assigned user must receive token, got %+v", resp)
	}
	if !strings.HasSuffix(resp.Data.Path, env.sub.Token) {
		t.Fatalf("assigned user path must carry token, got %q", resp.Data.Path)
	}
}

// 有旧入站分配的用户：照常拿到 path 拼 ?user=+sig 回退链（回归点 ①/②）。
func TestSubInfoLegacyInboundUserGetsDefaultPath(t *testing.T) {
	env := newSubInfoTestEnv(t)
	resp := callSubInfo(t, env.h, env.legacyID, 1, false)
	if resp.Code != 0 || resp.Data.Token != env.sub.Token {
		t.Fatalf("legacy-assigned user must receive token, got %+v", resp)
	}
}

// 管理员：照常读取（回归点 ③ 的读侧）。
func TestSubInfoAdminGetsDefaultToken(t *testing.T) {
	env := newSubInfoTestEnv(t)
	resp := callSubInfo(t, env.h, env.adminID, 0, false)
	if resp.Code != 0 || resp.Data.Token != env.sub.Token {
		t.Fatalf("admin must receive token, got %+v", resp)
	}
}

// reset 仍仅管理员：普通用户（含已分配者）reset 必须 403 且 token 不变；
// 管理员 reset 后 token 轮换（回归点 ③）。
func TestSubInfoResetStaysAdminOnlyAndRotates(t *testing.T) {
	env := newSubInfoTestEnv(t)
	for _, uid := range []int64{env.plainID, env.assignedID, env.legacyID} {
		resp := callSubInfo(t, env.h, uid, 1, true)
		if resp.Code != 403 {
			t.Fatalf("non-admin reset must be 403, user %d got %+v", uid, resp)
		}
	}
	cur, err := env.repo.DefaultSubscription(time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("reload default subscription: %v", err)
	}
	if cur.Token != env.sub.Token {
		t.Fatalf("token changed after forbidden reset: %q -> %q", env.sub.Token, cur.Token)
	}
	resp := callSubInfo(t, env.h, env.adminID, 0, true)
	if resp.Code != 0 || resp.Data.Token == "" || resp.Data.Token == env.sub.Token {
		t.Fatalf("admin reset must rotate token, got %+v", resp)
	}
}
