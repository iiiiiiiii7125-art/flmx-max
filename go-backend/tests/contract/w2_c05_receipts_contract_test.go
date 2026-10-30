package contract_test

import (
	"strings"
	"testing"
	"time"
)

// Round 1 波 2 / 簇 C05 回归（隧道管理回执与目标校验）：
//   - B06-002：delete-with-forwards 的 replace 目标校验须先于删除
//     动作、无转发时同样执行（旧实现无转发直接删并报成功）。
//   - B06-005：batch-delete-preview 混入坏 id 改逐条口径（有效条目
//     照常返回、坏 id 挂 failures），不再整批失败。
//   - B06-004：tunnel/update-order 逐条回执，不存在的 id 计 failures。
//   - B08-002：forward/update-order 两段式——先全量校验、后落库，
//     中途不存在时前面条目的排序不得被改动。
//   - B08-003：forward 批量入口对 id<=0 与不存在 id 同口径计入
//     failures，不再静默吞。

func TestW2TunnelDeleteWithForwardsReplaceWithoutTargetRejectedWhenNoForwards(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	tunnelID, _ := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-notarget-tunnel", "w2-notarget-node", "26000-26010")

	// 单条入口：无转发 + replace + 无 target → 必须被拒、隧道保留。
	out := requestContractEnvelope(t, router, adminToken, "/api/v1/tunnel/delete-with-forwards", map[string]interface{}{
		"id":     tunnelID,
		"action": "replace",
	})
	if out.Code != -1 || !strings.Contains(out.Msg, "目标隧道不能为空") {
		t.Fatalf("expected -1 目标隧道不能为空, got code=%d msg=%q", out.Code, out.Msg)
	}
	if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); count != 1 {
		t.Fatalf("tunnel must be kept after rejected replace, got count=%d", count)
	}

	// 批量入口同口径：无转发 + replace + 无 target → 被拒、隧道保留。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/tunnel/batch-delete-with-forwards", map[string]interface{}{
		"ids":    []int64{tunnelID},
		"action": "replace",
	})
	if out.Code != -1 || !strings.Contains(out.Msg, "目标隧道不能为空") {
		t.Fatalf("batch: expected -1 目标隧道不能为空, got code=%d msg=%q", out.Code, out.Msg)
	}
	if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, tunnelID); count != 1 {
		t.Fatalf("tunnel must be kept after rejected batch replace, got count=%d", count)
	}

	// 对照：replace + target 等于自身 → 同样被拒、隧道保留。
	otherTunnelID, _ := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-notarget-other", "w2-notarget-node-b", "26100-26110")
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/tunnel/delete-with-forwards", map[string]interface{}{
		"id":             otherTunnelID,
		"action":         "replace",
		"targetTunnelId": otherTunnelID,
	})
	if out.Code != -1 || !strings.Contains(out.Msg, "目标隧道不能与当前隧道相同") {
		t.Fatalf("expected -1 目标隧道不能与当前隧道相同, got code=%d msg=%q", out.Code, out.Msg)
	}
	if count := mustQueryInt(t, repo, `SELECT COUNT(1) FROM tunnel WHERE id = ?`, otherTunnelID); count != 1 {
		t.Fatalf("tunnel must be kept after self-target replace, got count=%d", count)
	}
}

func TestW2TunnelBatchDeletePreviewMixedIDsReturnsPerItem(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	tunnelA, nodeA := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-preview-a", "w2-preview-node-a", "26200-26210")
	seedTunnelDeleteForward(t, repo, now, tunnelA, nodeA, "w2-preview-forward", 26201)

	out := requestContractEnvelope(t, router, adminToken, "/api/v1/tunnel/batch-delete-preview", map[string]interface{}{
		"ids": []int64{tunnelA, 99999},
	})
	if out.Code != 0 {
		t.Fatalf("expected success with per-item failures, got code=%d msg=%q", out.Code, out.Msg)
	}
	data, ok := out.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected preview object, got %T", out.Data)
	}
	if contractValueAsInt64(data["tunnelCount"]) != 1 {
		t.Fatalf("expected tunnelCount=1 (only the valid tunnel), got %#v", data["tunnelCount"])
	}
	if contractValueAsInt64(data["totalForwardCount"]) != 1 {
		t.Fatalf("expected totalForwardCount=1, got %#v", data["totalForwardCount"])
	}
	items, ok := data["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected exactly one preview item, got %#v", data["items"])
	}
	failures, ok := data["failures"].([]interface{})
	if !ok || len(failures) != 1 {
		t.Fatalf("expected exactly one failure entry, got %#v", data["failures"])
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
}

