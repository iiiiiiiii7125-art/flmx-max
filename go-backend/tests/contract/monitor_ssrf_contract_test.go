package contract_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
)

// AUDIT-R2 (F1X-001): 服务监控 TCP 目标 SSRF —— 低权限监控用户
// 创建面板本机执行的内网目标监控必须被拒绝。
func TestMonitorServiceCreateRejectsPrivateTargetForNonAdmin(t *testing.T) {
	secret := "f1x001-contract-secret"
	router, repo := setupContractRouter(t, secret)
	// 普通用户（roleID=1）+ 授予监控权限。
	seedContractUser(t, repo, 2, "monuser", 1, 1)
	if err := repo.InsertMonitorPermission(2, time.Now().UnixMilli()); err != nil {
		t.Fatalf("grant monitor permission: %v", err)
	}
	userToken, err := auth.GenerateToken(2, "monuser", 1, secret)
	if err != nil {
		t.Fatalf("generate user token: %v", err)
	}

	post := func(body string) response.R {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/monitor/services/create", bytes.NewBufferString(body))
		req.Header.Set("Authorization", userToken)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		var out response.R
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return out
	}

	// 1. 内网目标必须被拒绝（面板本机执行，nodeId 缺省为 0）。
	out := post(`{"name":"ssrf-poc","type":"tcp","target":"127.0.0.1:33571","intervalSec":60,"timeoutSec":5}`)
	if out.Code == 0 {
		t.Fatal("SSRF: 面板本机 127.0.0.1 监控目标被放行")
	}

	// 2. 私网段同样拒绝。
	out = post(`{"name":"ssrf-poc2","type":"tcp","target":"192.168.1.1:80","intervalSec":60,"timeoutSec":5}`)
	if out.Code == 0 {
		t.Fatal("SSRF: 面板本机 192.168.1.1 监控目标被放行")
	}

	// 3. 公网目标放行（功能不受损）。
	out = post(`{"name":"public-ok","type":"tcp","target":"8.8.8.8:53","intervalSec":60,"timeoutSec":5}`)
	if out.Code != 0 {
		t.Fatalf("公网目标被误拒: code=%d msg=%s", out.Code, out.Msg)
	}

	// 4. 指定节点执行（nodeId>0）不受面板本机 SSRF 限制（节点侧信任边界）。
	// 先建一个节点以通过 handler 的节点存在校验。
	if err := repo.DB().Create(&model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "127.0.0.1", Port: "30000", Status: 1}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	out = post(`{"name":"node-side","type":"tcp","target":"127.0.0.1:80","nodeId":1,"intervalSec":60,"timeoutSec":5}`)
	if out.Code != 0 {
		t.Fatalf("节点侧执行被误拒: code=%d msg=%s", out.Code, out.Msg)
	}
}
