package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R2 B04-001: 节点地址写面旧序「先剥离后校验」使 IsValidNodeAddress 的
// scheme 拒绝与端口数值/范围校验不可达——"http://evil.com" 剥成
// "http"、"example.com:abc" 剥成 "example.com" 静默落库。修复为先验
// 原始值后剥离：坏形态在 create/update/V4/V6/监听地址各面一律拒绝、
// 不落库；合法 host:port 的 R30 剥离归一保持不变。

func TestR2K1NodeCreateRejectsBadPortAndScheme(t *testing.T) {
	r, h := r2b2Open(t)

	bad := []string{"http://evil.com", "example.com:abc", "foo:bar", "example.com:99999", "example.com:0"}
	for i, addr := range bad {
		payload, _ := json.Marshal(map[string]interface{}{
			"name": "k1-b04-bad", "serverIp": addr, "forwardMode": "agent",
		})
		out := pcFixCreateNode(t, h, string(payload))
		if out.Code == 0 {
			t.Fatalf("case %d: nodeCreate accepted serverIp %q", i, addr)
		}
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name = 'k1-b04-bad'`); n != 0 {
		t.Fatalf("rejected nodeCreate persisted %d rows", n)
	}

	// V4 面同型。
	payload, _ := json.Marshal(map[string]interface{}{
		"name": "k1-b04-badv4", "serverIp": "198.51.100.77", "serverIpV4": "http://evil.com", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code == 0 {
		t.Fatal("nodeCreate accepted serverIpV4 \"http://evil.com\"")
	}

	// 监听地址面同型：非数字端口不得被剥离吞掉。
	payload, _ = json.Marshal(map[string]interface{}{
		"name": "k1-b04-badlisten", "serverIp": "198.51.100.77", "tcpListenAddr": "203.0.113.7:abc", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code == 0 {
		t.Fatal("nodeCreate accepted tcpListenAddr with non-numeric port")
	}

	// 对照：合法 host:port 仍按 R30 口径剥离落库。
	payload, _ = json.Marshal(map[string]interface{}{
		"name": "k1-b04-good", "serverIp": "example.com:8443", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code != 0 {
		t.Fatalf("nodeCreate rejected valid host:port: %+v", out)
	}
	serverIP, _, _, _, _, _ := pcFixNodeAddrs(t, r, "k1-b04-good")
	if serverIP != "example.com" {
		t.Fatalf("stored server_ip = %q, want example.com (R30 strip preserved)", serverIP)
	}
}

func TestR2K1NodeUpdateRejectsBadPortAndScheme(t *testing.T) {
	r, h := r2b2Open(t)
	payload, _ := json.Marshal(map[string]interface{}{
		"name": "k1-b04-upd", "serverIp": "example.com:8443", "forwardMode": "agent",
	})
	if out := pcFixCreateNode(t, h, string(payload)); out.Code != 0 {
		t.Fatalf("seed nodeCreate failed: %+v", out)
	}
	id := pcFixNodeID(t, r, "k1-b04-upd")

	post := func(body map[string]interface{}) int {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		h.nodeUpdate(rec, req)
		var env struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode update response: %v", err)
		}
		return env.Code
	}

	if code := post(map[string]interface{}{"id": id, "serverIp": "http://evil.com"}); code == 0 {
		t.Fatal("nodeUpdate accepted serverIp \"http://evil.com\"")
	}
	if code := post(map[string]interface{}{"id": id, "serverIp": "example.com:abc"}); code == 0 {
		t.Fatal("nodeUpdate accepted serverIp \"example.com:abc\"")
	}
	serverIP, _, _, _, _, _ := pcFixNodeAddrs(t, r, "k1-b04-upd")
	if serverIP != "example.com" {
		t.Fatalf("server_ip changed to %q after rejected updates, want example.com", serverIP)
	}

	// 对照：合法更新照常。
	if code := post(map[string]interface{}{"id": id, "serverIp": "example.org:9443"}); code != 0 {
		t.Fatal("nodeUpdate rejected valid host:port")
	}
	serverIP, _, _, _, _, _ = pcFixNodeAddrs(t, r, "k1-b04-upd")
	if serverIP != "example.org" {
		t.Fatalf("server_ip = %q after valid update, want example.org", serverIP)
	}
}
