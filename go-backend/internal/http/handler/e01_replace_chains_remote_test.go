package handler

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// E-01（P2）：replaceTunnelChainsTx 对出口/中转节点无端口时的端口
// 分配口径。本机节点未填端口仍随机分配（本地对照组）。原三项远端
// （联邦）断言已随联邦功能删除一并摘除——远端节点概念不复存在，
// 链路端口恒由本机分配。

func e01InsertNode(t *testing.T, r *repo.Repository, name, portSpec string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, name, name+"-secret", "10.90.80.71", "10.90.80.71", "", portSpec, "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0).Error; err != nil {
		t.Fatalf("insert node %s: %v", name, err)
	}
	return mustLastInsertID(t, r, name)
}

func e01ChainPort(t *testing.T, r *repo.Repository, tunnelID int64, chainType string, nodeID int64) sql.NullInt64 {
	t.Helper()
	var port sql.NullInt64
	if err := r.DB().Raw(`SELECT port FROM chain_tunnel WHERE tunnel_id = ? AND chain_type = ? AND node_id = ?`,
		tunnelID, chainType, nodeID).Row().Scan(&port); err != nil {
		t.Fatalf("read chain port (tunnel=%d type=%s node=%d): %v", tunnelID, chainType, nodeID, err)
	}
	return port
}

func e01Fixture(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/e01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")
	localID := e01InsertNode(t, r, "e01-local", "30000-30010")
	return h, r, localID
}

func TestE01ReplaceChainsLocalNodeStillPicks(t *testing.T) {
	// 对照组：本机节点未填端口仍随机分配且落在节点端口段内（修复前后均绿）。
	h, r, localID := e01Fixture(t)
	tx := r.BeginTx()
	req := map[string]interface{}{
		"inNodeId":  []interface{}{map[string]interface{}{"nodeId": localID}},
		"outNodeId": []interface{}{map[string]interface{}{"nodeId": localID}},
	}
	if err := h.replaceTunnelChainsTx(tx, 1, req); err != nil {
		_ = tx.Rollback()
		t.Fatalf("local exit without port must still pick one: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	port := e01ChainPort(t, r, 1, "3", localID)
	if !port.Valid || port.Int64 < 30000 || port.Int64 > 30010 {
		t.Fatalf("local exit port must be randomly picked within 30000-30010, got valid=%v value=%d", port.Valid, port.Int64)
	}
}
