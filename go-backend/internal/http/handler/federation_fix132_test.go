package handler

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// FIX-132 联邦修复批（D3-01…D3-08）的回归测试集合。
// 每条测试对应一个审计编号，场景尽量复刻 R5 真机定案的请求/响应。

func fix132ShareFixture(t *testing.T, rangeStart, rangeEnd int) (*Handler, *repo.Repository, *repo.PeerShare) {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	h := New(r, "test-jwt-secret")
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "fix132-node", "fix132-secret", "10.90.80.71", "10.90.80.71", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, 0, "", "", "").Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "fix132-node")

	share := &repo.PeerShare{
		Name:           "fix132-share",
		NodeID:         nodeID,
		Token:          "fix132-share-token",
		PortRangeStart: rangeStart,
		PortRangeEnd:   rangeEnd,
		IsActive:       1,
		CreatedTime:    now,
		UpdatedTime:    now,
	}
	if err := r.CreatePeerShare(share); err != nil {
		t.Fatalf("create share: %v", err)
	}
	loaded, err := r.GetPeerShareByToken(share.Token)
	if err != nil || loaded == nil {
		t.Fatalf("load share: %v", err)
	}
	return h, r, loaded
}

func fix132PeerRequest(t *testing.T, h *Handler, path, token string, body interface{}, invoke func(http.ResponseWriter, *http.Request)) response.R {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	invoke(res, req)
	var payload response.R
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload
}

// D3-06: 端口范围未设（0/0）时 validateFederationCommandPorts 此前直接
// return nil 放行任意端口，与 tunnel/create、reserve-port 的 fail-closed
// 语义打架（R5 T2.3 坐实：:39999 被接受并落库为期望状态）。
func TestValidateFederationCommandPortsRejectsUnsetRange(t *testing.T) {
	share := &repo.PeerShare{PortRangeStart: 0, PortRangeEnd: 0}
	data := []interface{}{
		map[string]interface{}{"name": "svc", "addr": ":39999"},
	}
	if err := validateFederationCommandPorts(share, data); err == nil {
		t.Fatal("unset port range (0/0) must reject service commands, got nil")
	}

	// 半设置（start>0, end=0）同样是无效范围，必须 fail-closed。
	half := &repo.PeerShare{PortRangeStart: 31000, PortRangeEnd: 0}
	if err := validateFederationCommandPorts(half, data); err == nil {
		t.Fatal("half-configured port range must reject service commands, got nil")
	}

	// 无服务条目时没有可监听端口，保持放行（删除类命令不走此校验）。
	if err := validateFederationCommandPorts(share, nil); err != nil {
		t.Fatalf("empty service list should pass, got: %v", err)
	}
}

// D3-06 / R5 T2: 同一 share（0/0）、同一端口 39999，三条路径必须同口径拒绝。
func TestFederationPortPathsConsistentOnUnsetRange(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 0, 0)

	tunnelPayload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 39999, Target: "1.1.1.1:443",
	}, h.federationTunnelCreate)
	if tunnelPayload.Code != 403 {
		t.Fatalf("tunnel/create on unset range: expected 403, got %d (%s)", tunnelPayload.Code, tunnelPayload.Msg)
	}

	reservePayload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/reserve-port", share.Token, federationRuntimeReservePortRequest{
		ResourceKey: "tunnel:1:node:1:type:3:hop:0", Protocol: "tls", RequestedPort: 39999,
	}, h.federationRuntimeReservePort)
	if reservePayload.Code == 0 {
		t.Fatalf("reserve-port on unset range: expected rejection, got success (%s)", reservePayload.Msg)
	}

	commandPayload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/command", share.Token, federationRuntimeCommandRequest{
		CommandType: "AddService",
		Data: []interface{}{
			map[string]interface{}{
				"name":     "r5-canary-svc",
				"addr":     ":39999",
				"handler":  map[string]interface{}{"type": "tcp"},
				"listener": map[string]interface{}{"type": "tcp"},
			},
		},
	}, h.federationRuntimeCommand)
	if commandPayload.Code != 403 {
		t.Fatalf("runtime/command on unset range: expected 403, got %d (%s)", commandPayload.Code, commandPayload.Msg)
	}
	if !strings.Contains(commandPayload.Msg, "port") {
		t.Fatalf("runtime/command rejection should mention the port range, got %q", commandPayload.Msg)
	}

	resourceCount := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share_resource WHERE share_id = ?`, share.ID)
	if resourceCount != 0 {
		t.Fatalf("rejected command must not persist desired state, found %d resource rows", resourceCount)
	}
	runtimeCount := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share_runtime WHERE share_id = ?`, share.ID)
	if runtimeCount != 0 {
		t.Fatalf("rejected paths must not create runtimes, found %d rows", runtimeCount)
	}
}

