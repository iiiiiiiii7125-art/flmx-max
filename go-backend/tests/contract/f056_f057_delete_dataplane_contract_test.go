package contract_test

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/auth"
)

// F-056/F-057 (B4): node/user deletion previously went straight to the
// DB cascade — gost forward services on the affected nodes never got a
// DeleteService, so orphan listeners kept forwarding outside counting
// and quota (the same defect family as F-002's tunnel delete).
//
// These tests reuse the F-002 seeding (a gost tunnel + forward owned by
// user 2 on a mock-online node) and assert the DeleteService fan-out for
// both delete entries.

// TestUserDeleteCleansForwardDataPlaneContract: deleting the owner must
// delete the forward's services on the node before removing DB rows.
func TestUserDeleteCleansForwardDataPlaneContract(t *testing.T) {
	secret := "f057-secret"
	router, repo := setupContractRouter(t, secret)
	server := httptest.NewServer(router)
	defer server.Close()
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	now := time.Now().UnixMilli()

	// seedF002Tunnel assigns the forward to user_id=2; give that user a
	// real row (role 1, deletable).
	if err := repo.DB().Exec(`
		INSERT INTO "user"(id, user, pwd, role_id, status, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time)
		VALUES(2, 'contract-user', 'x', 1, 1, 0, 100, 0, 0, 0, 10, ?)
	`, now).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, nodeID, forwardID := seedF002Tunnel(t, repo, now, "f057-tunnel", "f057-node", "f057-node-secret", "agent", 26001)
	rec := &deleteServiceRecorder{}
	stop := startMockNodeSessionWithCommandRecorder(t, server.URL, "f057-node-secret", rec.handler)
	defer stop()
	waitNodeStatus(t, repo, nodeID, 1)

	out := requestContractEnvelope(t, router, admin, "/api/v1/user/delete", map[string]interface{}{"id": 2})
	if out.Code != 0 {
		t.Fatalf("user delete: expected success, got code=%d msg=%q", out.Code, out.Msg)
	}
	base := fmt.Sprintf("%d_2_0", forwardID)
	if !rec.sawService(base+"_tcp") {
		t.Fatalf("F-057: user delete never sent DeleteService for %s_tcp; recorded=%v", base, rec.services)
	}
	if got := mustQueryInt(t, repo, `SELECT COUNT(*) FROM forward WHERE id = ?`, forwardID); got != 0 {
		t.Fatalf("forward row should be gone after user delete, got %d", got)
	}
}

// TestNodeDeleteCleansForwardServicesOnNodeContract: deleting a node
// must at least attempt to remove the forward services hosted on it
// (the forward itself belongs to the tunnel and survives in the DB).
func TestNodeDeleteCleansForwardServicesOnNodeContract(t *testing.T) {
	secret := "f056-secret"
	router, repo := setupContractRouter(t, secret)
	server := httptest.NewServer(router)
	defer server.Close()
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	now := time.Now().UnixMilli()

	_, nodeID, forwardID := seedF002Tunnel(t, repo, now, "f056-tunnel", "f056-node", "f056-node-secret", "agent", 26001)
	rec := &deleteServiceRecorder{}
	stop := startMockNodeSessionWithCommandRecorder(t, server.URL, "f056-node-secret", rec.handler)
	defer stop()
	waitNodeStatus(t, repo, nodeID, 1)

	out := requestContractEnvelope(t, router, admin, "/api/v1/node/delete", map[string]interface{}{"id": nodeID})
	if out.Code != 0 {
		t.Fatalf("node delete: expected success, got code=%d msg=%q", out.Code, out.Msg)
	}
	base := fmt.Sprintf("%d_2_0", forwardID)
	if !rec.sawService(base+"_tcp") {
		t.Fatalf("F-056: node delete never sent DeleteService for %s_tcp; recorded=%v", base, rec.services)
	}
	if got := mustQueryInt(t, repo, `SELECT COUNT(*) FROM node WHERE id = ?`, nodeID); got != 0 {
		t.Fatalf("node row should be gone after node delete, got %d", got)
	}
}
