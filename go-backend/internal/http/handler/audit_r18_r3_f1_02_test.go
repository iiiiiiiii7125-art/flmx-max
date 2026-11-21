package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-R3-F1-02: nodeTunnelTLSWasIssued used to return false on a DB
// read error (fail-open to legacy). The best-exit downgrade guard
// and the deploy fail-closed gate both read it, so a transient
// GetNodeTunnelTLS error in the same round that ListNodeTunnelTLSReady
// succeeded (Ready=0, previously issued) skipped the downgrade
// check and let a legacy dialer be pushed against a verified
// listener — or let a full deploy fall through to legacy + warning.
// The fix reports the read error and both callers treat "unknown"
// as issued: the reorder is refused and the deploy is refused.
// The fault is injected through nodeTunnelTLSRowReader (the test
// seam next to tunnelTLSCommandSender): the batch ready-set read
// keeps working while the single-row issued read fails, which is
// the exact interleave the report describes.

// TestBestExitReorderRefusesWhenIssuedReadFails (R18-R3-F1-02,
// reorder path): a previously-issued dialer-group member reads
// Ready=0 in the ready set while its issued-row read fails — the
// reorder must refuse to push anything, exactly as when the
// member is known-issued and not ready.
func TestBestExitReorderRefusesWhenIssuedReadFails(t *testing.T) {
	a := newBestExitReorderAgent(t, []int64{1, 3}, []int64{2, 4})
	seedBestExitReorderTunnel(t, a, 78)
	markBestExitReorderReady(t, a, 1, 2, 3, 4)
	if err := a.h.repo.ClearNodeTunnelTLSReady(3); err != nil {
		t.Fatalf("clear node 3 ready: %v", err)
	}
	a.h.nodeTunnelTLSRowReader = func(nodeID int64) (*model.NodeTunnelTLS, error) {
		if nodeID == 3 {
			return nil, errors.New("simulated transient node_tunnel_tls read error")
		}
		return a.h.repo.GetNodeTunnelTLS(nodeID)
	}
	inNodes := []chainNodeRecord{
		{NodeID: 1, Protocol: "tls", Strategy: "best"},
		{NodeID: 3, Protocol: "tls", Strategy: "best"},
	}
	outNodes := []chainNodeRecord{
		{NodeID: 4, Protocol: "tls", Strategy: "best", Port: 24002},
		{NodeID: 2, Protocol: "tls", Strategy: "best", Port: 24001},
	}
	cmds := driveBestExitReorderSwitch(t, a, 78, inNodes, nil, outNodes, inNodes)
	if len(cmds) != 0 {
		t.Fatalf("refused reorder must push nothing, got %d UpdateChains", len(cmds))
	}
	snap, ok := a.h.bestExit.snapshot(bestExitOwnerKey{TunnelID: 78, OwnerNodeID: 1})
	if !ok {
		t.Fatal("best-exit decision missing for owner 1 (the switch never fired)")
	}
	if snap.Reason != "apply retry cooldown" {
		t.Fatalf("snapshot reason = %q, want %q (switch fired, apply refused and recorded)", snap.Reason, "apply retry cooldown")
	}
}

// TestTunnelTLSReadyForStateRefusesWhenIssuedReadFails (R18-R3-F1-02,
// full-deploy path): node 7 was issued before (row exists, Ready=0)
// and the re-issue fails (offline); the issued-row read then fails
// transiently — the deploy must be refused (fail-closed), not fall
// through to legacy + warning.
func TestTunnelTLSReadyForStateRefusesWhenIssuedReadFails(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline, statusErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	if err := h.repo.MarkNodeTunnelTLSReady(7, "ca-7", "cert-7", "node-7.flmx.internal", time.Now().Unix()); err != nil {
		t.Fatalf("mark node 7 ready: %v", err)
	}
	if err := h.repo.ClearNodeTunnelTLSReady(7); err != nil {
		t.Fatalf("clear node 7 ready: %v", err)
	}
	h.nodeTunnelTLSRowReader = func(nodeID int64) (*model.NodeTunnelTLS, error) {
		if nodeID == 7 {
			return nil, errors.New("simulated transient node_tunnel_tls read error")
		}
		return h.repo.GetNodeTunnelTLS(nodeID)
	}
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	if _, err := h.tunnelTLSReadyForState(state); err == nil {
		t.Fatal("an issued-read failure for a previously-issued node must refuse the deploy (fail-closed), got nil error")
	} else if !strings.Contains(err.Error(), "拒绝以未验证方式建链") {
		t.Fatalf("refusal error = %q, want it to refuse unverified building", err.Error())
	}
}

// TestBestExitReorderWouldDowngradeTreatsIssuedReadErrorAsIssued
// (R18-R3-F1-02, guard unit): with the repository closed, every
// issued-row read errors while the ready set is handed in directly
// (the successful List read of the report scenario) — a not-ready
// member must still trip the downgrade check. This test uses no
// seam, so it also red-proofs by file-level revert of
// tunnel_tls.go alone.
func TestBestExitReorderWouldDowngradeTreatsIssuedReadErrorAsIssued(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/tls-issued-err.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	h := New(r, "unit-test-secret")
	if err := r.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}
	targets := []tunnelRuntimeNode{{NodeID: 2, Protocol: "tls"}}
	tlsReady := map[int64]bool{1: true, 2: false}
	if !h.bestExitReorderWouldDowngrade(1, targets, "tls", []int64{1, 2}, tlsReady) {
		t.Fatal("an issued-read error must count as issued: member 2 is not ready, so the reorder would downgrade a verified leg")
	}
}
