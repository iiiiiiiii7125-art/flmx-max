package contract_test

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// F-074 (audit FINAL §5-B8 / A3-007): pickTunnelPort 在入口节点端口范围
// 无交集时静默回退虚构的 10000，下游再逐节点报"超出范围/已被占用"，
// 掩盖真实原因（无公共可用端口），甚至在 10000 恰好可用时歪打正着。
// 修复前本测试失败：错误信息不含"无公共可用端口"。
func TestForwardCreateNoCommonPortF074(t *testing.T) {
	secret := "unit" + "-test-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	now := time.Now().UnixMilli()

	// 双入口隧道，两节点端口范围互不相交。
	tunnelID, _ := seedTunnelDeleteTunnelWithNode(t, r, now, "f074-tunnel", "f074-node-a", "10000-10001")
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES('f074-node-b', 'f074-node-b-secret', '10.0.0.2', '10.0.0.2', '', '20000-20001', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)
	`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeB := mustLastInsertID(t, r, "f074-node-b")
	if err := r.DB().Exec(`
		INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(?, 1, ?, 0, 'round', 2, 'tls')
	`, tunnelID, nodeB).Error; err != nil {
		t.Fatal(err)
	}

	out := requestContractEnvelope(t, router, admin, "/api/v1/forward/create",
		map[string]interface{}{"tunnelId": tunnelID, "name": "f074-fwd", "remoteAddr": "203.0.113.5:80"})
	if out.Code == 0 {
		t.Fatalf("create must fail when entry nodes share no port, got %+v", out)
	}
	if !strings.Contains(out.Msg, "无公共可用端口") {
		t.Fatalf("error must name the real cause (no common available port), got %q", out.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM forward WHERE name = 'f074-fwd'`); n != 0 {
		t.Fatalf("no forward row may be created, got %d", n)
	}

	// 正例：单入口隧道自动选口仍可用且落在范围内。
	soloID, _ := seedTunnelDeleteTunnelWithNode(t, r, now, "f074-solo", "f074-node-solo", "21000-21010")
	out = requestContractEnvelope(t, router, admin, "/api/v1/forward/create",
		map[string]interface{}{"tunnelId": soloID, "name": "f074-ok", "remoteAddr": "203.0.113.5:80"})
	if out.Code != 0 {
		t.Fatalf("single-entry auto-pick must still work: %+v", out)
	}
	p := mustQueryInt(t, r, `SELECT port FROM forward_port WHERE forward_id IN (SELECT id FROM forward WHERE name = 'f074-ok')`)
	if p < 21000 || p > 21010 {
		t.Fatalf("auto-picked port must be inside the node range, got %d", p)
	}
}
