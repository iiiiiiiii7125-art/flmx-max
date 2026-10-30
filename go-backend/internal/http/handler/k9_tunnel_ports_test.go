package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// Round 2 K9：
//   - B06-001：隧道 create/update 对 type/status 补合法集校验
//     （{1,2} / {0,1}），与 flow/trafficRatio/分组 status 同口径点名
//     拒绝、不落库；update 按 presence 语义只校验在场字段。
//   - P01-002：非入口段（链路段+出口段）更新缺省口改为「现口仍合法
//     则沿用」——改名等无关编辑不再静默漂移口；现口被占/越界才按
//     创建的随机口径重挑，并经回执 warnings 提示。
//   - P01-001 联邦撞口三面：本地自动选口、显式指定、入站创建都必须
//     看见 peer_share_runtime 有效预留口并避开/拒绝。

func seedK9Node(t *testing.T, r *repo.Repository, id int64, name, ip, portSpec string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, '', ?, '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, id, name, name+"-secret", ip, ip, portSpec, now, now).Error; err != nil {
		t.Fatalf("insert node %s: %v", name, err)
	}
}

func setupK9Handler(t *testing.T) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/k9.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	seedK9Node(t, r, 10, "entry-a", "10.0.0.1", "30000-30010")
	seedK9Node(t, r, 11, "exit-a", "10.0.0.2", "35000-35010")
	seedK9Node(t, r, 12, "chain-a", "10.0.0.3", "36000-36010")
	seedK9Node(t, r, 13, "fed-a", "10.0.0.4", "37000-37001")
	return h
}

func seedK9PeerReservation(t *testing.T, h *Handler, nodeID int64, port int) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO peer_share_runtime(share_id, node_id, reservation_id, resource_key, binding_id, role, chain_name, service_name, protocol, strategy, port, target, applied, release_pending, status, created_time, updated_time)
		VALUES(5, ?, ?, ?, '', 'exit', '', '', 'tls', 'round', ?, '', 1, 0, 1, ?, ?)
	`, nodeID, "resv-k9", "rk-k9", port, now, now).Error; err != nil {
		t.Fatalf("insert peer reservation: %v", err)
	}
}

// seedK9Type2Tunnel 落库一条 type=2 隧道：入口 node10、链路 node12:chainPort、
// 出口 node11:exitPort（P01 动态实证 TUPD 的同形夹具）。
func seedK9Type2Tunnel(t *testing.T, h *Handler, id int64, name string, chainPort, exitPort int) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(?, ?, 1, 2, 'tls', 1, ?, ?, 1, ?, '', '', 0)
	`, id, name, now, now, id).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	rows := []struct {
		chainType string
		nodeID    int64
		port      interface{}
	}{
		{"1", 10, nil},
		{"2", 12, chainPort},
		{"3", 11, exitPort},
	}
	for _, row := range rows {
		if err := h.repo.DB().Exec(`
			INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
			VALUES(?, ?, ?, ?, 'round', 1, 'tls')
		`, id, row.chainType, row.nodeID, row.port).Error; err != nil {
			t.Fatalf("insert chain row: %v", err)
		}
	}
}

type k9Envelope struct {
	Code int                    `json:"code"`
	Msg  string                 `json:"msg"`
	Data map[string]interface{} `json:"data"`
}

func k9Post(t *testing.T, h *Handler, fn func(http.ResponseWriter, *http.Request), url string, payload map[string]interface{}) k9Envelope {
	t.Helper()
	res := httptest.NewRecorder()
	fn(res, newAuthenticatedJSONRequest(t, payload))
	var env k9Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func k9ChainPort(t *testing.T, h *Handler, tunnelID int64, chainType string) int {
	t.Helper()
	var port int
	if err := h.repo.DB().Raw(`SELECT COALESCE(port, 0) FROM chain_tunnel WHERE tunnel_id = ? AND chain_type = ?`, tunnelID, chainType).Scan(&port).Error; err != nil {
		t.Fatalf("read chain port: %v", err)
	}
	return port
}

func k9TunnelRow(t *testing.T, h *Handler, name string) (typeVal, status, count int) {
	t.Helper()
	if err := h.repo.DB().Raw(`SELECT COUNT(*) FROM tunnel WHERE name = ?`, name).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return 0, 0, 0
	}
	var row struct {
		Type   int
		Status int
	}
	if err := h.repo.DB().Raw(`SELECT type, status FROM tunnel WHERE name = ?`, name).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Type, row.Status, count
}

