package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"context"
	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2 批 2 · 幂等族收口回归（R2X-004 覆盖面 + R2X-032 scope 键）。
//
// - R2X-004：新接入 7 面（monitorServiceCreate、inboundRelayCreate、
//   federationShareCreate、remoteSourceSave 新建分支、tunnelCreate、
//   userCreate、groupCreate）每面一条「同键重发只建一行」。
// - R2X-032：同键异端点各建一条——请求必须用真实注册路径构造
//   （现行 xp327 测试用 "/" 假路径正是缺陷掩盖者，本批已改掉）；
//   同键 subscription create 后同键 update（ID>0）正常执行不被回放。

func r2b2Post(t *testing.T, fn func(http.ResponseWriter, *http.Request), path string, payload map[string]interface{}, key string) string {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res := httptest.NewRecorder()
	fn(res, req)
	return res.Body.String()
}

func r2b2AuthPost(t *testing.T, fn func(http.ResponseWriter, *http.Request), path string, payload map[string]interface{}, key string) string {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	token, err := auth.GenerateToken(1, "admin", 0, "test-secret")
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	req.Header.Set("Authorization", token)
	claims, ok := auth.ValidateToken(token, "test-secret")
	if !ok {
		t.Fatal("validate token failed")
	}
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, claims))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res := httptest.NewRecorder()
	fn(res, req)
	return res.Body.String()
}

func r2b2AssertCode0(t *testing.T, label, body string) {
	t.Helper()
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil || env.Code != 0 {
		t.Fatalf("%s: want code 0, got %s", label, body)
	}
}

func r2b2Open(t *testing.T) (*repo.Repository, *Handler) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/r2b2.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, New(r, "test-secret")
}

// --- R2X-004 新接入面 ---

func TestR2B2_UserCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	payload := map[string]interface{}{"user": "idem-user-b2", "pwd": "secret123"}
	b1 := r2b2Post(t, h.userCreate, "/api/v1/user/create", payload, "b2-user-key")
	r2b2AssertCode0(t, "userCreate first", b1)
	b2 := r2b2Post(t, h.userCreate, "/api/v1/user/create", payload, "b2-user-key")
	if b2 != b1 {
		t.Fatalf("userCreate 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM "user" WHERE user = 'idem-user-b2'`); n != 1 {
		t.Fatalf("userCreate 同键建出 %d 行, want 1", n)
	}
}

