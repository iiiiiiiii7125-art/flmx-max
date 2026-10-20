package contract_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/auth"
	storeRepo "go-backend/internal/store/repo"
)

// F-002 (B4): deleting a tunnel must run each of its forwards through the
// same data-plane cleanup as a single forward delete (gost DeleteService /
// nft pending-delete ledger) BEFORE the DB cascade. Before the fix, all
// tunnel-delete entries went straight to the DB cascade: gost services
// kept listening as orphans outside counting/quota, and nft forwards
// lost even their pending-delete ledger rows.

type deleteServiceRecorder struct {
	mu       sync.Mutex
	services []string
	calls    int
}

func (r *deleteServiceRecorder) handler(cmdType string, data json.RawMessage) (bool, string) {
	if strings.EqualFold(strings.TrimSpace(cmdType), "DeleteService") {
		var payload struct {
			Services []string `json:"services"`
		}
		_ = json.Unmarshal(data, &payload)
		r.mu.Lock()
		r.calls++
		r.services = append(r.services, payload.Services...)
		r.mu.Unlock()
	}
	return false, ""
}

func (r *deleteServiceRecorder) sawService(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.services {
		if s == name {
			return true
		}
	}
	return false
}

func seedF002Tunnel(t *testing.T, repo *storeRepo.Repository, now int64, name, nodeName, secret, forwardMode string, port int) (tunnelID, nodeID, forwardID int64) {
	t.Helper()
	if err := repo.DB().Exec(`
		INSERT INTO tunnel(name, traffic_ratio, type, protocol, flow, status, created_time, updated_time, in_ip, inx, ip_preference)
		VALUES(?, 1.0, 1, 'tls', 1, 1, ?, ?, NULL, 0, '')
	`, name, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	tunnelID = mustLastInsertID(t, repo, name)
	// Node starts OFFLINE (status=0) on purpose: the WS connect handler
	// registers the node session in s.nodes BEFORE UpdateNodeOnline flips
	// status to 1, so waitNodeStatus(…, 1) is a real synchronization point
	// for "the mock session can receive commands". Seeding status=1 made
	// that wait vacuous and the delete request raced session registration
	// (SendCommand fails fast with 节点不在线) — an intermittent failure
	// in the gost sub-cases below and in the F-056/F-057 tests reusing
	// this seed.
	if err := repo.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, forward_mode)
		VALUES(?, ?, '10.0.0.1', '10.0.0.1', '', '26000-26010', '', 'v1', 1, 1, 1, ?, ?, 0, '[::]', '[::]', 0, ?)
	`, nodeName, secret, now, now, forwardMode).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID = mustLastInsertID(t, repo, nodeName)
	if err := repo.DB().Exec(`
		INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(?, 1, ?, 0, 'round', 1, 'tls')
	`, tunnelID, nodeID).Error; err != nil {
		t.Fatalf("insert chain_tunnel: %v", err)
	}
	if err := repo.DB().Exec(`
		INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(2, 'contract-user', ?, ?, '1.1.1.1:443', 'fifo', 0, 0, ?, ?, 1, 0)
	`, name+"-forward", tunnelID, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	forwardID = mustLastInsertID(t, repo, name+"-forward")
	if err := repo.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, ?, ?)`, forwardID, nodeID, port).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}
	return tunnelID, nodeID, forwardID
}

// gost tunnel, node online (mock agent): every tunnel-delete entry must
// deliver DeleteService for the forward's services before removing rows.
func TestTunnelDeleteSendsDeleteServiceForForwardsContract(t *testing.T) {
	secret := "f002-gost-secret"
	router, repo := setupContractRouter(t, secret)
	server := httptest.NewServer(router)
	defer server.Close()
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	now := time.Now().UnixMilli()

	run := func(caseName, path string, makeBody func(tunnelID int64) map[string]interface{}, nodeSecret string) {
		tunnelID, nodeID, forwardID := seedF002Tunnel(t, repo, now, "f002-"+caseName, "f002-node-"+caseName, nodeSecret, "agent", 26001)
		rec := &deleteServiceRecorder{}
		stop := startMockNodeSessionWithCommandRecorder(t, server.URL, nodeSecret, rec.handler)
		defer stop()
		waitNodeStatus(t, repo, nodeID, 1)

		out := requestContractEnvelope(t, router, admin, path, makeBody(tunnelID))
		if out.Code != 0 {
			t.Fatalf("%s: expected success, got code=%d msg=%q", caseName, out.Code, out.Msg)
		}
		base := fmt.Sprintf("%d_2_0", forwardID)
		if rec.calls == 0 || !rec.sawService(base+"_tcp") {
			t.Fatalf("%s: DeleteService for forward service %s_tcp must be delivered before tunnel deletion (calls=%d services=%v)",
				caseName, base, rec.calls, rec.services)
		}
		if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); count != 0 {
			t.Fatalf("%s: tunnel row must be deleted", caseName)
		}
		if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM forward WHERE id = ?`, forwardID); count != 0 {
			t.Fatalf("%s: forward row must be deleted", caseName)
		}
	}

	run("delete-with-forwards", "/api/v1/tunnel/delete-with-forwards",
		func(id int64) map[string]interface{} { return map[string]interface{}{"id": id, "action": "delete_forwards"} }, "f002-secret-dwf")
	run("plain-delete", "/api/v1/tunnel/delete",
		func(id int64) map[string]interface{} { return map[string]interface{}{"id": id} }, "f002-secret-plain")
	run("batch-delete", "/api/v1/tunnel/batch-delete",
		func(id int64) map[string]interface{} { return map[string]interface{}{"ids": []int64{id}} }, "f002-secret-batch")
}

// nft tunnel whose node cannot be reached (no SSH config): the forward
// must land in the pending-delete ledger for the background retry loop
// and the tunnel must NOT be deleted — wiping the rows (the old cascade
// behavior) destroys the only record that the rule still exists.
func TestTunnelDeleteNftForwardGoesPendingWhenNodeUnreachableContract(t *testing.T) {
	secret := "f002-nft-secret"
	router, repo := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	now := time.Now().UnixMilli()

	tunnelID, _, forwardID := seedF002Tunnel(t, repo, now, "f002-nft", "f002-node-nft", "f002-nft-node-secret", "nftables", 26002)

	out := requestContractEnvelope(t, router, admin, "/api/v1/tunnel/delete", map[string]interface{}{"id": tunnelID})
	if out.Code == 0 {
		t.Fatalf("delete must report failure while nft cleanup is pending, got success: %+v", out)
	}
	if status := mustQueryInt(t, repo, `SELECT status FROM forward WHERE id = ?`, forwardID); status != 2 {
		t.Fatalf("forward must be marked pending-delete (status=2), got %d", status)
	}
	if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM nft_pending_delete WHERE forward_id = ?`, forwardID); count != 1 {
		t.Fatalf("pending-delete ledger row must exist for retry, got %d", count)
	}
	if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); count != 1 {
		t.Fatalf("tunnel must survive until its forwards are cleaned, got count=%d", count)
	}
}
