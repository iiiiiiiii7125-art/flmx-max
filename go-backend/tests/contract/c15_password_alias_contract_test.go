package contract_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

// c15ReqSeq 为本文件 post 的每个请求分配独立源 IP（见 post 内注释）。
var c15ReqSeq atomic.Uint32

// R1-C15 / B01-003：改密接口不得接受新密码与当前密码完全相同。
// 同值提交必须被拒，且不得落库、不得推进 password_changed_at（否则会
// 把其它会话踢下线）、不得让既有令牌失效；管理员与普通用户同口径。
// 正向对照：换成真正的新密码仍可成功，且随后可用新密码登录。
func TestR1C15UpdatePasswordSameValueRejected(t *testing.T) {
	const secret = "c15-jwt-secret"
	router, r := setupContractRouter(t, secret)

	post := func(path, token string, payload interface{}) response.R {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		// 测试侧隔离限流桶键值（照 passkey_contract_test.go 先例）：登录限流
		// 按客户端 IP 计数（globalLoginLimiter，10/min/IP，K-005 有意设计，
		// 不得放宽）。本用例每轮含 2 次登录，-count 连跑时各轮共用同一进程
		// 全局限流器，而 httptest 默认 RemoteAddr 全包共用同一地址，第 6 轮起
		// 首个登录必撞 429（与被测改密语义无关的测试侧串扰）。此处仅在测试
		// 侧让每个请求带独立源 IP、落入自己的限流桶；产品限流逻辑与阈值
		// 一字未动，断言不变。
		n := c15ReqSeq.Add(1)
		req.RemoteAddr = fmt.Sprintf("198.51.%d.%d:1234", (n>>8)&0xff, n&0xff)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		var out response.R
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode response for %s: %v (status %d)", path, err, resp.Code)
		}
		return out
	}

	changePassword := func(token, username, current, next string) response.R {
		t.Helper()
		return post("/api/v1/user/updatePassword", token, map[string]string{
			"newUsername":     username,
			"currentPassword": current,
			"newPassword":     next,
			"confirmPassword": next,
		})
	}

	// --- 管理员路径：默认口令同值改密（强制改密空转场景） ---
	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}
	adminChangedAtBefore := mustQueryPasswordChangedAtByUsername(t, r, "admin@user")

	out := changePassword(adminToken, "admin@user", "admin@user", "admin@user")
	if out.Code == 0 || !strings.Contains(out.Msg, "不能与当前密码相同") {
		t.Fatalf("admin same-value change must be rejected, got code=%d msg=%q", out.Code, out.Msg)
	}
	if got := mustQueryPasswordChangedAtByUsername(t, r, "admin@user"); got != adminChangedAtBefore {
		t.Fatalf("rejected same-value change must not advance password_changed_at: got %d want %d", got, adminChangedAtBefore)
	}
	// 拒绝不得踢会话：改密前签发的令牌仍须可用（export 返回原始备份
	// 载荷而非信封，故以 HTTP 状态断言）。
	{
		req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/export", bytes.NewBufferString(`{"types":["configs"]}`))
		req.Header.Set("Authorization", adminToken)
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("pre-change admin token must stay valid after rejected same-value change, export status=%d", resp.Code)
		}
	}
	// 密码未被改动：仍可用原口令登录。
	loginOut := post("/api/v1/user/login", "", map[string]string{"username": "admin@user", "password": "admin@user"})
	if loginOut.Code != 0 {
		t.Fatalf("login with original admin password must still work, got code=%d msg=%q", loginOut.Code, loginOut.Msg)
	}

	// --- 普通用户路径：同口径 ---
	userChangedAt := seedContractUser(t, r, 2, "c15_user", 1, 1) // 密码 contract-pass
	userToken, err := auth.GenerateToken(2, "c15_user", 1, secret)
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}
	out = changePassword(userToken, "c15_user", "contract-pass", "contract-pass")
	if out.Code == 0 || !strings.Contains(out.Msg, "不能与当前密码相同") {
		t.Fatalf("user same-value change must be rejected, got code=%d msg=%q", out.Code, out.Msg)
	}
	if got := mustQueryPasswordChangedAtByUsername(t, r, "c15_user"); got != userChangedAt {
		t.Fatalf("rejected same-value change must not advance user password_changed_at: got %d want %d", got, userChangedAt)
	}

	// --- 正向对照：真换密码仍成功，且新口令同值再改仍被拒 ---
	out = changePassword(adminToken, "admin@user", "admin@user", "Admin@2026new")
	if out.Code != 0 {
		t.Fatalf("real password change must succeed, got code=%d msg=%q", out.Code, out.Msg)
	}
	// 中间件以 IatMs <= PasswordChangedAt 判定吊销（同毫秒亦拒绝，产品有意
	// 的 fail-closed 行为，不得改动）；真改密刚把 PasswordChangedAt 推进到
	// 当前毫秒，紧接着用真实时刻现签的 token 会偶发同毫秒被 401，到不了
	// 下面的同值校验。故测试侧照 passkey_contract_test.go 的先例：以落库的
	// PasswordChangedAt 为锚，用 GenerateTokenAt 把新 token 的签发时刻显式
	// 置于改密之后 2s，模拟真实重新登录的时间间隔，不依赖睡眠竞速。
	changedAt := mustQueryPasswordChangedAtByUsername(t, r, "admin@user")
	adminToken2, err := auth.GenerateTokenAt(1, "admin@user", 0, secret, time.UnixMilli(changedAt).Add(2*time.Second))
	if err != nil {
		t.Fatalf("regenerate admin token: %v", err)
	}
	out = changePassword(adminToken2, "admin@user", "Admin@2026new", "Admin@2026new")
	if out.Code == 0 || !strings.Contains(out.Msg, "不能与当前密码相同") {
		t.Fatalf("same-value change against new password must be rejected, got code=%d msg=%q", out.Code, out.Msg)
	}
	loginOut = post("/api/v1/user/login", "", map[string]string{"username": "admin@user", "password": "Admin@2026new"})
	if loginOut.Code != 0 {
		t.Fatalf("login with new admin password must work, got code=%d msg=%q", loginOut.Code, loginOut.Msg)
	}
}