// D3-04（P1）：middle/exit 角色服务此前零鉴权——任何知道
// IP:port 的人都能把提供方节点当任意出口（exit）或向 middle 注入
// 流量。修复口径：每个 share 一组独立 relay 凭据，服务 handler 恒带
// auth；消费方经 Connect 取得凭据、存 RemoteConfig、建链时写入
// connector auth；存量 share 由提供方懒补发。
func TestPeerShareRelayCredentialsLifecycle(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	if share.RelayUsername != "" || share.RelayPassword != "" {
		t.Fatal("fixture share must start without relay credentials")
	}

	connect := func() map[string]interface{} {
		out := fix132PeerRequest(t, h, "/api/v1/federation/connect", share.Token, map[string]interface{}{},
			func(w http.ResponseWriter, req *http.Request) { h.federationConnect(w, req) })
		if out.Code != 0 {
			t.Fatalf("connect failed: %+v", out)
		}
		data, _ := out.Data.(map[string]interface{})
		return data
	}

	first := connect()
	username := asString(first["relayUsername"])
	password := asString(first["relayPassword"])
	if !strings.HasPrefix(username, "share_") || len(password) < 20 {
		t.Fatalf("connect must issue per-share relay credentials, got user=%q password_len=%d", username, len(password))
	}
	stored, err := r.GetPeerShare(share.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload share: %v", err)
	}
	if stored.RelayUsername != username || stored.RelayPassword != password {
		t.Fatal("issued credentials must be persisted on the share (lazy backfill)")
	}
	second := connect()
	if asString(second["relayUsername"]) != username || asString(second["relayPassword"]) != password {
		t.Fatal("credentials must be stable across connects, not rotated per call")
	}
}

