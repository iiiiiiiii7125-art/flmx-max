package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// X-P3-23 (L-09-5 + L-S2): 事务尾巴统一。
// A) deleteTunnelByID 尾步 MarkPeerShareRuntimeReleasedByPort 纳入
// 级联主事务：注入尾步失败时整个删除回滚、隧道行保留（修复前尾步
// 在事务外，失败时隧道已删、runtime 未释放且无回滚）。
// B) 节点删除的联邦绑定 DB 清理在 DeleteNodeCascade 主事务内
// （断言绑定行随节点同删）。
func TestR1B8_XP323_TunnelDeleteTailInMainTx(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp323.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('xp323-node', 's323', '198.51.100.51', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	tx := r.DB()
	_ = tx
	tunnelID, err := func() (int64, error) {
		got, err := h.repo.CreateFederationTunnel("Share-9-Port-31009", 1, "tcp", now, nodeID, 31009)
		return got, err
	}()
	if err != nil {
		t.Fatalf("create federation tunnel: %v", err)
	}
	rt := &model.PeerShareRuntime{
		ShareID: 9, NodeID: nodeID, ReservationID: "res-323", ResourceKey: "rk-323",
		Port: 31009, Status: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(rt).Error; err != nil {
		t.Fatalf("seed runtime: %v", err)
	}

	// 注入尾步失败：peer_share_runtime 的 UPDATE 被触发器中止。
	if err := r.DB().Exec(`CREATE TRIGGER fail_peer_release BEFORE UPDATE ON peer_share_runtime BEGIN SELECT RAISE(ABORT, 'injected tail failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.deleteTunnelByID(tunnelID); err == nil {
		t.Fatalf("尾步失败时 deleteTunnelByID 应报错")
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel WHERE id = ?`, tunnelID); n != 1 {
		t.Fatalf("尾步失败应回滚主事务、隧道保留, got rows=%d", n)
	}
	var rtStatus int
	if err := r.DB().Raw(`SELECT status FROM peer_share_runtime WHERE id = ?`, rt.ID).Row().Scan(&rtStatus); err != nil {
		t.Fatal(err)
	}
	if rtStatus != 1 {
		t.Fatalf("回滚后 runtime 应仍为 active, got %d", rtStatus)
	}

	// 去掉触发器：删除成功且 runtime 同步释放。
	if err := r.DB().Exec(`DROP TRIGGER fail_peer_release`).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.deleteTunnelByID(tunnelID); err != nil {
		t.Fatalf("正常删除应成功: %v", err)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM tunnel WHERE id = ?`, tunnelID); n != 0 {
		t.Fatalf("隧道应已删除, got %d", n)
	}
	if err := r.DB().Raw(`SELECT status FROM peer_share_runtime WHERE id = ?`, rt.ID).Row().Scan(&rtStatus); err != nil {
		t.Fatal(err)
	}
	if rtStatus != 0 {
		t.Fatalf("删除后 runtime 应被释放 (status=0), got %d", rtStatus)
	}
}

// X-P3-23 B: 节点删除时联邦绑定行随主事务一并删除。
func TestR1B8_XP323_NodeDeleteBindingsInCascadeTx(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp323b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, port, created_time, status) VALUES('xp323b-node', 's323b', '198.51.100.52', '1000-2000', ?, 1)`, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.UpsertFederationTunnelBinding(&repo.FederationTunnelBinding{
		TunnelID: 77, NodeID: nodeID, ChainType: 3, RemoteURL: "https://peer.example",
		ResourceKey: "tunnel:77", RemoteBindingID: "b-77", Status: 1,
		CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteNodeCascade(nodeID); err != nil {
		t.Fatalf("delete node cascade: %v", err)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM federation_tunnel_binding WHERE node_id = ?`, nodeID); n != 0 {
		t.Fatalf("节点删除后绑定行应随主事务删除, got %d", n)
	}
}
