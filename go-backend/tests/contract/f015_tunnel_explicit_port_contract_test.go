package contract_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// F-015 (B4): type-2 tunnel exit/chain ports given explicitly used to
// skip every local check — validateRemoteNodePort is a no-op for local
// nodes, so out-of-range, >65535, and cross-table-colliding ports were
// stored and only failed at runtime bind. prepareTunnelCreateState now
// validates explicit ports (range + node spec + three-table occupancy).
func TestTunnelExplicitPortValidationContract(t *testing.T) {
	secret := "f015-secret"
	router, repo := setupContractRouter(t, secret)
	now := time.Now().UnixMilli()
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	insertNode := func(name, ip, portRange string) int64 {
		t.Helper()
		if err := repo.DB().Exec(`
			INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
			VALUES(?, ?, ?, ?, '', ?, '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
		`, name, name+"-secret", ip, ip, portRange, now, now).Error; err != nil {
			t.Fatalf("insert node %s: %v", name, err)
		}
		return mustLastInsertID(t, repo, name)
	}
	entryID := insertNode("f015-entry", "10.30.0.1", "30000-30010")
	chainID := insertNode("f015-chain", "10.30.0.2", "31000-31010")
	exitID := insertNode("f015-exit", "10.30.0.3", "32000-32010")

	create := func(name string, chainPort, outPort int) (int, string) {
		payload := `{"name":"` + name + `","type":2,"flow":1,"status":1,` +
			`"inNodeId":[{"nodeId":` + jsonInt64(entryID) + `,"protocol":"tls"}],` +
			`"chainNodes":[[{"nodeId":` + jsonInt64(chainID) + `,"protocol":"tls","strategy":"round","port":` + jsonInt64(int64(chainPort)) + `}]],` +
			`"outNodeId":[{"nodeId":` + jsonInt64(exitID) + `,"protocol":"tls","port":` + jsonInt64(int64(outPort)) + `}]}`
		out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/create", json.RawMessage(payload))
		return out.Code, out.Msg
	}

	// 1) 出口显式口 99999（>65535）：必须拒绝。
	if code, msg := create("f015-bad-range", 0, 99999); code == 0 || !strings.Contains(msg, "超出有效范围") {
		t.Fatalf("F-015: out port 99999 must be rejected as out of range, got code=%d msg=%q", code, msg)
	}
	// 2) 出口显式口在 1-65535 内但超出节点规格（32011 > 32010）：拒绝。
	if code, msg := create("f015-out-of-spec", 0, 32011); code == 0 || !strings.Contains(msg, "允许范围") {
		t.Fatalf("F-015: out port outside node spec must be rejected, got code=%d msg=%q", code, msg)
	}
	// 3) 出口显式口与已有转发入口同节点同口：拒绝（跨表占用）。
	if err := repo.DB().Exec(`
		INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(1, 'admin', 'f015-occupier', 0, '1.1.1.1:80', 'fifo', 0, 0, ?, ?, 1, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	occupierID := mustLastInsertID(t, repo, "f015-occupier")
	if err := repo.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, ?, 32005)`, occupierID, exitID).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}
	if code, msg := create("f015-collide-forward", 0, 32005); code == 0 || !strings.Contains(msg, "已被占用") {
		t.Fatalf("F-015: out port colliding with a forward must be rejected, got code=%d msg=%q", code, msg)
	}
	// 4) 链式显式口与入站同节点同口：拒绝。
	if err := repo.DB().Exec(`
		INSERT INTO node_inbound(node_id, kernel_id, name, protocol, port, created_time, updated_time)
		VALUES(?, 0, 'f015-ib', 'vless', 31005, ?, ?)
	`, chainID, now, now).Error; err != nil {
		t.Fatalf("insert inbound: %v", err)
	}
	if code, msg := create("f015-collide-inbound", 31005, 0); code == 0 || !strings.Contains(msg, "已被占用") {
		t.Fatalf("F-015: chain port colliding with an inbound must be rejected, got code=%d msg=%q", code, msg)
	}
	// 5) 合法显式口（规格内、未占用）：创建成功（防误杀）。
	if code, msg := create("f015-ok", 31006, 32007); code != 0 {
		t.Fatalf("valid explicit ports must be accepted, got code=%d msg=%q", code, msg)
	}
	if got := mustQueryInt(t, repo, `SELECT COUNT(*) FROM tunnel WHERE name = 'f015-ok'`); got != 1 {
		t.Fatalf("valid tunnel must persist, got %d", got)
	}
	// 被拒的创建不得留下隧道行。
	if got := mustQueryInt(t, repo, `SELECT COUNT(*) FROM tunnel WHERE name LIKE 'f015-bad%' OR name LIKE 'f015-out%' OR name LIKE 'f015-collide%'`); got != 0 {
		t.Fatalf("rejected creates must not persist tunnels, got %d", got)
	}
}
