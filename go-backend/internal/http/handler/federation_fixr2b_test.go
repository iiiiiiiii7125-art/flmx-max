package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/client"
	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// R2-F3-01 钉格（消费方凭据自愈）：提供方重新补发凭据后，消费方持
// 旧的非空凭据时——建隧道路径（ensureRemoteNodeRelayAuth）与节点状态
// 同步（syncRemoteNodeStatuses）都必须按 Connect 回执刷新落盘。修复
// 前两路均不刷新（ensure 仅密码为空时、状态同步从不落盘），本文件
// 两测均红。

func fixR2BProviderServer(t *testing.T, relayUser, relayPass, panelVersion string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"shareId":42,"shareName":"s","nodeId":7,"nodeName":"prov-node","serverIp":"10.0.0.5","status":1,"maxBandwidth":100,"currentFlow":5,"expiryTime":0,"portRangeStart":30000,"portRangeEnd":30100,"relayUsername":%q,"relayPassword":%q,"panelVersion":%q},"ts":0}`,
			relayUser, relayPass, panelVersion)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fixR2BRemoteNode(t *testing.T, r *repo.Repository, remoteURL, remoteConfig string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	name := fmt.Sprintf("fixr2b-remote-%d", now)
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, '10.0.0.1', '10.0.0.1', '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0, 1, ?, 'tok', ?)
	`, name, name+"-secret", now, now, remoteURL, remoteConfig).Error; err != nil {
		t.Fatalf("insert remote node: %v", err)
	}
	return mustLastInsertID(t, r, name)
}

func fixR2BStoredConfig(t *testing.T, r *repo.Repository, nodeID int64) map[string]interface{} {
	t.Helper()
	var raw string
	if err := r.DB().Raw(`SELECT remote_config FROM node WHERE id = ?`, nodeID).Scan(&raw).Error; err != nil {
		t.Fatalf("read remote_config: %v", err)
	}
	cfg := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("stored remote_config is not JSON: %v (%q)", err, raw)
	}
	return cfg
}

