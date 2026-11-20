package handler

import (
	"testing"

	"go-backend/internal/store/repo"
)

// TestBestExitReorderRefusesUnknownDialerGroup (R18-R2-F1-01,
// fail-closed): a reorder that cannot name its dialer group must
// not push a chain for a TLS-family tunnel — an unknown group
// renders legacy by construction, which would downgrade a
// verified leg.
func TestBestExitReorderRefusesUnknownDialerGroup(t *testing.T) {
	a := newBestExitReorderAgent(t, []int64{1}, []int64{2, 4})
	seedBestExitReorderTunnel(t, a, 78)
	markBestExitReorderReady(t, a, 1, 2, 4)
	outNodes := []chainNodeRecord{
		{NodeID: 4, Protocol: "tls", Strategy: "best", Port: 24002},
		{NodeID: 2, Protocol: "tls", Strategy: "best", Port: 24001},
	}
	scores := []bestExitCandidateScore{
		{OwnerNodeID: 1, ExitNodeID: 2, Success: true},
		{OwnerNodeID: 1, ExitNodeID: 4, Success: true},
	}
	if err := a.h.applyBestExitChainOrder(78, 1, outNodes, scores, "", nil); err == nil {
		t.Fatal("reorder with an unknown dialer group must be refused for a TLS tunnel")
	}
	if got := len(a.commandsOfType("UpdateChains")); got != 0 {
		t.Fatalf("refused reorder must push nothing, got %d UpdateChains", got)
	}
}

// TestTunnelTLSReadyMapErrReportsDBError (R18-R2-F1-01, fail-closed
// 收口): the reorder path must be able to tell a DB error apart
// from "no node is ready" — a DB blip must refuse the reorder,
// not render legacy against a possibly-verified listener.
func TestTunnelTLSReadyMapErrReportsDBError(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/tls-ready-err.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	h := New(r, "unit-test-secret")
	if err := r.Close(); err != nil {
		t.Fatalf("close repo: %v", err)
	}
	if _, err := h.tunnelTLSReadyMapErr([]int64{1, 2}); err == nil {
		t.Fatal("tunnelTLSReadyMapErr must report a DB error instead of silently returning an empty ready set")
	}
}
