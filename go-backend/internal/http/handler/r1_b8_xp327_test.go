package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// X-P3-27 (L-11-2): 节点/限速/订阅/入站创建同批接入 X-P2-17 幂等键
// 机制。各创建面同键重发只建一行、回放首次结果。
// R2 批 2 测试构造修正（R2X-032）：请求一律用真实注册路径构造。
// 此前 post 助手写死 "/" 假路径——R2X-032 的键维度缺陷（缓存键不含
// 端点/动作）正是被该构造掩盖（假路径下各面 method+path 恒同、scope
// 不存在也看不出来）。改为逐面传真实路径，断言强度不变。
func TestR1B8_XP327_CreateSurfacesIdempotencyKey(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp327.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	post := func(fn func(http.ResponseWriter, *http.Request), path string, payload map[string]interface{}, key string) string {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res := httptest.NewRecorder()
		fn(res, req)
		return res.Body.String()
	}
	assertCode0 := func(label, body string) {
		t.Helper()
		var env struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil || env.Code != 0 {
			t.Fatalf("%s: want code 0, got %s", label, body)
		}
	}

	// 限速创建
	sl := map[string]interface{}{"name": "idem-sl", "speed": 100}
	b1 := post(h.speedLimitCreate, "/api/v1/speed-limit/create", sl, "sl-key")
	assertCode0("speedLimit first", b1)
	b2 := post(h.speedLimitCreate, "/api/v1/speed-limit/create", sl, "sl-key")
	if b2 != b1 {
		t.Fatalf("speedLimit 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name='idem-sl'`); n != 1 {
		t.Fatalf("speedLimit 同键建出 %d 行, want 1", n)
	}
	post(h.speedLimitCreate, "/api/v1/speed-limit/create", sl, "sl-key-2")
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name='idem-sl'`); n != 2 {
		t.Fatalf("speedLimit 不同键应建第二行, got %d", n)
	}

	// 节点创建
	nd := map[string]interface{}{"name": "idem-node", "serverIp": "10.0.1.5"}
	b1 = post(h.nodeCreate, "/api/v1/node/create", nd, "node-key")
	assertCode0("node first", b1)
	b2 = post(h.nodeCreate, "/api/v1/node/create", nd, "node-key")
	if b2 != b1 {
		t.Fatalf("node 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name='idem-node'`); n != 1 {
		t.Fatalf("node 同键建出 %d 行, want 1", n)
	}

	// 订阅创建（save 的新建分支）
	sb := map[string]interface{}{"name": "idem-sub", "sourceAll": true}
	b1 = post(h.subscriptionSave, "/api/v1/subscription/save", sb, "sub-key")
	assertCode0("subscription first", b1)
	b2 = post(h.subscriptionSave, "/api/v1/subscription/save", sb, "sub-key")
	if b2 != b1 {
		t.Fatalf("subscription 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE name='idem-sub'`); n != 1 {
		t.Fatalf("subscription 同键建出 %d 行, want 1", n)
	}

	// 入站创建：种子节点+内核。
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('idem-in-node', 's', '203.0.113.31', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	kNodeID := mustLastInsertID(t, r, "node")
	if err := r.DB().Exec(`INSERT INTO node_kernel(node_id, kernel_type, version, status, created_time, updated_time) VALUES(?, 'xray', 'latest', 'pending', ?, ?)`, kNodeID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	ib := map[string]interface{}{
		"name": "idem-inbound", "nodeId": kNodeID, "kernelType": "xray",
		"protocol": "trojan", "listen": "::", "port": 32443,
		"settings": map[string]interface{}{"password": "pw"},
		"security": map[string]interface{}{"type": "tls", "sni": "example.com", "certMode": "self"},
	}
	b1 = post(h.inboundCreate, "/api/v1/inbound/create", ib, "in-key")
	assertCode0("inbound first", b1)
	b2 = post(h.inboundCreate, "/api/v1/inbound/create", ib, "in-key")
	if b2 != b1 {
		t.Fatalf("inbound 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound WHERE name='idem-inbound'`); n != 1 {
		t.Fatalf("inbound 同键建出 %d 行, want 1", n)
	}
}