func TestFixR2BEnsureRelayAuthRefreshesRotatedCredentials(t *testing.T) {
	DisableSafeRemoteAddrCheckForTesting = true
	defer func() { DisableSafeRemoteAddrCheckForTesting = false }()

	r, err := repo.Open(filepath.Join(t.TempDir(), "fixr2b-ensure.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")

	srv := fixR2BProviderServer(t, "share_user", "new-rotated-password", "1.9.6")
	oldConfig := `{"shareId":42,"maxBandwidth":100,"currentFlow":5,"expiryTime":0,"portRangeStart":30000,"portRangeEnd":30100,"relayUsername":"share_user","relayPassword":"old-stale-password","panelVersion":"1.9.5"}`
	nodeID := fixR2BRemoteNode(t, r, srv.URL, oldConfig)

	node := &nodeRecord{ID: nodeID, Name: "prov-node (Remote)", IsRemote: 1, RemoteURL: srv.URL, RemoteToken: "tok", RemoteConfig: oldConfig}
	h.ensureRemoteNodeRelayAuth(client.NewFederationClient(), node, "")

	if !strings.Contains(node.RemoteConfig, "new-rotated-password") {
		t.Fatalf("in-memory RemoteConfig must carry rotated credentials, got %s", node.RemoteConfig)
	}
	cfg := fixR2BStoredConfig(t, r, nodeID)
	if cfg["relayPassword"] != "new-rotated-password" {
		t.Fatalf("stored relayPassword = %v, want rotated value", cfg["relayPassword"])
	}
	if cfg["panelVersion"] != "1.9.6" {
		t.Fatalf("stored panelVersion = %v, want 1.9.6", cfg["panelVersion"])
	}
	// 合并口径：用量等其余键不得被凭据刷新抹掉。
	if cfg["shareId"] != float64(42) || cfg["portRangeStart"] != float64(30000) {
		t.Fatalf("merge must preserve other RemoteConfig keys, got %v", cfg)
	}
}

func TestFixR2BSyncStatusesRefreshesRotatedCredentials(t *testing.T) {
	DisableSafeRemoteAddrCheckForTesting = true
	defer func() { DisableSafeRemoteAddrCheckForTesting = false }()

	r, err := repo.Open(filepath.Join(t.TempDir(), "fixr2b-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")

	srv := fixR2BProviderServer(t, "share_user", "rotated-by-status-sync", "1.9.6")
	oldConfig := `{"shareId":42,"relayUsername":"share_user","relayPassword":"stale-password","panelVersion":"1.9.5"}`
	nodeID := fixR2BRemoteNode(t, r, srv.URL, oldConfig)

	items := []map[string]interface{}{{"id": nodeID, "isRemote": 1, "status": 0}}
	h.syncRemoteNodeStatuses(items)

	if items[0]["status"] != 1 {
		t.Fatalf("status sync must still surface provider status, got %v", items[0]["status"])
	}
	cfg := fixR2BStoredConfig(t, r, nodeID)
	if cfg["relayPassword"] != "rotated-by-status-sync" {
		t.Fatalf("status sync must persist rotated credentials, stored relayPassword = %v", cfg["relayPassword"])
	}
}

// R2-F3-03 钉格（回放收养）：提供方改名联邦隧道后，消费方同参重试
// 必须回放既有 tunnelId，而非落 403。修复前名称不等即判非回放，本
// 测红。
func TestFixR2BReplayAdoptsRenamedTunnel(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	create := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443"}

	first := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if first.Code != 0 {
		t.Fatalf("first create: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	tunnelID := r10TunnelID(t, first.Data)

	if err := r.DB().Exec(`UPDATE tunnel SET name = 'renamed-by-provider-admin' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("rename tunnel: %v", err)
	}

	second := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if second.Code != 0 {
		t.Fatalf("same-params retry after provider rename: expected replay code 0, got %d (%s)", second.Code, second.Msg)
	}
	if got := r10TunnelID(t, second.Data); got != tunnelID {
		t.Fatalf("retry tunnelId = %d, want adopted replay of %d", got, tunnelID)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); n != 1 {
		t.Fatalf("adopted tunnel rows = %d, want 1", n)
	}
}

// R2-F3-04 钉格（回放 Target 归一）：同义不同写（主机名大小写、端口
// 前导零）的同参重试必须回放；异目标仍 403。修复前逐字比较，本测红。
func TestFixR2BReplayNormalizesTarget(t *testing.T) {
	h, _, share := fix132ShareFixture(t, 20000, 20010)

	first := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
		federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "EXAMPLE.com:443"}, h.federationTunnelCreate)
	if first.Code != 0 {
		t.Fatalf("first create: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	tunnelID := r10TunnelID(t, first.Data)

	retry := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
		federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "example.com:443"}, h.federationTunnelCreate)
	if retry.Code != 0 {
		t.Fatalf("case-variant retry: expected replay code 0, got %d (%s)", retry.Code, retry.Msg)
	}
	if got := r10TunnelID(t, retry.Data); got != tunnelID {
		t.Fatalf("case-variant retry tunnelId = %d, want %d", got, tunnelID)
	}

	// 端口前导零同义：0443 与 443 是同一目标。
	first2 := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
		federationTunnelRequest{Protocol: "tcp", RemotePort: 20006, Target: "10.0.0.9:0443"}, h.federationTunnelCreate)
	if first2.Code != 0 {
		t.Fatalf("second create: expected code 0, got %d (%s)", first2.Code, first2.Msg)
	}
	tunnelID2 := r10TunnelID(t, first2.Data)
	retry2 := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
		federationTunnelRequest{Protocol: "tcp", RemotePort: 20006, Target: "10.0.0.9:443"}, h.federationTunnelCreate)
	if retry2.Code != 0 {
		t.Fatalf("leading-zero-port retry: expected replay code 0, got %d (%s)", retry2.Code, retry2.Msg)
	}
	if got := r10TunnelID(t, retry2.Data); got != tunnelID2 {
		t.Fatalf("leading-zero-port retry tunnelId = %d, want %d", got, tunnelID2)
	}

	// 归一不得放宽到异目标：换主机仍 403。
	denied := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
		federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "other.com:443"}, h.federationTunnelCreate)
	if denied.Code != 403 {
		t.Fatalf("different-host retry: expected 403, got %d (%s)", denied.Code, denied.Msg)
	}
}

// R2-F3-02 钉格（删共享清理）：改名后的联邦隧道也必须被删共享清理
// 命中（运行时 ResourceKey 反查）；清理失败的隧道必须在回执点名；
// 查列表失败必须回错误而非静默。修复前：改名隧道漏删（红）、失败回
// 执无披露（红）、清单失败静默（签名即 void）。
func TestFixR2BShareDeleteCleansRenamedTunnel(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	create := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443"}
	first := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if first.Code != 0 {
		t.Fatalf("create: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	tunnelID := r10TunnelID(t, first.Data)
	if err := r.DB().Exec(`UPDATE tunnel SET name = 'renamed-before-share-delete' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("rename tunnel: %v", err)
	}

	body, _ := json.Marshal(deletePeerShareRequest{ID: share.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/share/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.federationShareDelete(res, req)
	var payload response.R
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("share delete: expected code 0, got %d (%s)", payload.Code, payload.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); n != 0 {
		t.Fatalf("renamed federation tunnel must be cleaned with its share, %d rows remain", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share WHERE id = ?`, share.ID); n != 0 {
		t.Fatalf("share must be deleted, %d rows remain", n)
	}
}

func TestFixR2BShareDeleteDisclosesUncleanedTunnels(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	create := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443"}
	first := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if first.Code != 0 {
		t.Fatalf("create: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	tunnelID := r10TunnelID(t, first.Data)

	// 强制逐条删除失败：隧道行 DELETE 被触发器中止。
	if err := r.DB().Exec(`CREATE TRIGGER fixr2b_block_tunnel_delete BEFORE DELETE ON tunnel BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END;`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	body, _ := json.Marshal(deletePeerShareRequest{ID: share.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/share/delete", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	h.federationShareDelete(res, req)
	var payload response.R
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("share delete with one uncleaned tunnel: expected code 0 with disclosure, got %d (%s)", payload.Code, payload.Msg)
	}
	data, ok := payload.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("receipt must disclose uncleaned tunnels, data = %v", payload.Data)
	}
	ids, ok := data["uncleanedTunnelIds"].([]interface{})
	if !ok || len(ids) != 1 || int64(ids[0].(float64)) != tunnelID {
		t.Fatalf("uncleanedTunnelIds = %v, want [%d]", data["uncleanedTunnelIds"], tunnelID)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); n != 1 {
		t.Fatalf("blocked tunnel delete must leave the tunnel row, %d rows", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share WHERE id = ?`, share.ID); n != 0 {
		t.Fatalf("share itself must still be deleted, %d rows remain", n)
	}
}

func TestFixR2BCleanupFederationTunnelsListFailureIsAnError(t *testing.T) {
	_, r, share := fix132ShareFixture(t, 20000, 20010)
	h := New(r, "test-jwt-secret")
	if err := r.DB().Exec(`DROP TABLE tunnel`).Error; err != nil {
		t.Fatalf("drop tunnel table: %v", err)
	}
	failed, err := h.cleanupFederationTunnels(share.ID)
	if err == nil {
		t.Fatalf("list failure must surface as an error, got failed=%v err=nil", failed)
	}
}

// R2-F3-05 钉格（建隧道回滚）：运行时建行失败时回滚删除成功走既有
// 内部错误出口且不留隧道；回滚删除也失败时回执必须点名孤儿隧道 ID。
// 修复前回执恒为不点名的 "Internal error"，点名断言红。
func TestFixR2BTunnelCreateRollbackFailureNamesOrphan(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	if err := r.DB().Exec(`CREATE TRIGGER fixr2b_block_runtime_insert BEFORE INSERT ON peer_share_runtime BEGIN SELECT RAISE(ABORT, 'forced insert failure'); END;`).Error; err != nil {
		t.Fatalf("create insert trigger: %v", err)
	}
	create := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443"}

	// 回滚成功路径：内部错误出口、隧道不留。
	payload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if payload.Code == 0 {
		t.Fatalf("create with blocked runtime insert must fail, got code 0")
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE name = ?`, fmt.Sprintf("Share-%d-Port-%d", share.ID, 20005)); n != 0 {
		t.Fatalf("successful rollback must not leave a tunnel, %d rows", n)
	}

	// 回滚失败路径：隧道删除被中止 → 回执点名孤儿隧道 ID、行保留。
	if err := r.DB().Exec(`CREATE TRIGGER fixr2b_block_tunnel_delete BEFORE DELETE ON tunnel BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END;`).Error; err != nil {
		t.Fatalf("create delete trigger: %v", err)
	}
	payload = fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if payload.Code == 0 {
		t.Fatalf("create with blocked runtime insert and blocked rollback must fail, got code 0")
	}
	orphanID := int64(mustQueryInt(t, r, `SELECT id FROM tunnel WHERE name = ?`, fmt.Sprintf("Share-%d-Port-%d", share.ID, 20005)))
	if orphanID <= 0 {
		t.Fatalf("orphaned tunnel row must remain after failed rollback")
	}
	if !strings.Contains(payload.Msg, fmt.Sprintf("tunnel %d", orphanID)) {
		t.Fatalf("receipt must name the orphaned tunnel %d, got msg %q", orphanID, payload.Msg)
	}
}

// R2-F3-06 钉格（导入回执版本）：无版本警告时，初次导入与重复导入
// （刷新）回执都必须带 panelVersion。修复前初次导入回 OKEmpty、刷新
// 回执仅 refreshed/nodeId，本测红。
func TestFixR2BNodeImportReceiptAlwaysCarriesPanelVersion(t *testing.T) {
	DisableSafeRemoteAddrCheckForTesting = true
	defer func() { DisableSafeRemoteAddrCheckForTesting = false }()

	r, err := repo.Open(filepath.Join(t.TempDir(), "fixr2b-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")
	srv := fixR2BProviderServer(t, "share_user", "import-password", "1.9.6")

	importOnce := func() response.R {
		body, _ := json.Marshal(nodeImportRequest{RemoteURL: srv.URL, Token: "tok"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/node/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		h.nodeImport(res, req)
		var payload response.R
		if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return payload
	}

	first := importOnce()
	if first.Code != 0 {
		t.Fatalf("first import: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	data, ok := first.Data.(map[string]interface{})
	if !ok || data["panelVersion"] != "1.9.6" {
		t.Fatalf("first import receipt must carry panelVersion without a warning, data = %v", first.Data)
	}

	second := importOnce()
	if second.Code != 0 {
		t.Fatalf("repeat import: expected code 0, got %d (%s)", second.Code, second.Msg)
	}
	data, ok = second.Data.(map[string]interface{})
	if !ok || data["refreshed"] != true || data["panelVersion"] != "1.9.6" {
		t.Fatalf("refresh receipt must carry refreshed=true and panelVersion, data = %v", second.Data)
	}
}