func TestFederationRoleServiceCarriesRelayAuth(t *testing.T) {
	a := newCleanupAgent(t)
	now := time.Now().UnixMilli()
	share := &repo.PeerShare{
		Name: "role-auth", NodeID: 1, Token: "role-auth-token",
		PortRangeStart: 20000, PortRangeEnd: 20010, IsActive: 1,
		RelayUsername: "share_roleauth", RelayPassword: "role-auth-password-0123456789",
		CreatedTime: now, UpdatedTime: now,
	}
	if err := a.h.repo.CreatePeerShare(share); err != nil {
		t.Fatal(err)
	}

	reserve := func(key string, port int) string {
		out := fix132PeerRequest(t, a.h, "/api/v1/federation/runtime/reserve-port", share.Token,
			map[string]interface{}{"resourceKey": key, "protocol": "tls", "requestedPort": port},
			func(w http.ResponseWriter, req *http.Request) { a.h.federationRuntimeReservePort(w, req) })
		if out.Code != 0 {
			t.Fatalf("reserve %s failed: %+v", key, out)
		}
		data, _ := out.Data.(map[string]interface{})
		return asString(data["reservationId"])
	}
	applyRole := func(reservationID, role string, targets []map[string]interface{}) response.R {
		return fix132PeerRequest(t, a.h, "/api/v1/federation/runtime/apply-role", share.Token,
			map[string]interface{}{
				"reservationId": reservationID, "role": role, "protocol": "tls", "strategy": "round",
				"targets": targets,
			},
			func(w http.ResponseWriter, req *http.Request) { a.h.federationRuntimeApplyRole(w, req) })
	}
	serviceHandlerAuth := func() map[string]interface{} {
		cmds := a.commandsOfType("UpdateService")
		if len(cmds) == 0 {
			return nil
		}
		var services []map[string]interface{}
		if err := json.Unmarshal(cmds[len(cmds)-1].Data, &services); err != nil || len(services) == 0 {
			return nil
		}
		handlerCfg, _ := services[0]["handler"].(map[string]interface{})
		auth, _ := handlerCfg["auth"].(map[string]interface{})
		return auth
	}

	// exit 角色：服务 handler 必须带本 share 的凭据。
	exitReservation := reserve("role-auth:exit", 20001)
	if out := applyRole(exitReservation, "exit", nil); out.Code != 0 {
		t.Fatalf("apply exit role failed: %+v", out)
	}
	if auth := serviceHandlerAuth(); asString(auth["username"]) != "share_roleauth" ||
		asString(auth["password"]) != "role-auth-password-0123456789" {
		t.Fatalf("exit service handler must carry the share relay auth, got %v", auth)
	}

	// middle 角色：目标带远端 share 凭据时，链 connector 必须带该凭据；
	// middle 自身服务仍带本 share 凭据。
	middleReservation := reserve("role-auth:middle", 20002)
	out := applyRole(middleReservation, "middle", []map[string]interface{}{
		{"host": "203.0.113.9", "port": 20001, "protocol": "tls",
			"relayUsername": "share_next", "relayPassword": "next-share-password-0123456789"},
	})
	if out.Code != 0 {
		t.Fatalf("apply middle role failed: %+v", out)
	}
	chainCmds := a.commandsOfType("UpdateChains")
	if len(chainCmds) == 0 {
		t.Fatal("middle role must dispatch UpdateChains")
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(chainCmds[len(chainCmds)-1].Data, &payload); err != nil {
		t.Fatal(err)
	}
	chainData, _ := payload["data"].(map[string]interface{})
	hops, _ := chainData["hops"].([]interface{})
	if len(hops) == 0 {
		t.Fatal("chain has no hops")
	}
	nodes, _ := hops[0].(map[string]interface{})["nodes"].([]interface{})
	if len(nodes) == 0 {
		t.Fatal("hop has no nodes")
	}
	connector, _ := nodes[0].(map[string]interface{})["connector"].(map[string]interface{})
	connAuth, _ := connector["auth"].(map[string]interface{})
	if asString(connAuth["username"]) != "share_next" ||
		asString(connAuth["password"]) != "next-share-password-0123456789" {
		t.Fatalf("middle chain connector must carry the target share auth, got %v", connAuth)
	}
	if auth := serviceHandlerAuth(); asString(auth["username"]) != "share_roleauth" {
		t.Fatalf("middle service handler must carry its own share auth, got %v", auth)
	}

	// middle 目标指向私网必须被拒（与 exit 的连接时过滤同口径），且
	// 不得下发新服务。
	before := len(a.commandsOfType("UpdateService"))
	badReservation := reserve("role-auth:middle-bad", 20003)
	out = applyRole(badReservation, "middle", []map[string]interface{}{
		{"host": "192.168.1.10", "port": 20001, "protocol": "tls"},
	})
	if out.Code == 0 {
		t.Fatal("middle target on a private address must be rejected")
	}
	if got := len(a.commandsOfType("UpdateService")); got != before {
		t.Fatal("rejected middle apply must not dispatch services")
	}
}

// TestBuildTunnelChainConfigCarriesRemoteRelayAuth 锁定消费方本地链：
// 目标是远端节点且其 RemoteConfig 带 share 凭据时，connector 必须带
// auth；本地目标不得带。
func TestBuildTunnelChainConfigCarriesRemoteRelayAuth(t *testing.T) {
	from := &nodeRecord{ID: 1, Name: "local", ServerIP: "203.0.113.1"}
	remote := &nodeRecord{
		ID: 2, Name: "remote", ServerIP: "203.0.113.2", IsRemote: 1,
		RemoteConfig: `{"remoteUrl":"http://203.0.113.2:6365","token":"peer-token","shareId":9,"relayUsername":"share_abc","relayPassword":"remote-share-password"}`,
	}
	localTarget := &nodeRecord{ID: 3, Name: "local2", ServerIP: "203.0.113.3"}
	nodes := map[int64]*nodeRecord{1: from, 2: remote, 3: localTarget}

	connectorOf := func(targets []tunnelRuntimeNode) map[string]interface{} {
		chain, err := buildTunnelChainConfig(77, 1, targets, nodes, "")
		if err != nil {
			t.Fatal(err)
		}
		hops := chain["hops"].([]map[string]interface{})
		items := hops[0]["nodes"].([]map[string]interface{})
		connector, _ := items[0]["connector"].(map[string]interface{})
		return connector
	}

	conn := connectorOf([]tunnelRuntimeNode{{NodeID: 2, Protocol: "tls", Strategy: "round", Port: 45000}})
	auth, _ := conn["auth"].(map[string]interface{})
	if asString(auth["username"]) != "share_abc" || asString(auth["password"]) != "remote-share-password" {
		t.Fatalf("remote target connector must carry the share relay auth, got %v", auth)
	}

	conn = connectorOf([]tunnelRuntimeNode{{NodeID: 3, Protocol: "tls", Strategy: "round", Port: 45001}})
	if _, present := conn["auth"]; present {
		t.Fatal("local target connector must not carry relay auth")
	}
}

// D3-07（P2 疑似）：diagnose/tcpping 此前校验一遍、下发原始主机名，
// 节点二次解析可被 DNS rebinding 引向内网。修复后下发的必须是面板
// 解析校验后钉死的 IP。
func TestFederationDiagnosePinsResolvedIP(t *testing.T) {
	a := newCleanupAgent(t)
	now := time.Now().UnixMilli()
	if err := a.h.repo.CreatePeerShare(&repo.PeerShare{
		Name: "diag-pin", NodeID: 1, Token: "diag-pin-token",
		PortRangeStart: 20000, PortRangeEnd: 20010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	origLookup := federationDiagnoseLookupIP
	federationDiagnoseLookupIP = func(host string) ([]net.IP, error) {
		if host == "diag-target.test" {
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	t.Cleanup(func() { federationDiagnoseLookupIP = origLookup })

	call := func(path, body string) response.R {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer diag-pin-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if strings.HasSuffix(path, "/diagnose") {
			a.h.federationRuntimeDiagnose(rec, req)
		} else {
			a.h.federationRuntimeCommand(rec, req)
		}
		var out response.R
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	lastPing := func() (string, int) {
		cmds := a.commandsOfType("TcpPing")
		if len(cmds) == 0 {
			return "", 0
		}
		var data map[string]interface{}
		if err := json.Unmarshal(cmds[len(cmds)-1].Data, &data); err != nil {
			t.Fatal(err)
		}
		return asString(data["ip"]), asInt(data["count"], 0)
	}

	// diagnose 入口：下发的必须是解析后钉死的 IP，不能是原始主机名。
	// （假 agent 的 ACK 不带 data，handler 以「节点未返回诊断数据」
	// 收尾，断言对象是已下发的命令本身。）
	_ = call("/api/v1/federation/runtime/diagnose",
		`{"ip":"diag-target.test","port":443,"count":4}`)
	if ip, _ := lastPing(); ip != "203.0.113.9" {
		t.Fatalf("diagnose must dispatch the pinned IP 203.0.113.9, got %q", ip)
	}

	// runtime/command 的 TcpPing 变体同口径：IP 钉死 + count 钳制。
	_ = call("/api/v1/federation/runtime/command",
		`{"commandType":"TcpPing","data":{"ip":"diag-target.test","port":443,"count":50}}`)
	if ip, count := lastPing(); ip != "203.0.113.9" || count != maxFederationDiagnoseCount {
		t.Fatalf("tcpping must dispatch pinned IP with clamped count, got ip=%q count=%d", ip, count)
	}

	// 解析结果含内网地址的主机名仍整体拒绝，且不得下发任何新命令。
	before := len(a.commandsOfType("TcpPing"))
	federationDiagnoseLookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("192.168.1.1")}, nil
	}
	out := call("/api/v1/federation/runtime/diagnose",
		`{"ip":"diag-target.test","port":443,"count":4}`)
	if out.Code == 0 {
		t.Fatalf("hostname resolving to a private address must be rejected, got %+v", out)
	}
	if got := len(a.commandsOfType("TcpPing")); got != before {
		t.Fatal("no command may be dispatched for a rejected target")
	}
}

// D3-08 / R5 T1.3 旁证：peer 端点把原始 DB 错误回吐给持 token 对端
// （实测原文 "constraint failed: UNIQUE constraint failed: ..."）。
// 与 authPeer 已硬化的口径对齐：记日志、只回 "Internal error"。
func TestPeerEndpointsDoNotLeakDatabaseErrors(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 31000, 31099)

	// 制造确定性的 DB 层失败：删掉 runtime 表，后续 repo 调用必报错。
	if err := r.DB().Exec(`DROP TABLE peer_share_runtime`).Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}

	reservePayload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/reserve-port", share.Token, federationRuntimeReservePortRequest{
		ResourceKey: "tunnel:1:node:1:type:3:hop:0", Protocol: "tls",
	}, h.federationRuntimeReservePort)
	if reservePayload.Code == 0 {
		t.Fatal("reserve-port against dropped table must fail")
	}
	if strings.Contains(reservePayload.Msg, "no such table") || strings.Contains(reservePayload.Msg, "peer_share_runtime") {
		t.Fatalf("reserve-port leaked raw DB error to peer: %q", reservePayload.Msg)
	}
	if reservePayload.Msg != "Internal error" {
		t.Fatalf("reserve-port should return the classified message, got %q", reservePayload.Msg)
	}

	tunnelPayload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, federationTunnelRequest{
		Protocol: "tcp", RemotePort: 31001, Target: "1.1.1.1:443",
	}, h.federationTunnelCreate)
	if tunnelPayload.Code == 0 {
		t.Fatal("tunnel/create against dropped table must fail")
	}
	if strings.Contains(tunnelPayload.Msg, "no such table") || strings.Contains(tunnelPayload.Msg, "peer_share_runtime") {
		t.Fatalf("tunnel/create leaked raw DB error to peer: %q", tunnelPayload.Msg)
	}
	if tunnelPayload.Msg != "Internal error" {
		t.Fatalf("tunnel/create should return the classified message, got %q", tunnelPayload.Msg)
	}
}