func TestK9TunnelCreateRejectsOutOfSetTypeStatus(t *testing.T) {
	cases := []struct {
		name    string
		fields  map[string]interface{}
		wantMsg string
	}{
		{"type-0", map[string]interface{}{"type": 0}, "隧道类型"},
		{"type-3", map[string]interface{}{"type": 3}, "隧道类型"},
		{"type-9", map[string]interface{}{"type": 9}, "隧道类型"},
		{"type-neg", map[string]interface{}{"type": -1}, "隧道类型"},
		{"status-neg", map[string]interface{}{"status": -1}, "隧道状态"},
		{"status-2", map[string]interface{}{"status": 2}, "隧道状态"},
		{"status-7", map[string]interface{}{"status": 7}, "隧道状态"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupK9Handler(t)
			name := "k9-bad-" + tc.name
			payload := map[string]interface{}{
				"name": name, "type": 1, "status": 1,
				"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
			}
			for k, v := range tc.fields {
				payload[k] = v
			}
			env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", payload)
			if env.Code == 0 {
				t.Fatalf("B06-001: out-of-set create must be rejected, got success")
			}
			if !strings.Contains(env.Msg, tc.wantMsg) {
				t.Fatalf("rejection must name the field (%s), got msg=%q", tc.wantMsg, env.Msg)
			}
			if _, _, count := k9TunnelRow(t, h, name); count != 0 {
				t.Fatalf("rejected create must not persist, got %d rows", count)
			}
		})
	}
}

func TestK9TunnelCreateAcceptsLegalTypeStatus(t *testing.T) {
	h := setupK9Handler(t)
	env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "k9-ok", "type": 1, "status": 0,
		"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("legal create must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	typeVal, status, count := k9TunnelRow(t, h, "k9-ok")
	if count != 1 || typeVal != 1 || status != 0 {
		t.Fatalf("stored = type %d status %d count %d, want 1/0/1", typeVal, status, count)
	}
}

func TestK9TunnelUpdateRejectsOutOfSetTypeStatus(t *testing.T) {
	cases := []struct {
		name    string
		fields  map[string]interface{}
		wantMsg string
	}{
		{"type-3", map[string]interface{}{"type": 3}, "隧道类型"},
		{"type-0", map[string]interface{}{"type": 0}, "隧道类型"},
		{"status-7", map[string]interface{}{"status": 7}, "隧道状态"},
		{"status-neg", map[string]interface{}{"status": -1}, "隧道状态"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := setupK9Handler(t)
			seedProbeTargetTunnel(t, h, 77, "k9-upd", "", 0)
			payload := map[string]interface{}{
				"id": 77, "name": "k9-upd",
				"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
			}
			for k, v := range tc.fields {
				payload[k] = v
			}
			env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", payload)
			if env.Code == 0 {
				t.Fatalf("B06-001: out-of-set update must be rejected, got success")
			}
			if !strings.Contains(env.Msg, tc.wantMsg) {
				t.Fatalf("rejection must name the field (%s), got msg=%q", tc.wantMsg, env.Msg)
			}
			typeVal, status, _ := k9TunnelRow(t, h, "k9-upd")
			if typeVal != 1 || status != 1 {
				t.Fatalf("rejected update must not persist, stored type=%d status=%d", typeVal, status)
			}
		})
	}
}

func TestK9TunnelUpdateAcceptsLegalStatusToggle(t *testing.T) {
	h := setupK9Handler(t)
	seedProbeTargetTunnel(t, h, 77, "k9-upd-ok", "", 0)
	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", map[string]interface{}{
		"id": 77, "name": "k9-upd-ok", "status": 0,
		"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("legal update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	_, status, _ := k9TunnelRow(t, h, "k9-upd-ok")
	if status != 0 {
		t.Fatalf("status toggle must persist, got %d", status)
	}
}

// P01-002 主场景（复刻 P01 的 TUPD 漂移实证）：隧道显式建于出口
// 35009/链路 36008，仅改名的 UI 式更新（不带 port）后两口必须原样
// 沿用、回执无换口提示。修复前此处两口会被顺序重挑为 35000/36000。
func TestK9TunnelRenameKeepsNonEntryPorts(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Type2Tunnel(t, h, 88, "k9-keep", 36008, 35009)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", map[string]interface{}{
		"id": 88, "name": "k9-keep-renamed", "type": 2, "status": 1, "flow": 1, "trafficRatio": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("rename update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := k9ChainPort(t, h, 88, "3"); got != 35009 {
		t.Fatalf("P01-002: exit port drifted on rename: got %d, want 35009 kept", got)
	}
	if got := k9ChainPort(t, h, 88, "2"); got != 36008 {
		t.Fatalf("P01-002: chain port drifted on rename: got %d, want 36008 kept", got)
	}
	if warnings, ok := env.Data["warnings"]; ok {
		if s, _ := warnings.([]interface{}); len(s) > 0 {
			t.Fatalf("P01-002: no port change happened, receipt must not warn, got %v", warnings)
		}
	}
}

