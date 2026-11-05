package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/handler"
	"go-backend/internal/store/repo"
)

// E1-02 全链：服务端登出后，此前签发的 token 在 HTTP 面立即失效
// （复制走的 token 同样死亡）；重新登录（水位之后签发）可用；
// 其他用户的 token 不受影响。修复前 /api/v1/user/logout 不存在
// （404），且旧 token 在登出后继续有效。
func TestLogoutEndpointRevokesIssuedTokens(t *testing.T) {
	const secret = "router-logout-secret"
	r, err := repo.Open(filepath.Join(t.TempDir(), "router-logout.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer func() { _ = r.Close() }()

	h := handler.New(r, secret)
	router := NewRouter(h, secret)
	ts := httptest.NewServer(router)
	defer ts.Close()

	call := func(token, path, body string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var env struct {
			Code int `json:"code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
		return env.Code
	}

	// 播种管理员 id=1（repo.Open 的 seedData），token 签发于当前时刻。
	adminToken, err := auth.GenerateToken(1, "admin", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	if code := call(adminToken, "/api/v1/user/list", `{"current":1,"size":10}`); code != 0 {
		t.Fatalf("pre-logout list code = %d, want 0", code)
	}
	if code := call(adminToken, "/api/v1/user/logout", `{}`); code != 0 {
		t.Fatalf("logout code = %d, want 0", code)
	}
	// 同一 token 自此失效：业务面与登出面都被中间件以 401 拒绝。
	if code := call(adminToken, "/api/v1/user/list", `{"current":1,"size":10}`); code != 401 {
		t.Fatalf("post-logout list code = %d, want 401", code)
	}
	if code := call(adminToken, "/api/v1/user/logout", `{}`); code != 401 {
		t.Fatalf("post-logout repeat logout code = %d, want 401", code)
	}

	// 重新登录：水位之后签发的新 token 立即可用（签发侧
	// tokenIssueTime 保证 IatMs 严格大于水位，此处以水位+1 模拟）。
	state, err := r.GetUserAuthState(1)
	if err != nil {
		t.Fatalf("admin state: %v", err)
	}
	reloginToken, err := auth.GenerateTokenAt(1, "admin", 0, secret, time.UnixMilli(state.RevocationWatermark()+1))
	if err != nil {
		t.Fatalf("generate relogin token: %v", err)
	}
	if code := call(reloginToken, "/api/v1/user/list", `{"current":1,"size":10}`); code != 0 {
		t.Fatalf("relogin list code = %d, want 0", code)
	}

	// 其他用户不受牵连：其 token 在管理员登出后仍有效（以其自身
	// 登出面的 200 作为鉴权通过探针）。
	otherID, err := r.CreateUser("logout_bystander", "hash", 1, 0, 0, 0, 10, 1, 0, 1000)
	if err != nil {
		t.Fatalf("create bystander: %v", err)
	}
	otherToken, err := auth.GenerateToken(otherID, "logout_bystander", 1, secret)
	if err != nil {
		t.Fatalf("generate bystander token: %v", err)
	}
	if code := call(otherToken, "/api/v1/user/logout", `{}`); code != 0 {
		t.Fatalf("bystander logout code = %d, want 0 (its token must survive admin logout)", code)
	}
}