// R1-C15 / B14-002：备份端点全集的双前缀别名必须与正常前缀覆盖一致。
// 清单与 handler.go 中备份路由注册表一一对应（10 条 × 2 前缀）；任一路径
// 不得再返回 Go ServeMux 的裸文本 404，必须走 handler（信封或备份载荷）。
func TestR1C15BackupDoublePrefixAliasesComplete(t *testing.T) {
	const secret = "c15-jwt-secret"
	router, _ := setupContractRouter(t, secret)

	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("generate admin token: %v", err)
	}

	type backupRoute struct {
		path   string
		method string
		body   string
	}
	// 畸形/空 body 使写型端点在解码或令牌校验处提前以信封收口，
	// 不触发真实导入、上传或外网调用；GET 端点直接读状态。
	routes := []backupRoute{
		{"/api/v1/backup/export", http.MethodPost, `{"types":["configs"]}`},
		{"/api/v1/backup/import-auto-backup", http.MethodGet, ""},
		{"/api/v1/backup/import-auto-backup/status", http.MethodGet, ""},
		{"/api/v1/backup/gist/status", http.MethodPost, `{}`},
		{"/api/v1/backup/gist/save", http.MethodPost, `{}`},
		{"/api/v1/backup/gist/upload", http.MethodPost, `{}`},
		{"/api/v1/backup/gist/versions", http.MethodPost, `{}`},
		{"/api/v1/backup/gist/restore", http.MethodPost, `{`},
		{"/api/v1/backup/import", http.MethodPost, `{`},
		{"/api/v1/backup/restore", http.MethodPost, `{`},
	}

	do := func(route backupRoute, path string) (int, []byte) {
		t.Helper()
		var body io.Reader
		if route.body != "" {
			body = bytes.NewBufferString(route.body)
		}
		req := httptest.NewRequest(route.method, path, body)
		req.Header.Set("Authorization", adminToken)
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body for %s: %v", path, err)
		}
		return resp.Code, raw
	}

	for _, route := range routes {
		for _, prefixPath := range []string{route.path, "/api/v1" + route.path} {
			status, raw := do(route, prefixPath)
			if strings.TrimSpace(string(raw)) == "404 page not found" {
				t.Fatalf("%s %s returned bare mux 404 (route not registered)", route.method, prefixPath)
			}
			var probe map[string]interface{}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("%s %s must answer with JSON (envelope or backup payload), got status=%d body=%q", route.method, prefixPath, status, string(raw))
			}
		}
	}

	// 关键端点等价性：status 两前缀都须 code=0 且 exists 一致（B14-002 原 repro）。
	for _, path := range []string{"/api/v1/backup/import-auto-backup/status", "/api/v1/api/v1/backup/import-auto-backup/status"} {
		_, raw := do(backupRoute{method: http.MethodGet}, path)
		var out struct {
			Code int `json:"code"`
			Data struct {
				Exists bool `json:"exists"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode status envelope for %s: %v", path, err)
		}
		if out.Code != 0 || out.Data.Exists {
			t.Fatalf("expected code=0 exists=false on fresh db for %s, got code=%d exists=%v", path, out.Code, out.Data.Exists)
		}
	}
}
