package handler

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// E-01（P2）：replaceTunnelChainsTx 对远端（联邦）出口/中转节点无端口
// 时也分配本机随机端口——无端口段分享误报"节点端口已满"（联邦从未
// 被联系），有端口段分享则把未预留的随机端口写进链行。修复：出口/
// 中转两支加 IsRemoteNodeTx 守卫（照抄 prepareTunnelCreateState
// :5578/:5617），远端节点端口留 0/NULL；本机节点未填端口仍随机分配。
// 先红后绿：修复前三项远端断言失败，修复后通过；本地对照组始终通过。

func e01InsertNode(t *testing.T, r *repo.Repository, name, portSpec string, isRemote int) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, name, name+"-secret", "10.90.80.71", "10.90.80.71", "", portSpec, "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, isRemote, "", "", "").Error; err != nil {
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

func e01Fixture(t *testing.T) (*Handler, *repo.Repository, int64, int64, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/e01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-jwt-secret")
	localID := e01InsertNode(t, r, "e01-local", "30000-30010", 0)
	remoteNoRangeID := e01InsertNode(t, r, "e01-remote-norange", "0", 1)
	remoteRangeID := e01InsertNode(t, r, "e01-remote-range", "10000-20000", 1)
	return h, r, localID, remoteNoRangeID, remoteRangeID
}

func TestE01ReplaceChainsRemoteOutNoRangeLeavesNull(t *testing.T) {
	// 联邦分享无端口段 + 远端出口未填端口：修复前报错"节点端口已满，
	// 无可用端口"（红）；修复后通过且链行端口为 NULL（绿）。
	h, r, localID, remoteNoRangeID, _ := e01Fixture(t)
	tx := r.BeginTx()
	req := map[string]interface{}{
		"inNodeId":  []interface{}{map[string]interface{}{"nodeId": localID}},
		"outNodeId": []interface{}{map[string]interface{}{"nodeId": remoteNoRangeID}},
	}
	if err := h.replaceTunnelChainsTx(tx, 1, req); err != nil {
		_ = tx.Rollback()
		t.Fatalf("remote exit without port range must not fail at commit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	if port := e01ChainPort(t, r, 1, "3", remoteNoRangeID); port.Valid {
		t.Fatalf("remote exit port must be NULL until federation reserves one, got %d", port.Int64)
	}
}

func TestE01ReplaceChainsRemoteOutWithRangeLeavesNull(t *testing.T) {
	// 联邦分享有端口段 + 远端出口未填端口：修复前链行写随机端口
	// （红）；修复后为 NULL（绿）。入口用本机节点，出口用有端口段
	// 的远端节点。
	h, r, localID, _, remoteRangeID := e01Fixture(t)
	tx := r.BeginTx()
	req := map[string]interface{}{
		"inNodeId":  []interface{}{map[string]interface{}{"nodeId": localID}},
		"outNodeId": []interface{}{map[string]interface{}{"nodeId": remoteRangeID}},
	}
	if err := h.replaceTunnelChainsTx(tx, 1, req); err != nil {
		_ = tx.Rollback()
		t.Fatalf("remote exit with port range must not fail at commit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	if port := e01ChainPort(t, r, 1, "3", remoteRangeID); port.Valid {
		t.Fatalf("remote exit port must be NULL until federation reserves one, got %d", port.Int64)
	}
}

func TestE01ReplaceChainsRemoteChainWithRangeLeavesNull(t *testing.T) {
	// 远端中转未填端口：修复前链行写随机端口（红）；修复后为 NULL（绿）。
	// 入口与出口用本机节点，链节点用有端口段的远端节点。
	h, r, localID, _, remoteRangeID := e01Fixture(t)
	tx := r.BeginTx()
	req := map[string]interface{}{
		"inNodeId":  []interface{}{map[string]interface{}{"nodeId": localID}},
		"outNodeId": []interface{}{map[string]interface{}{"nodeId": localID, "port": 30005}},
		"chainNodes": []interface{}{
			[]interface{}{map[string]interface{}{"nodeId": remoteRangeID}},
		},
	}
	if err := h.replaceTunnelChainsTx(tx, 1, req); err != nil {
		_ = tx.Rollback()
		t.Fatalf("remote relay without port must not fail at commit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	if port := e01ChainPort(t, r, 1, "2", remoteRangeID); port.Valid {
		t.Fatalf("remote relay port must be NULL until federation reserves one, got %d", port.Int64)
	}
	// 同单出口端口显式指定时原样落库（未动面）。
	if port := e01ChainPort(t, r, 1, "3", localID); !port.Valid || port.Int64 != 30005 {
		t.Fatalf("explicit local exit port must be kept, got valid=%v value=%d", port.Valid, port.Int64)
	}
}

func TestE01ReplaceChainsLocalNodeStillPicks(t *testing.T) {
	// 对照组：本机节点未填端口仍随机分配且落在节点端口段内（修复前后均绿）。
	h, r, localID, _, _ := e01Fixture(t)
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
