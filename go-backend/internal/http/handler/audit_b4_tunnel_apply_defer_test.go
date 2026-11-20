package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// R18-B4-01: tunnel runtime apply must not silently skip
// offline/timeout nodes. A type-2 tunnel whose entry/chain/exit
// nodes are unreachable still commits and still answers success —
// but the receipt must name the skipped nodes and each skipped
// node must get the 30s local-runtime retry, so an online node
// whose command timed out is re-pushed without waiting for a
// reconnect. Old code: bare success, no warning, no retry.

func b4RetryQueued(t *testing.T, h *Handler, nodeIDs ...int64) []int64 {
	t.Helper()
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	var missing []int64
	for _, id := range nodeIDs {
		if _, ok := h.nodeLocalRuntimeRetryQueued[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func TestAuditB4TunnelCreateOfflineNodesWarnAndRetry(t *testing.T) {
	h := setupK9Handler(t)
	// Nodes 10/11/12 exist in the DB with Status=1 but no agent
	// is connected, so every apply command fails "节点不在线".
	res := httptest.NewRecorder()
	h.tunnelCreate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"name": "b4-offline", "type": 2, "status": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	}))
	var env struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode tunnel create response: %v (%s)", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("tunnel create with offline nodes must still succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	for _, name := range []string{"entry-a", "chain-a", "exit-a"} {
		if !strings.Contains(env.Warning, name) {
			t.Fatalf("receipt warning must name skipped node %q, got warning=%q", name, env.Warning)
		}
	}
	if !strings.Contains(env.Warning, "自动重试") {
		t.Fatalf("receipt warning must state the automatic retry, got warning=%q", env.Warning)
	}
	if missing := b4RetryQueued(t, h, 10, 11, 12); len(missing) > 0 {
		t.Fatalf("skipped nodes must get the automatic retry queued, missing nodes %v", missing)
	}
	if _, _, count := k9TunnelRow(t, h, "b4-offline"); count != 1 {
		t.Fatalf("tunnel must be persisted despite the skips, got %d rows", count)
	}
}

func TestAuditB4TunnelUpdateOfflineNodesWarnAndRetry(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Type2Tunnel(t, h, 88, "b4-upd", 36008, 35009)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", map[string]interface{}{
		"id": 88, "name": "b4-upd-renamed", "type": 2, "status": 1, "flow": 1, "trafficRatio": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": []interface{}{[]map[string]interface{}{{"nodeId": 12, "protocol": "tls"}}},
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("tunnel update with offline nodes must still succeed, got code=%d msg=%s", env.Code, env.Msg)
	}
	joined := k6WarningsJoined(env)
	for _, name := range []string{"entry-a", "chain-a", "exit-a"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("update receipt warnings must name skipped node %q, got %q", name, joined)
		}
	}
	if !strings.Contains(joined, "自动重试") {
		t.Fatalf("update receipt warnings must state the automatic retry, got %q", joined)
	}
	if missing := b4RetryQueued(t, h, 10, 11, 12); len(missing) > 0 {
		t.Fatalf("skipped nodes must get the automatic retry queued, missing nodes %v", missing)
	}
}
