package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"go-backend/internal/store/repo"
)

// R18-R3-B7-01: a DB that already ran the v6 federation-removal
// migration (version recorded, so the migration itself skips on
// upgrade) must still flag its orphaned Type 2 tunnels in
// tunnelList: a Type 2 tunnel with no live entry segment — zero
// chain_tunnel rows, or an entry row pointing at a node that no
// longer exists — is derived as orphaned at list time. The
// derivation must not write the todo key (list stays read-only:
// only fill-missing, never rewrite the whole table per call).
func TestR18R3B701AlreadyMigratedDBFlagsZeroSegmentType2Tunnel(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3-b701-already-migrated.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	db := r.DB()

	// Precondition: this is an already-migrated DB — Open recorded
	// the v6 version, so migrateV6FederationRemoval would skip.
	var versionCount int64
	if err := db.Table("schema_migration").Where("version = ?", "rmshare_v6_federation_removal").Count(&versionCount).Error; err != nil {
		t.Fatalf("count v6 version records: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("v6 version records = %d, want 1 (already-migrated DB)", versionCount)
	}

	if err := db.Exec("INSERT INTO node (name, secret, server_ip, port, created_time, updated_time, status) VALUES ('b701-local', 's-local', '10.0.0.1', '1000-2000', 1, 1, 1)").Error; err != nil {
		t.Fatalf("seed local node: %v", err)
	}
	var localNodeID int64
	if err := db.Table("node").Where("name = ?", "b701-local").Pluck("id", &localNodeID).Error; err != nil {
		t.Fatalf("read local node id: %v", err)
	}

	seedTunnel := func(name string, typ int) int64 {
		t.Helper()
		if err := db.Exec("INSERT INTO tunnel (name, type, flow, status, created_time, updated_time) VALUES (?, ?, 0, 1, 1, 1)", name, typ).Error; err != nil {
			t.Fatalf("seed tunnel %s: %v", name, err)
		}
		var id int64
		if err := db.Table("tunnel").Where("name = ?", name).Pluck("id", &id).Error; err != nil {
			t.Fatalf("read tunnel id %s: %v", name, err)
		}
		return id
	}
	zeroSegID := seedTunnel("b701-zero-seg", 2)
	danglingID := seedTunnel("b701-dangling-entry", 2)
	healthyID := seedTunnel("b701-healthy", 2)
	type1ID := seedTunnel("b701-type1-no-seg", 1)

	// Entry row pointing at a node that no longer exists.
	if err := db.Exec("INSERT INTO chain_tunnel (tunnel_id, chain_type, node_id, inx) VALUES (?, '1', 999999, 1)", danglingID).Error; err != nil {
		t.Fatalf("seed dangling chain row: %v", err)
	}
	// Healthy Type 2 tunnel: entry segment points at a live node.
	if err := db.Exec("INSERT INTO chain_tunnel (tunnel_id, chain_type, node_id, inx) VALUES (?, '1', ?, 1)", healthyID, localNodeID).Error; err != nil {
		t.Fatalf("seed healthy chain row: %v", err)
	}

	rec := httptest.NewRecorder()
	h.tunnelList(rec, httptest.NewRequest("POST", "/api/v1/tunnel/list", nil))
	var env struct {
		Code int                      `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode tunnel list: %v (body=%s)", err, rec.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("tunnel list must succeed, got code=%d body=%s", env.Code, rec.Body.String())
	}
	flagged := map[int64]bool{}
	for _, item := range env.Data {
		if id, ok := item["id"].(float64); ok {
			flagged[int64(id)] = item["orphanedByFederationRemoval"] == true
		}
	}
	if !flagged[zeroSegID] {
		t.Errorf("zero-segment Type 2 tunnel %d must be flagged orphanedByFederationRemoval, got %+v", zeroSegID, env.Data)
	}
	if !flagged[danglingID] {
		t.Errorf("Type 2 tunnel %d with a dangling entry node must be flagged orphanedByFederationRemoval, got %+v", danglingID, env.Data)
	}
	if flagged[healthyID] {
		t.Errorf("healthy Type 2 tunnel %d must NOT be flagged orphanedByFederationRemoval, got %+v", healthyID, env.Data)
	}
	if flagged[type1ID] {
		t.Errorf("Type 1 tunnel %d must NOT be flagged orphanedByFederationRemoval, got %+v", type1ID, env.Data)
	}

	// List-time derivation must not write the todo key: a second
	// list (or any list) leaves the key absent.
	cfg, err := r.GetConfigByName(repo.RMShareOrphanTunnelsKey)
	if err != nil || cfg != nil {
		t.Fatalf("tunnelList must not write the orphan-todo key, got %+v err=%v", cfg, err)
	}
}
