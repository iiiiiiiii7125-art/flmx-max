package contract_test

import (
	"strings"
	"testing"
	"time"
)

// Round 1 波 2 / 事项五（B04-002 handler 子项）：node/update-order 逐
// 条聚合回执。repo 的 UpdateNodeOrder 已由 C04 改为返回
// (RowsAffected, error)，本测试锁定 handler 侧消费：混入不存在 id
// 时 failures 含该条、存在条目正常落序、部分失败不报全量成功（旧
// 实现丢弃返回值恒 OKEmpty、无 data）。

func TestW2NodeUpdateOrderReportsPerItemFailures(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	_, nodeID := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-node-order-tunnel", "w2-node-order-node", "26600-26610")

	out := requestContractEnvelope(t, router, adminToken, "/api/v1/node/update-order", map[string]interface{}{
		"nodes": []map[string]interface{}{
			{"id": nodeID, "inx": 5},
			{"id": 99999, "inx": 1},
		},
	})
	if out.Code != 0 {
		t.Fatalf("expected success envelope with per-item failures, got code=%d msg=%q", out.Code, out.Msg)
	}
	result := mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["successCount"]) != 1 {
		t.Fatalf("expected successCount=1, got %#v", result["successCount"])
	}
	if contractValueAsInt64(result["failCount"]) != 1 {
		t.Fatalf("expected failCount=1, got %#v", result["failCount"])
	}
	failures, ok := result["failures"].([]interface{})
	if !ok || len(failures) != 1 {
		t.Fatalf("expected exactly one failure, got %#v", result["failures"])
	}
	first, ok := failures[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected failure object, got %T", failures[0])
	}
	if contractValueAsInt64(first["id"]) != 99999 {
		t.Fatalf("expected failure id=99999, got %#v", first["id"])
	}
	if reason, _ := first["reason"].(string); !strings.Contains(reason, "不存在") {
		t.Fatalf("expected failure reason to mention 不存在, got %q", reason)
	}
	if inx := mustQueryInt64(t, repo, `SELECT inx FROM node WHERE id = ?`, nodeID); inx != 5 {
		t.Fatalf("expected node inx updated to 5, got %d", inx)
	}

	// 对照：全合法时 failures 为空、计数全成功。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/node/update-order", map[string]interface{}{
		"nodes": []map[string]interface{}{
			{"id": nodeID, "inx": 9},
		},
	})
	if out.Code != 0 {
		t.Fatalf("expected success, got code=%d msg=%q", out.Code, out.Msg)
	}
	result = mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["successCount"]) != 1 || contractValueAsInt64(result["failCount"]) != 0 {
		t.Fatalf("expected successCount=1/failCount=0, got %#v / %#v", result["successCount"], result["failCount"])
	}
	if inx := mustQueryInt64(t, repo, `SELECT inx FROM node WHERE id = ?`, nodeID); inx != 9 {
		t.Fatalf("expected node inx updated to 9, got %d", inx)
	}
}
