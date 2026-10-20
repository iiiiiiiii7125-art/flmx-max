package contract_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// F-014 (B4): tunnelUpdate previously ran its whole check-then-write
// sequence without any per-node port lock, while forwardCreate only
// checked outside its lock — two concurrent writers could both pass
// their cross-table occupancy checks and land the same (node, port) in
// chain_tunnel and forward_port (no cross-table DB constraint exists
// by design). tunnelUpdate now holds the sorted per-node locks across
// validate and commit phases with an in-lock re-check, and forward
// writes re-check inside CreateForwardTx's lock.
//
// This is an invariant guard: for every round, the two racing writes
// must never BOTH succeed with the contested port occupied twice in
// the DB. (Race reproduction is probabilistic; the guard asserts the
// invariant that must hold after the fix.)
func TestTunnelUpdateForwardCreateRaceInvariantContract(t *testing.T) {
	secret := "f014-secret"
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

	const rounds = 12
	for round := 0; round < rounds; round++ {
		prefix := fmt.Sprintf("f014-r%d", round)
		entryID := insertNode(prefix+"-entry", "10.40.0.1", "30000-30010")
		chainID := insertNode(prefix+"-chain", "10.40.0.2", "31000-31010")
		exitID := insertNode(prefix+"-exit", "10.40.0.3", "32000-32010")
		const contested = 31005

		// Tunnel under update (type 2) and a type-1 tunnel entering the
		// chain node (forwards created on it land on the chain node).
		out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/create", json.RawMessage(fmt.Sprintf(
			`{"name":"%s-main","type":2,"flow":1,"status":1,"inNodeId":[{"nodeId":%d,"protocol":"tls"}],"chainNodes":[[{"nodeId":%d,"protocol":"tls","strategy":"round"}]],"outNodeId":[{"nodeId":%d,"protocol":"tls"}]}`,
			prefix, entryID, chainID, exitID)))
		if out.Code != 0 {
			t.Fatalf("round %d: create main tunnel: %+v", round, out)
		}
		mainTunnelID := mustQueryInt64(t, repo, `SELECT id FROM tunnel WHERE name = ?`, prefix+"-main")
		out = requestContractEnvelope(t, router, admin, "/api/v1/tunnel/create", json.RawMessage(fmt.Sprintf(
			`{"name":"%s-side","type":1,"flow":1,"status":1,"inNodeId":[{"nodeId":%d,"protocol":"tls"}]}`,
			prefix, chainID)))
		if out.Code != 0 {
			t.Fatalf("round %d: create side tunnel: %+v", round, out)
		}
		sideTunnelID := mustQueryInt64(t, repo, `SELECT id FROM tunnel WHERE name = ?`, prefix+"-side")

		var wg sync.WaitGroup
		start := make(chan struct{})
		var updateCode, createCode int
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			res := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/update", json.RawMessage(fmt.Sprintf(
				`{"id":%d,"name":"%s-main","type":2,"flow":1,"status":1,"inNodeId":[{"nodeId":%d,"protocol":"tls"}],"chainNodes":[[{"nodeId":%d,"protocol":"tls","strategy":"round","port":%d}]],"outNodeId":[{"nodeId":%d,"protocol":"tls"}]}`,
				mainTunnelID, prefix, entryID, chainID, contested, exitID)))
			updateCode = res.Code
		}()
		go func() {
			defer wg.Done()
			<-start
			res := requestContractEnvelope(t, router, admin, "/api/v1/forward/create", json.RawMessage(fmt.Sprintf(
				`{"tunnelId":%d,"name":"%s-fwd","remoteAddr":"1.1.1.1:443","inPort":%d}`,
				sideTunnelID, prefix, contested)))
			createCode = res.Code
		}()
		close(start)
		wg.Wait()

		chainRows := mustQueryInt(t, repo, `SELECT COUNT(*) FROM chain_tunnel WHERE tunnel_id = ? AND node_id = ? AND port = ?`, mainTunnelID, chainID, contested)
		forwardRows := mustQueryInt(t, repo, `SELECT COUNT(*) FROM forward_port WHERE node_id = ? AND port = ?`, chainID, contested)
		if chainRows+forwardRows > 1 {
			t.Fatalf("round %d: cross-table double occupancy landed: chain=%d forward=%d (update code=%d, create code=%d)",
				round, chainRows, forwardRows, updateCode, createCode)
		}
		if updateCode == 0 && createCode == 0 {
			t.Fatalf("round %d: both racing writes reported success for contested port %d", round, contested)
		}
	}
}