func TestW2TunnelUpdateOrderReportsPerItemFailures(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	tunnelID, _ := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-order-tunnel", "w2-order-node", "26300-26310")

	out := requestContractEnvelope(t, router, adminToken, "/api/v1/tunnel/update-order", map[string]interface{}{
		"tunnels": []map[string]interface{}{
			{"id": tunnelID, "inx": 7},
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
	if inx := mustQueryInt64(t, repo, `SELECT inx FROM tunnel WHERE id = ?`, tunnelID); inx != 7 {
		t.Fatalf("expected valid tunnel inx updated to 7, got %d", inx)
	}
}

func TestW2ForwardUpdateOrderValidatesAllBeforeWriting(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	tunnelID, nodeID := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-fwd-order-tunnel", "w2-fwd-order-node", "26400-26410")
	forwardID := seedTunnelDeleteForward(t, repo, now, tunnelID, nodeID, "w2-fwd-order-forward", 26401)
	// seed 的 inx=0；请求把合法条目排在不存在条目前面——旧实现会先
	// 把 F 的 inx 写成 42 再报错，两段式下必须保持 0 不变。
	out := requestContractEnvelope(t, router, adminToken, "/api/v1/forward/update-order", map[string]interface{}{
		"forwards": []map[string]interface{}{
			{"id": forwardID, "inx": 42},
			{"id": 99999, "inx": 1},
		},
	})
	if out.Code != -1 || !strings.Contains(out.Msg, "转发不存在") {
		t.Fatalf("expected -1 转发不存在, got code=%d msg=%q", out.Code, out.Msg)
	}
	if inx := mustQueryInt64(t, repo, `SELECT inx FROM forward WHERE id = ?`, forwardID); inx != 0 {
		t.Fatalf("forward inx must be unchanged when a later item is invalid, got %d", inx)
	}

	// 对照：全合法时正常落库。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/forward/update-order", map[string]interface{}{
		"forwards": []map[string]interface{}{
			{"id": forwardID, "inx": 42},
		},
	})
	if out.Code != 0 {
		t.Fatalf("expected success, got code=%d msg=%q", out.Code, out.Msg)
	}
	if inx := mustQueryInt64(t, repo, `SELECT inx FROM forward WHERE id = ?`, forwardID); inx != 42 {
		t.Fatalf("expected forward inx=42 after valid update-order, got %d", inx)
	}
}

func TestW2ForwardBatchInvalidIDsCountedAsFailures(t *testing.T) {
	w2Key := "w2-contract-test-key"
	router, repo := setupContractRouter(t, w2Key)
	adminToken := mustAdminToken(t, w2Key)
	now := time.Now().UnixMilli()

	targetTunnelID, _ := seedTunnelDeleteTunnelWithNode(t, repo, now, "w2-batch-target", "w2-batch-node", "26500-26510")

	// batch-delete 只含非法 id：旧行为 successCount=0/failCount=0 全
	// 成功回执；现须 failCount=1 并在 failures 给出该 id。
	out := requestContractEnvelope(t, router, adminToken, "/api/v1/forward/batch-delete", map[string]interface{}{
		"ids": []int64{0},
	})
	if out.Code != 0 {
		t.Fatalf("batch-delete: expected success envelope, got code=%d msg=%q", out.Code, out.Msg)
	}
	result := mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["successCount"]) != 0 || contractValueAsInt64(result["failCount"]) != 1 {
		t.Fatalf("batch-delete: expected successCount=0/failCount=1, got %#v / %#v", result["successCount"], result["failCount"])
	}

	// batch-pause 混非法 id：逐个计数。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/forward/batch-pause", map[string]interface{}{
		"ids": []int64{0, -3},
	})
	if out.Code != 0 {
		t.Fatalf("batch-pause: expected success envelope, got code=%d msg=%q", out.Code, out.Msg)
	}
	result = mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["failCount"]) != 2 {
		t.Fatalf("batch-pause: expected failCount=2, got %#v", result["failCount"])
	}

	// batch-redeploy 同口径。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/forward/batch-redeploy", map[string]interface{}{
		"ids": []int64{-7},
	})
	if out.Code != 0 {
		t.Fatalf("batch-redeploy: expected success envelope, got code=%d msg=%q", out.Code, out.Msg)
	}
	result = mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["failCount"]) != 1 {
		t.Fatalf("batch-redeploy: expected failCount=1, got %#v", result["failCount"])
	}

	// batch-change-tunnel：非法 id 与不存在 id 同批同口径计数。
	out = requestContractEnvelope(t, router, adminToken, "/api/v1/forward/batch-change-tunnel", map[string]interface{}{
		"forwardIds":     []int64{0, 99999},
		"targetTunnelId": targetTunnelID,
	})
	if out.Code != 0 {
		t.Fatalf("batch-change-tunnel: expected success envelope, got code=%d msg=%q", out.Code, out.Msg)
	}
	result = mustTunnelDeleteFailureResult(t, out)
	if contractValueAsInt64(result["successCount"]) != 0 || contractValueAsInt64(result["failCount"]) != 2 {
		t.Fatalf("batch-change-tunnel: expected successCount=0/failCount=2, got %#v / %#v", result["successCount"], result["failCount"])
	}
	failures, ok := result["failures"].([]interface{})
	if !ok || len(failures) != 2 {
		t.Fatalf("batch-change-tunnel: expected two failure entries, got %#v", result["failures"])
	}
}