// P01-002 重挑场景：现口被联邦有效预留抢占（存量撞口态）时，更新
// 必须把该段换到未占用口，并经 warnings 点名新旧口；未撞口的链路
// 段保持不动。
func TestK9TunnelUpdateRepicksPortTakenByPeerReservation(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Type2Tunnel(t, h, 89, "k9-repick", 36008, 35009)
	seedK9PeerReservation(t, h, 11, 35009)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", map[string]interface{}{
		"id": 89, "name": "k9-repick-renamed", "type": 2, "status": 1, "flow": 1, "trafficRatio": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("repick update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	newExit := k9ChainPort(t, h, 89, "3")
	if newExit == 35009 || newExit < 35000 || newExit > 35010 {
		t.Fatalf("P01-002: occupied exit port must be re-picked inside the node spec, got %d", newExit)
	}
	if got := k9ChainPort(t, h, 89, "2"); got != 36008 {
		t.Fatalf("P01-002: untouched chain port must stay 36008, got %d", got)
	}
	warnings, _ := env.Data["warnings"].([]interface{})
	joined := ""
	for _, w := range warnings {
		if s, ok := w.(string); ok {
			joined += s + ";"
		}
	}
	if !strings.Contains(joined, "35009") || !strings.Contains(joined, "变更为") {
		t.Fatalf("P01-002: port change must surface in receipt warnings naming old/new ports, got %q", joined)
	}
}

// P01-001 联邦撞口三面（本地侧）：
//  1. 自动选口：节点 13 规格仅 37000-37001，37000 被有效预留占 →
//     创建 type=2 隧道出口必须落 37001。
//  2. 显式指定：显式 port=37000 创建必须被拒（已被占用）。
//  3. 入站面：inboundPortConflict 对仅被预留占用的口必须报告冲突。
func TestK9TunnelCreateAvoidsPeerReservation(t *testing.T) {
	h := setupK9Handler(t)
	seedK9PeerReservation(t, h, 13, 37000)

	env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "k9-fed-auto", "type": 2, "status": 1,
		"inNodeId":  []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": 13, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("create must succeed on the free port, code=%d msg=%s", env.Code, env.Msg)
	}
	var tunnelID int64
	if err := h.repo.DB().Raw(`SELECT id FROM tunnel WHERE name = ?`, "k9-fed-auto").Scan(&tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if got := k9ChainPort(t, h, tunnelID, "3"); got != 37001 {
		t.Fatalf("P01-001: auto-pick must avoid the peer-reserved 37000, got exit port %d", got)
	}

	env = k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "k9-fed-explicit", "type": 2, "status": 1,
		"inNodeId":  []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"outNodeId": []map[string]interface{}{{"nodeId": 13, "protocol": "tls", "port": 37000}},
	})
	if env.Code == 0 || !strings.Contains(env.Msg, "已被占用") {
		t.Fatalf("P01-001: explicit port on a peer reservation must be rejected as occupied, got code=%d msg=%q", env.Code, env.Msg)
	}
	if _, _, count := k9TunnelRow(t, h, "k9-fed-explicit"); count != 0 {
		t.Fatal("rejected create must not persist")
	}
}

func TestK9InboundConflictSeesPeerReservation(t *testing.T) {
	h := setupK9Handler(t)
	seedK9PeerReservation(t, h, 13, 37000)
	msg, err := h.inboundPortConflict(13, kernel.Inbound{Port: 37000}, 0)
	if err != nil {
		t.Fatalf("inboundPortConflict: %v", err)
	}
	if !strings.Contains(msg, "联邦分享预留") {
		t.Fatalf("P01-001: inbound create must see the peer reservation on 37000, got conflict %q", msg)
	}
	msg, err = h.inboundPortConflict(13, kernel.Inbound{Port: 37001}, 0)
	if err != nil {
		t.Fatalf("inboundPortConflict free port: %v", err)
	}
	if msg != "" {
		t.Fatalf("free port 37001 must not conflict, got %q", msg)
	}
}
