package handler

import (
	"strings"
	"testing"
)

// R18-R2-F1-03: a new TLS tunnel whose nodes never received
// certificates (offline / old agent) used to be built as an
// unverified chain with no receipt hint at all — the only record
// was one server-side log line. The fix records the legacy nodes
// during the deploy (tunnelTLSReadyForState) and surfaces them in
// the create/update receipt: which node, why, the impact
// (unverified segments, no mutual certificate authentication),
// and the remediation (bring the node online / upgrade the agent,
// then redeploy).

// TestTunnelTLSReadyForStateRecordsLegacyNodesOffline: offline,
// never-issued nodes keep the deploy (legacy) and are recorded on
// the state with the classified cause, so the receipt can warn.
func TestTunnelTLSReadyForStateRecordsLegacyNodesOffline(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueErr: errFakeTunnelTLSNodeOffline, statusErr: errFakeTunnelTLSNodeOffline}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	ready, err := h.tunnelTLSReadyForState(state)
	if err != nil {
		t.Fatalf("never-issued offline nodes must keep legacy rendering, got error: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want empty", ready)
	}
	if len(state.TLSLegacyNodes) != 2 {
		t.Fatalf("TLSLegacyNodes = %+v, want both nodes recorded", state.TLSLegacyNodes)
	}
	for _, n := range state.TLSLegacyNodes {
		if n.Cause != "节点离线" {
			t.Fatalf("node %d cause = %q, want 节点离线", n.NodeID, n.Cause)
		}
	}
}

// TestTunnelTLSReadyForStateRecordsLegacyNodeOldAgent: an old agent
// that answers the issue command with "未知命令" is recorded with
// the old-agent cause (not lumped in as offline).
func TestTunnelTLSReadyForStateRecordsLegacyNodeOldAgent(t *testing.T) {
	h := openTunnelTLSHandler(t)
	fake := &fakeTunnelTLSSender{issueRejectMessage: "未知命令类型: TunnelTLSIssue"}
	h.tunnelTLSCommandSender = fake.send
	state := &tunnelCreateState{
		TunnelID: 99, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 7}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 8}},
	}
	if _, err := h.tunnelTLSReadyForState(state); err != nil {
		t.Fatalf("old-agent nodes must keep legacy rendering, got error: %v", err)
	}
	if len(state.TLSLegacyNodes) != 2 {
		t.Fatalf("TLSLegacyNodes = %+v, want both nodes recorded", state.TLSLegacyNodes)
	}
	for _, n := range state.TLSLegacyNodes {
		if n.Cause != "agent 版本过旧" {
			t.Fatalf("node %d cause = %q, want agent 版本过旧", n.NodeID, n.Cause)
		}
	}
}

// TestTunnelTLSLegacyWarningsNameNodesAndImpact: the renderer names
// the node and the cause, states the impact (unverified segments,
// no mutual certificate authentication), and the remediation
// (redeploy after the node is back / upgraded). An unclassified
// cause falls back to the generic wording.
func TestTunnelTLSLegacyWarningsNameNodesAndImpact(t *testing.T) {
	h := &Handler{jwtSecret: "unit-test-secret"}
	state := &tunnelCreateState{
		Nodes: map[int64]*nodeRecord{
			7: {ID: 7, Name: "entry-a"},
			8: {ID: 8, Name: "exit-a"},
		},
		TLSLegacyNodes: []tunnelTLSLegacyNode{
			{NodeID: 7, Cause: "节点离线"},
			{NodeID: 8, Cause: ""},
		},
	}
	warnings := h.tunnelTLSLegacyWarnings(state)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", warnings)
	}
	for _, want := range []string{"entry-a", "节点离线", "以未验证方式建成", "无双向证书认证", "重新部署"} {
		if !strings.Contains(warnings[0], want) {
			t.Fatalf("entry warning = %q, want it to contain %q", warnings[0], want)
		}
	}
	for _, want := range []string{"exit-a", "证书下发失败", "以未验证方式建成", "重新部署"} {
		if !strings.Contains(warnings[1], want) {
			t.Fatalf("exit warning = %q, want it to contain %q", warnings[1], want)
		}
	}
	if got := h.tunnelTLSLegacyWarnings(&tunnelCreateState{}); len(got) != 0 {
		t.Fatalf("no legacy nodes must produce no warnings, got %v", got)
	}
}
