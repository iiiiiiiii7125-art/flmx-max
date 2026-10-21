package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go-backend/internal/store/repo"
)

// FIX-B A2-05（=A9-04 后端半）：nodeBatchDelete 吞掉全部删除错误且恒
// OKEmpty——改为 batchOperationResult 逐条回执，不存在节点如实记失败。
func TestNodeBatchDeleteReturnsReceipt(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/batch-node.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.17")

	body := strings.NewReader(`{"ids":[` + strconv.FormatInt(nodeID, 10) + `,999999]}`)
	req := httptest.NewRequest("POST", "/api/v1/node/batch-delete", body)
	// nodeBatchDelete 走 idsFromBody（不校验 claims），直接调用即可。
	res := httptest.NewRecorder()
	h.nodeBatchDelete(res, req)

	var env struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("batch delete code=%d body=%s", env.Code, res.Body.String())
	}
	if env.Data.SuccessCount != 1 || env.Data.FailCount != 1 {
		t.Fatalf("A2-05: receipt = %+v, want success 1 / fail 1", env.Data)
	}
	if len(env.Data.Failures) != 1 || env.Data.Failures[0].ID != 999999 {
		t.Fatalf("A2-05: failures = %+v, want the missing node id", env.Data.Failures)
	}
	if n, err := r.GetNodeByID(nodeID); err != nil || n != nil {
		t.Fatalf("existing node should have been deleted, got %+v / %v", n, err)
	}
}

// FIX-B A9-05（BC-21）：隧道编辑保留请求显式给出的 inIp，不再被系统
// 采集值无条件覆盖（create 已是此口径，update 漏跟）。
func TestTunnelUpdatePreservesExplicitInIP(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/tunnel-inip.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.18")
	tunnelID := seedTunnelForNftables(t, h, "inip-tunnel", nodeID)

	res := httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       tunnelID,
		"inNodeId": []map[string]interface{}{{"nodeId": nodeID, "protocol": "tls"}},
		"inIp":     "203.0.113.99",
	}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%s body=%s", code, msg, res.Body.String())
	}
	var inIP string
	if err := r.DB().Raw(`SELECT COALESCE(in_ip, '') FROM tunnel WHERE id = ?`, tunnelID).Scan(&inIP).Error; err != nil {
		t.Fatal(err)
	}
	if inIP != "203.0.113.99" {
		t.Fatalf("A9-05: explicit inIp overwritten on edit, got %q want 203.0.113.99", inIP)
	}
}

