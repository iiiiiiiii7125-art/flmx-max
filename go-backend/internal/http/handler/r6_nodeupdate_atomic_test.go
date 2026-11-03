package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R6-21 regression gate: nodeUpdate's two-phase write must not
// land the node fields when the SSH config fails validation.
// Before the fix the order was UpdateNode -> persistNodeSSHConfig,
// so a deterministic SSH validation failure (here: password auth
// with no password anywhere) returned an error receipt while the
// submitted fields had already been persisted, undisclosed. The
// SSH resolve/validate phase now runs before UpdateNode: the same
// request is rejected whole, with zero fields landed.

func TestNodeUpdateSSHValidationFailureLandsNothing(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r6-21.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, forward_mode)
		VALUES('nft-r621', 's', '198.51.100.40', '30000-30010', ?, ?, 0, '[::]', '[::]', 'nftables')
	`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "node")

	res := postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id":   nodeID,
		"name": "renamed-r621",
		"sshConfig": map[string]interface{}{
			"host": "198.51.100.40", "port": 22, "username": "root",
			"authType": "password", "password": "",
		},
	})
	code, msg := decodeEnvelopeA204(t, res)
	if code == 0 {
		t.Fatalf("invalid sshConfig must fail the update, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "SSH 密码不能为空") {
		t.Fatalf("receipt must name the SSH validation failure, got msg=%q", msg)
	}

	node, err := r.GetNodeByID(nodeID)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Name != "nft-r621" {
		t.Fatalf("R6-21: name landed despite the rejected update (name=%q) — the two-phase write is still non-atomic for validation failures", node.Name)
	}
	if cfg, _ := r.GetNodeSSHConfig(nodeID); cfg != nil {
		t.Fatalf("no SSH config may be written by the rejected update, got %+v", cfg)
	}

	// Control: the same rename with a valid sshConfig succeeds
	// whole — rename lands and the SSH config is written.
	res = postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id":   nodeID,
		"name": "renamed-r621",
		"sshConfig": map[string]interface{}{
			"host": "198.51.100.40", "port": 22, "username": "root",
			"authType": "password", "password": "secret",
		},
	})
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("valid update must succeed, code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	node, err = r.GetNodeByID(nodeID)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	if node.Name != "renamed-r621" {
		t.Fatalf("valid update must land the rename, name=%q", node.Name)
	}
	cfg, err := r.GetNodeSSHConfig(nodeID)
	if err != nil || cfg == nil || cfg.Port != 22 {
		t.Fatalf("valid update must write the SSH config, cfg=%+v err=%v", cfg, err)
	}
}
