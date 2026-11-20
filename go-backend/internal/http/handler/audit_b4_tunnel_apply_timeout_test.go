package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// R18-B4-01 (timeout leg): an online node whose agent never
// answers a command must be skipped as "timeout" — not "offline"
// — and must get the same 30s retry carrier. Before the fix this
// case had no retry at all: no reconnect would ever happen for a
// node that stays connected, so the divergence lasted forever.
// This file is separate from audit_b4_tunnel_apply_defer_test.go
// because it asserts on the new SkippedNodes record: on old code
// it fails to compile (the record does not exist), while the
// receipt/retry assertions in the sibling file fail on old code
// with ordinary assertion failures.

// dialSilentAgent connects a node agent that reads commands but
// never answers them, so SendCommand runs into the node-command
// timeout (defaultNodeCommandTimeout) instead of "节点不在线".
// It mirrors newCleanupAgent's transport setup, minus the ACK.
func dialSilentAgent(t *testing.T, h *Handler, nodeID int64, secret string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.wsServer.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatalf("dial silent agent: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !h.wsServer.IsNodeOnline(nodeID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !h.wsServer.IsNodeOnline(nodeID) {
		t.Fatalf("silent agent for node %d did not come online", nodeID)
	}
}

func TestAuditB4TunnelApplyOnlineTimeoutRetries(t *testing.T) {
	r, h := r2b2Open(t)
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(
		`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, status, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		21, "b4-silent-entry", "b4-silent-secret", "10.0.0.21", "10.0.0.21", "", "30000-30010", 1, "agent", now,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(
		`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, status, forward_mode, created_time)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		22, "b4-offline-exit", "b4-offline-secret", "10.0.0.22", "10.0.0.22", "", "35000-35010", 1, "agent", now,
	).Error; err != nil {
		t.Fatal(err)
	}
	dialSilentAgent(t, h, 21, "b4-silent-secret")

	entry := &nodeRecord{ID: 21, Name: "b4-silent-entry", ServerIP: "10.0.0.21", ServerIPv4: "10.0.0.21", Status: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	exit := &nodeRecord{ID: 22, Name: "b4-offline-exit", ServerIP: "10.0.0.22", ServerIPv4: "10.0.0.22", Status: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	state := &tunnelCreateState{
		TunnelID: 999, Type: 2, TunnelProtocol: "tls",
		InNodes:  []tunnelRuntimeNode{{NodeID: 21, Protocol: "tls", Strategy: "round"}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 22, Protocol: "tls", Strategy: "round", Port: 35009}},
		Nodes:    map[int64]*nodeRecord{21: entry, 22: exit},
	}
	if _, _, err := h.applyTunnelRuntime(state); err != nil {
		t.Fatalf("apply with a timed-out entry and an offline exit must not fail the request, got %v", err)
	}
	byNode := make(map[int64]tunnelRuntimeSkippedNode, len(state.SkippedNodes))
	for _, s := range state.SkippedNodes {
		byNode[s.NodeID] = s
	}
	if s, ok := byNode[21]; !ok || s.Reason != "timeout" {
		t.Fatalf("online-but-silent entry must be skipped as timeout, got %+v", byNode[21])
	}
	if s, ok := byNode[22]; !ok || s.Reason != "offline" {
		t.Fatalf("offline exit must be skipped as offline, got %+v", byNode[22])
	}
	// Drain the skips exactly as tunnelCreate/tunnelUpdate do:
	// warnings for the receipt + the automatic retry per node.
	warnings := h.handleTunnelRuntimeSkipped(state)
	joined := strings.Join(warnings, "；")
	if !strings.Contains(joined, "b4-silent-entry") || !strings.Contains(joined, "下发超时") {
		t.Fatalf("timeout skip must warn naming the node and the timeout, got %q", joined)
	}
	if !strings.Contains(joined, "b4-offline-exit") || !strings.Contains(joined, "不在线") {
		t.Fatalf("offline skip must warn naming the node and the offline cause, got %q", joined)
	}
	// The timeout node must get the same retry carrier as the
	// offline one — this is the finding's crux: before the fix an
	// online node's command timeout had no retry at all.
	if missing := b4RetryQueued(t, h, 21, 22); len(missing) > 0 {
		t.Fatalf("skipped nodes must get the automatic retry queued, missing nodes %v", missing)
	}
}