// D3-02: nodeCreate 直填 remoteUrl/remoteToken 此前零校验，绕过
// nodeImport 的唯一 SSRF 校验点——同一数据两条录入路径一条有门一条无门。
func TestNodeCreateValidatesRemoteURLLikeImport(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")

	create := func(remoteURL string) response.R {
		body, _ := json.Marshal(map[string]interface{}{
			"name":        "fix132-remote-node",
			"serverIp":    "203.0.113.20",
			"isRemote":    1,
			"remoteUrl":   remoteURL,
			"remoteToken": "peer-token",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		h.nodeCreate(res, req)
		var payload response.R
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return payload
	}

	// 与 import 路径完全同口径：IsSafeRemoteAddr 拒 loopback/RFC1918
	// （其链路本地缺口是既有共享实现的问题，元数据在 D3-01 的客户端
	// 拨号层另行硬拦，不在本条断言范围）。
	for _, bad := range []string{
		"http://127.0.0.1:6365",    // loopback
		"http://192.168.1.10:6365", // RFC1918
		"http://10.0.0.5:6365",     // RFC1918
		"ftp://203.0.113.10:6365",  // 非 http(s) scheme
		"203.0.113.10:6365",        // 缺 scheme
	} {
		payload := create(bad)
		if payload.Code == 0 {
			t.Fatalf("remoteUrl %q must be rejected by nodeCreate like nodeImport", bad)
		}
	}
	count := mustQueryInt(t, r, `SELECT COUNT(1) FROM node WHERE name = 'fix132-remote-node'`)
	if count != 0 {
		t.Fatalf("rejected remote nodes must not be persisted, found %d rows", count)
	}

	// 公网地址应放行（IP 字面量免 DNS 依赖），与 import 路径口径一致。
	payload := create("http://203.0.113.10:6365")
	if payload.Code != 0 {
		t.Fatalf("public remoteUrl must be accepted, got %d (%s)", payload.Code, payload.Msg)
	}
	count = mustQueryInt(t, r, `SELECT COUNT(1) FROM node WHERE name = 'fix132-remote-node'`)
	if count != 1 {
		t.Fatalf("expected 1 persisted remote node, got %d", count)
	}
}

// D3-03 / R5 T3: runtime/command 此前只有黑名单（shell hook、注册表
// 引用），没有 handler/listener 类型白名单——file（任意目录 FileServer）、
// sshd、tun 等高危 GOST 类型被校验层接受并落库为期望状态。
func TestPeerResourceCommandRejectsDangerousServiceTypes(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 31000, 31099)

	dangerous := []struct {
		name    string
		handler map[string]interface{}
	}{
		{"file", map[string]interface{}{"type": "file", "metadata": map[string]interface{}{"dir": "/tmp/audit132-fed-canary"}}},
		{"sshd", map[string]interface{}{"type": "sshd"}},
		{"tun", map[string]interface{}{"type": "tun"}},
		{"dns", map[string]interface{}{"type": "dns"}},
		{"redirect", map[string]interface{}{"type": "redirect"}},
	}
	for _, tc := range dangerous {
		payload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/command", share.Token, federationRuntimeCommandRequest{
			CommandType: "AddService",
			Data: []interface{}{
				map[string]interface{}{
					"name":     "r5-" + tc.name,
					"addr":     ":31001",
					"handler":  tc.handler,
					"listener": map[string]interface{}{"type": "tcp"},
				},
			},
		}, h.federationRuntimeCommand)
		if payload.Code == 0 {
			t.Fatalf("handler type %q must be rejected, got success", tc.name)
		}
		if !strings.Contains(payload.Msg, "not allowed") {
			t.Fatalf("handler type %q: expected whitelist rejection, got %q", tc.name, payload.Msg)
		}
	}

	rows := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share_resource WHERE share_id = ?`, share.ID)
	if rows != 0 {
		t.Fatalf("dangerous service configs must not persist desired state, found %d rows", rows)
	}

	// 危险 listener 类型同样必须拒（handler 合法也不能放行）。
	payload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/command", share.Token, federationRuntimeCommandRequest{
		CommandType: "AddService",
		Data: []interface{}{
			map[string]interface{}{
				"name":     "r5-bad-listener",
				"addr":     ":31002",
				"handler":  map[string]interface{}{"type": "tcp"},
				"listener": map[string]interface{}{"type": "tun"},
			},
		},
	}, h.federationRuntimeCommand)
	if payload.Code == 0 || !strings.Contains(payload.Msg, "not allowed") {
		t.Fatalf("listener type tun must be rejected by whitelist, got code=%d msg=%q", payload.Code, payload.Msg)
	}
}

// D3-03 正对照：面板自身会下发的服务/链类型必须通过校验层
// （tcp/udp 转发、relay 隧道服务、relay 链 + 隧道协议 dialer）。
func TestPeerResourceCommandAcceptsPanelServiceTypes(t *testing.T) {
	h, _, share := fix132ShareFixture(t, 31000, 31099)

	legitServices := []map[string]interface{}{
		{
			"name":     "fwd_tcp",
			"addr":     "[::]:31001",
			"handler":  map[string]interface{}{"type": "tcp"},
			"listener": map[string]interface{}{"type": "tcp"},
			"forwarder": map[string]interface{}{
				"nodes": []interface{}{map[string]interface{}{"name": "node_1", "addr": "1.1.1.1:443"}},
			},
		},
		{
			"name":     "tunnel_1",
			"addr":     "[::]:31002",
			"handler":  map[string]interface{}{"type": "relay"},
			"listener": map[string]interface{}{"type": "tls"},
		},
	}
	if _, err := h.preparePeerResourceCommand(share, "AddService", legitServices); err != nil {
		t.Fatalf("panel-generated service types must pass validation: %v", err)
	}

	chainData := map[string]interface{}{
		"chain": "chains_9",
		"data": map[string]interface{}{
			"name": "chains_9",
			"hops": []interface{}{
				map[string]interface{}{
					"name": "hop_9",
					"nodes": []interface{}{
						map[string]interface{}{
							"name":      "node_1",
							"addr":      "203.0.113.9:31003",
							"connector": map[string]interface{}{"type": "relay"},
							"dialer":    map[string]interface{}{"type": "tls"},
						},
					},
				},
			},
		},
	}
	if _, err := h.preparePeerResourceCommand(share, "AddChains", chainData); err != nil {
		t.Fatalf("panel-generated chain types must pass validation: %v", err)
	}

	badChain := map[string]interface{}{
		"name": "chains_bad",
		"hops": []interface{}{
			map[string]interface{}{
				"name": "hop_1",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":      "node_1",
						"addr":      "203.0.113.9:31004",
						"connector": map[string]interface{}{"type": "ss"},
						"dialer":    map[string]interface{}{"type": "tls"},
					},
				},
			},
		},
	}
	if _, err := h.preparePeerResourceCommand(share, "AddChains", badChain); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("chain connector type ss must be rejected by whitelist, got: %v", err)
	}
}
