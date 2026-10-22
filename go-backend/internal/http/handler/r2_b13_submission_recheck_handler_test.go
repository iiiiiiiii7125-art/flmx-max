package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R2X-049（补批13）handler 级回归：同名二次创建必须被拒且只落一行。
// 竞态窗口本身的确定性交错证明在 repo 层
// （internal/store/repo/r2_b13_submission_recheck_test.go：预检后、
// 提交前插入同名行，提交段复检拒绝）；这里钉住 handler 接线与
// 对外文案口径（与锁外预检一致）。

func r2b13DecodeResp(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, res.Body.String())
	}
	return env.Code, env.Msg
}

func TestR2B13_UserCreateDuplicateRejected(t *testing.T) {
	r, h := r2b2Open(t)
	payload := map[string]interface{}{"user": "b13-dup-user", "pwd": "secret123"}

	b1 := r2b2Post(t, h.userCreate, "/api/v1/user/create", payload, "")
	r2b2AssertCode0(t, "userCreate first", b1)

	res := httptest.NewRecorder()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/create", bytes.NewReader(body))
	h.userCreate(res, req)
	code, msg := r2b13DecodeResp(t, res)
	if code == 0 || !strings.Contains(msg, "用户名已存在") {
		t.Fatalf("duplicate userCreate: code=%d msg=%q, want rejection with 用户名已存在", code, msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM "user" WHERE user = 'b13-dup-user'`); n != 1 {
		t.Fatalf("user rows = %d, want 1", n)
	}
}

func TestR2B13_TunnelCreateDuplicateRejected(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	payload := []byte(`{
		"name":"b13-dup-tunnel",
		"type":1,
		"flow":1,
		"trafficRatio":1,
		"status":1,
		"inNodeId":[{"nodeId":10,"protocol":"tls"}]
	}`)

	res1 := httptest.NewRecorder()
	h.tunnelCreate(res1, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", bytes.NewReader(payload)))
	if code, msg := r2b13DecodeResp(t, res1); code != 0 {
		t.Fatalf("first tunnelCreate: code=%d msg=%q, want success", code, msg)
	}

	res2 := httptest.NewRecorder()
	h.tunnelCreate(res2, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/create", bytes.NewReader(payload)))
	code, msg := r2b13DecodeResp(t, res2)
	if code == 0 || !strings.Contains(msg, "隧道名称重复") {
		t.Fatalf("duplicate tunnelCreate: code=%d msg=%q, want rejection with 隧道名称重复", code, msg)
	}
	if n := mustQueryInt(t, h.repo, `SELECT COUNT(*) FROM tunnel WHERE name = 'b13-dup-tunnel'`); n != 1 {
		t.Fatalf("tunnel rows = %d, want 1", n)
	}
}
