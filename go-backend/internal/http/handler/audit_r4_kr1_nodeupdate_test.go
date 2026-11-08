package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

type kr1Envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func kr1CallNodeUpdate(t *testing.T, h *Handler, payload map[string]interface{}) kr1Envelope {
	t.Helper()
	req := newAuthenticatedJSONRequest(t, payload)
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var env kr1Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	return env
}

// TestNodeUpdateProtocolChangeRequiresOnline 钉 R4B03-002 方案①：
// 本地节点离线时改协议三开关整单拒绝并点名、且同单其他字段也不
// 落库；离线改其他字段、离线提交未变更的协议值均不受影响；DB 标
// 在线但无实时会话时下发失败同样整单拒绝不落库；真在线时下发
// SetProtocol 成功后落库。
func TestNodeUpdateProtocolChangeRequiresOnline(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r4-node-update.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()

	offlineID := kr1SeedNode(t, r, "r4-upd-offline", "198.51.100.31", 0, "agent")

	// 离线 + 协议变更 + 名称变更同单：整单拒绝，名称也不得落库。
	env := kr1CallNodeUpdate(t, h, map[string]interface{}{
		"id": offlineID, "socks": 1, "name": "should-not-stick",
	})
	if env.Code == 0 {
		t.Fatalf("离线改协议开关应被拒绝，却回成功: %+v", env)
	}
	if !strings.Contains(env.Msg, "节点未在线，等待节点上线后再设置") {
		t.Fatalf("拒绝文案应点名节点未在线: %q", env.Msg)
	}
	node, err := r.GetNodeByID(offlineID)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Socks != 0 || node.Name != "r4-upd-offline" {
		t.Fatalf("整单拒绝后不得有任何落库: socks=%d name=%q", node.Socks, node.Name)
	}

	// 离线只改其他字段：放行。
	env = kr1CallNodeUpdate(t, h, map[string]interface{}{
		"id": offlineID, "name": "renamed-offline",
	})
	if env.Code != 0 {
		t.Fatalf("离线改名称应放行: %+v", env)
	}
	// 离线提交与现值相同的协议值（无变更）：放行。
	env = kr1CallNodeUpdate(t, h, map[string]interface{}{
		"id": offlineID, "socks": 0, "name": "renamed-again",
	})
	if env.Code != 0 {
		t.Fatalf("离线提交未变更的协议值应放行: %+v", env)
	}
	node, _ = r.GetNodeByID(offlineID)
	if node.Name != "renamed-again" {
		t.Fatalf("离线改名未落库: name=%q", node.Name)
	}

	// DB 标在线但无实时会话（僵尸在线）：下发必失败，整单拒绝、
	// 不落库假成功。
	zombieID := kr1SeedNode(t, r, "r4-upd-zombie", "198.51.100.32", 1, "agent")
	env = kr1CallNodeUpdate(t, h, map[string]interface{}{
		"id": zombieID, "tls": 1,
	})
	if env.Code == 0 {
		t.Fatalf("下发失败应整单拒绝，却回成功: %+v", env)
	}
	node, _ = r.GetNodeByID(zombieID)
	if node.TLS != 0 {
		t.Fatalf("下发失败后 tls 不得落库: tls=%d", node.TLS)
	}

	// 真在线：SetProtocol 下发成功后落库。
	liveID := kr1SeedNode(t, r, "r4-upd-live", "198.51.100.33", 0, "agent")
	rec := &kr1CommandRecorder{}
	stopLive := startKR1MockNode(t, srv.URL, "r4-upd-live-secret", rec)
	defer stopLive()
	waitForCondition(t, 10*time.Second, func() bool { return h.wsServer.IsNodeOnline(liveID) }, "live mock node connected")
	env = kr1CallNodeUpdate(t, h, map[string]interface{}{
		"id": liveID, "http": 1,
	})
	if env.Code != 0 {
		t.Fatalf("在线改协议开关应成功: %+v", env)
	}
	if rec.count("SetProtocol") != 1 {
		t.Fatalf("SetProtocol 下发次数 = %d, want 1", rec.count("SetProtocol"))
	}
	if data := rec.lastSetProtocol(); data == nil || data["http"] != float64(1) {
		t.Fatalf("SetProtocol 载荷应含 http=1: %v", data)
	}
	node, _ = r.GetNodeByID(liveID)
	if node.HTTP != 1 {
		t.Fatalf("在线改协议开关未落库: http=%d", node.HTTP)
	}
}
