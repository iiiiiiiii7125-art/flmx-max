package handler

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// R18-R3-F1-01: when the panel CA files are lost while
// node_tunnel_tls rows survive, the panel creates a new CA — but
// ensure trusted any node whose agent fingerprints matched its DB
// row, so old-generation nodes were never re-issued and a TLS
// tunnel mixing old-CA and new-CA nodes broke cross-generation
// legs. The fix (option 1) binds the CA generation into the ensure
// fast path: the agent-reported CA fingerprint must match the
// panel's current CA, otherwise the node is re-issued and its
// fingerprints converge to the new CA.

// setupStaleCAGeneration builds the "DB survived, CA files lost"
// state: nodes 7 and 8 are marked Ready under an old CA
// fingerprint pair, while the fake agent reports exactly that old
// material (matching the rows) and the panel holds a fresh CA.
// It returns the handler, the fake sender, and the current / old
// CA fingerprints.
func setupStaleCAGeneration(t *testing.T) (*Handler, *fakeTunnelTLSSender, string, string, string) {
	t.Helper()
	h := openTunnelTLSHandler(t)
	ca, err := h.tunnelTLSAuthority()
	if err != nil {
		t.Fatalf("tunnelTLSAuthority: %v", err)
	}
	currentCA := ca.CASha256()
	oldCA := strings.Repeat("a", 64)
	oldCert := strings.Repeat("b", 64)
	fake := &fakeTunnelTLSSender{statusReady: true, statusCA: oldCA, statusCert: oldCert}
	h.tunnelTLSCommandSender = fake.send
	now := time.Now().Unix()
	for _, id := range []int64{7, 8} {
		if err := h.repo.MarkNodeTunnelTLSReady(id, oldCA, oldCert, fmt.Sprintf("node-%d.flmx.internal", id), now); err != nil {
			t.Fatalf("mark node %d ready under old CA: %v", id, err)
		}
	}
	return h, fake, currentCA, oldCA, oldCert
}

// TestEnsureNodeTunnelTLSReadyReissuesStaleCAGeneration
// (R18-R3-F1-01): a Ready node whose agent material matches its DB
// row but belongs to a superseded CA generation must be re-issued,
// not trusted.
func TestEnsureNodeTunnelTLSReadyReissuesStaleCAGeneration(t *testing.T) {
	h, fake, currentCA, _, oldCert := setupStaleCAGeneration(t)
	ready, _ := h.ensureNodeTunnelTLSReadyDetailed(7)
	if !ready {
		t.Fatal("stale-CA node must become ready after re-issue")
	}
	if fake.issueCalls != 1 {
		t.Fatalf("stale-CA node must be re-issued once, got %d issue calls", fake.issueCalls)
	}
	st, err := h.repo.GetNodeTunnelTLS(7)
	if err != nil || st == nil {
		t.Fatalf("node 7 row = %+v, err = %v, want a row after re-issue", st, err)
	}
	if st.CASha256 != currentCA {
		t.Fatalf("node 7 CA fingerprint = %q, want current CA %q", st.CASha256, currentCA)
	}
	if st.CertSha256 == oldCert {
		t.Fatalf("node 7 cert fingerprint still the stale one %q", oldCert)
	}
}

// TestTunnelTLSReadyForStateConvergesMixedCAGeneration
// (R18-R3-F1-01): a mixed tunnel whose two legs both hold
// old-generation material must re-issue both legs on deploy, so
// both fingerprints converge to the current CA.
func TestTunnelTLSReadyForStateConvergesMixedCAGeneration(t *testing.T) {
	h, fake, currentCA, _, _ := setupStaleCAGeneration(t)
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err != nil {
		t.Fatalf("mixed-CA tunnel must converge, got error: %v", err)
	}
	if !ready[7] || !ready[8] {
		t.Fatalf("ready = %v, want both legs ready after convergence", ready)
	}
	if fake.issueCalls != 2 {
		t.Fatalf("both stale-CA legs must be re-issued, got %d issue calls", fake.issueCalls)
	}
	for _, id := range []int64{7, 8} {
		st, err := h.repo.GetNodeTunnelTLS(id)
		if err != nil || st == nil {
			t.Fatalf("node %d row = %+v, err = %v, want a row after re-issue", id, st, err)
		}
		if st.CASha256 != currentCA {
			t.Fatalf("node %d CA fingerprint = %q, want current CA %q", id, st.CASha256, currentCA)
		}
	}
}