func TestR2B2_GroupCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	ug := map[string]interface{}{"name": "idem-ugroup-b2"}
	b1 := r2b2Post(t, h.groupUserCreate, "/api/v1/group/user/create", ug, "b2-ugroup-key")
	r2b2AssertCode0(t, "groupUserCreate first", b1)
	b2 := r2b2Post(t, h.groupUserCreate, "/api/v1/group/user/create", ug, "b2-ugroup-key")
	if b2 != b1 {
		t.Fatalf("groupUserCreate 同键应回放:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_group WHERE name = 'idem-ugroup-b2'`); n != 1 {
		t.Fatalf("groupUserCreate 同键建出 %d 行, want 1", n)
	}
	tg := map[string]interface{}{"name": "idem-tgroup-b2"}
	b1 = r2b2Post(t, h.groupTunnelCreate, "/api/v1/group/tunnel/create", tg, "b2-tgroup-key")
	r2b2AssertCode0(t, "groupTunnelCreate first", b1)
	b2 = r2b2Post(t, h.groupTunnelCreate, "/api/v1/group/tunnel/create", tg, "b2-tgroup-key")
	if b2 != b1 {
		t.Fatalf("groupTunnelCreate 同键应回放:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel_group WHERE name = 'idem-tgroup-b2'`); n != 1 {
		t.Fatalf("groupTunnelCreate 同键建出 %d 行, want 1", n)
	}
}

func TestR2B2_TunnelCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
	}))
	defer remote.Close()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(
		`INSERT INTO node(name, secret, server_ip, port, status, is_remote, remote_url, remote_token, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"b2-remote-1", "s1", "10.0.0.1", "10000-20000", 1, 1, remote.URL, "tok", "agent", now,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(
		`INSERT INTO node(name, secret, server_ip, port, status, is_remote, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		"b2-local-1", "s2", "10.0.0.2", "10000-20000", 1, 0, "agent", now,
	).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{
		"name":      "idem-tunnel-b2",
		"type":      1,
		"inNodeId":  []map[string]interface{}{{"nodeId": 1, "port": 18081, "protocol": "tcp"}},
		"outNodeId": []map[string]interface{}{{"nodeId": 2, "port": 29001, "protocol": "tcp"}},
	}
	b1 := r2b2Post(t, h.tunnelCreate, "/api/v1/tunnel/create", payload, "b2-tunnel-key")
	r2b2AssertCode0(t, "tunnelCreate first", b1)
	b2 := r2b2Post(t, h.tunnelCreate, "/api/v1/tunnel/create", payload, "b2-tunnel-key")
	if b2 != b1 {
		t.Fatalf("tunnelCreate 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel WHERE name = 'idem-tunnel-b2'`); n != 1 {
		t.Fatalf("tunnelCreate 同键建出 %d 行, want 1", n)
	}
}

func TestR2B2_MonitorServiceCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	now := time.Now().UnixMilli()
	adminID, err := r.CreateUser("b2-admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"name":"idem-mon-b2","type":"tcp","target":"8.8.8.8:53","intervalSec":60,"timeoutSec":5}`
	call := func() string {
		w := httptest.NewRecorder()
		req := claimsCtxReq(http.MethodPost, "/api/v1/monitor/services/create", "", body, adminID, 0)
		req.Header.Set("Idempotency-Key", "b2-monitor-key")
		h.monitorServiceCreate(w, req)
		return w.Body.String()
	}
	b1 := call()
	r2b2AssertCode0(t, "monitorServiceCreate first", b1)
	b2 := call()
	if b2 != b1 {
		t.Fatalf("monitorServiceCreate 同键应回放:\n%s\n%s", b1, b2)
	}
	monitors, err := r.ListServiceMonitors()
	if err != nil || len(monitors) != 1 {
		t.Fatalf("monitorServiceCreate 同键建出 %d 行, want 1 (err=%v)", len(monitors), err)
	}
}

func TestR2B2_FederationShareCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	nodeID := seedRelayTargetNode(t, r, "b2-share-node", "10.0.2.5")
	payload := map[string]interface{}{
		"name": "idem-share-b2", "nodeId": nodeID,
		"portRangeStart": 20000, "portRangeEnd": 20010,
	}
	b1 := r2b2Post(t, h.federationShareCreate, "/api/v1/federation/share/create", payload, "b2-share-key")
	r2b2AssertCode0(t, "federationShareCreate first", b1)
	b2 := r2b2Post(t, h.federationShareCreate, "/api/v1/federation/share/create", payload, "b2-share-key")
	if b2 != b1 {
		t.Fatalf("federationShareCreate 同键应回放:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM peer_share WHERE name = 'idem-share-b2'`); n != 1 {
		t.Fatalf("federationShareCreate 同键建出 %d 行, want 1", n)
	}
}

func TestR2B2_RemoteSourceSaveCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	payload := map[string]interface{}{
		"name": "idem-src-b2", "kind": "file",
		"content": "trojan://pw@t.example.com:443#IdemNodeB2",
	}
	b1 := r2b2Post(t, h.remoteSourceSave, "/api/v1/subscription/source/save", payload, "b2-source-key")
	r2b2AssertCode0(t, "remoteSourceSave first", b1)
	b2 := r2b2Post(t, h.remoteSourceSave, "/api/v1/subscription/source/save", payload, "b2-source-key")
	if b2 != b1 {
		t.Fatalf("remoteSourceSave 同键应回放:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM remote_source WHERE name = 'idem-src-b2'`); n != 1 {
		t.Fatalf("remoteSourceSave 同键建出 %d 行, want 1", n)
	}
}

func TestR2B2_InboundRelayCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "b2-relay-entry", "198.51.100.21")
	tunnelID := seedTunnelForNftables(t, h, "idem-relay-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	inbound := &model.NodeInbound{
		Name: "idem-imported-b2", Protocol: "ss", Listen: "198.51.100.9", Port: 8388,
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{
		"inboundId": inbound.ID, "tunnelId": tunnelID, "inPort": 30007, "name": "idem-relay-b2",
	}
	b1 := r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create", payload, "b2-relay-key")
	r2b2AssertCode0(t, "inboundRelayCreate first", b1)
	b2 := r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create", payload, "b2-relay-key")
	if b2 != b1 {
		t.Fatalf("inboundRelayCreate 同键应回放:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_inbound_relay WHERE name = 'idem-relay-b2'`); n != 1 {
		t.Fatalf("inboundRelayCreate 同键建出 %d 条 relay, want 1", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM forward WHERE name = 'idem-relay-b2'`); n != 1 {
		t.Fatalf("inboundRelayCreate 同键建出 %d 条 forward, want 1", n)
	}
}

// --- R2X-032 scope 键 ---

func TestR2B2_SameKeyDifferentEndpointsEachCreate(t *testing.T) {
	r, h := r2b2Open(t)
	sl := map[string]interface{}{"name": "idem-x-sl", "speed": 100}
	b1 := r2b2Post(t, h.speedLimitCreate, "/api/v1/speed-limit/create", sl, "b2-cross-key")
	r2b2AssertCode0(t, "speedLimit first", b1)

	nd := map[string]interface{}{"name": "idem-x-node", "serverIp": "10.0.1.9"}
	b2 := r2b2Post(t, h.nodeCreate, "/api/v1/node/create", nd, "b2-cross-key")
	r2b2AssertCode0(t, "node with same key on other endpoint", b2)
	if b2 == b1 {
		t.Fatalf("同键异端点不得串话回放: %s", b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name='idem-x-sl'`); n != 1 {
		t.Fatalf("speed_limit rows = %d, want 1", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node WHERE name='idem-x-node'`); n != 1 {
		t.Fatalf("同键异端点: node rows = %d, want 1（跨端点回放吞创建）", n)
	}

	// 同端点同键仍回放（防过修）。
	b3 := r2b2Post(t, h.speedLimitCreate, "/api/v1/speed-limit/create", sl, "b2-cross-key")
	if b3 != b1 {
		t.Fatalf("同端点同键应回放:\n%s\n%s", b1, b3)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM speed_limit WHERE name='idem-x-sl'`); n != 1 {
		t.Fatalf("speed_limit rows after replay = %d, want 1", n)
	}
}

func TestR2B2_SubscriptionCreateThenUpdateSameKey(t *testing.T) {
	r, h := r2b2Open(t)
	base := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription`) // repo.Open 自带默认订阅种子
	create := map[string]interface{}{"name": "idem-sub-b2", "sourceAll": true}
	b1 := r2b2Post(t, h.subscriptionSave, "/api/v1/subscription/save", create, "b2-sub-key")
	r2b2AssertCode0(t, "subscription create", b1)
	subID := int64(mustQueryInt(t, r, `SELECT id FROM subscription WHERE name='idem-sub-b2'`))

	update := map[string]interface{}{"id": subID, "name": "idem-sub-b2-renamed", "sourceAll": true}
	b2 := r2b2Post(t, h.subscriptionSave, "/api/v1/subscription/save", update, "b2-sub-key")
	r2b2AssertCode0(t, "subscription update same key", b2)
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE name='idem-sub-b2-renamed'`); n != 1 {
		t.Fatalf("同键 update（ID>0）应正常执行改名, renamed rows = %d（被回放吞掉）", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription`); n != base+1 {
		t.Fatalf("subscription rows = %d, want %d (种子基线+新建 1)", n, base+1)
	}
}
