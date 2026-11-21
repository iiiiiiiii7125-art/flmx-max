package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// R18-R3-F1-03: the create/update receipts warn when a TLS tunnel
// is built unverified, but the batch redeploy receipt did not —
// redeployTunnelAndForwards never surfaced the legacy warnings the
// apply recorded, so an offline node's TLS tunnel counted as
// success with no hint. The fix returns the warnings from the
// redeploy and merges them into the batch receipt; the background
// callers get the per-node log line + queued repush from
// scheduleTunnelTLSLegacyRetry instead.

// TestTunnelBatchRedeployWarnsUnverifiedTLSNodes (R18-R3-F1-03
// receipt): batch-redeploying a TLS tunnel whose nodes are offline
// still succeeds, but the receipt must warn that the chain was
// built unverified — naming the nodes and the cause — and each
// legacy node must have a repush queued for when it comes back.
func TestTunnelBatchRedeployWarnsUnverifiedTLSNodes(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Type2Tunnel(t, h, 88, "f1-03-batch", 36008, 35009)
	res := httptest.NewRecorder()
	h.tunnelBatchRedeploy(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{88}}))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			SuccessCount int      `json:"successCount"`
			FailCount    int      `json:"failCount"`
			Warnings     []string `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode batch redeploy response: %v (%s)", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("batch redeploy with offline nodes must still succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	if env.Data.SuccessCount != 1 || env.Data.FailCount != 0 {
		t.Fatalf("successCount=%d failCount=%d, want 1/0", env.Data.SuccessCount, env.Data.FailCount)
	}
	joined := strings.Join(env.Data.Warnings, "\n")
	for _, name := range []string{"entry-a", "chain-a", "exit-a"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("batch receipt warnings must name unverified node %q, got warnings=%q", name, joined)
		}
	}
	for _, want := range []string{"以未验证方式建成", "无双向证书认证", "节点离线", "重新部署"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("batch receipt warnings must say %q, got warnings=%q", want, joined)
		}
	}
	// The same redeploy must have queued a repush per legacy node
	// (the background callers' half of the fix: log + schedule).
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	for _, id := range []int64{10, 11, 12} {
		if _, queued := h.nodeLocalRuntimeRetryQueued[id]; !queued {
			t.Fatalf("node %d must have a repush queued after the unverified redeploy", id)
		}
	}
}
