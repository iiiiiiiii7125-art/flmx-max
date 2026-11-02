package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// C6-01（LEDGER3，低）：idsFromBodyWithInvalid 的 valid 段只排序不
// 去重，转发批量面按「请求条目」而非「相异 id」处理——批量删回执
// 自相矛盾（行已删却报「转发不存在」）、批量暂停虚报成功数、重部
// 署双执行、nft 恢复配额预留按条目重复自增可挤占同批相异条目。
// 裁定：valid 段排序后过 uniqueInt64s（与姊妹解析器 idsFromBody 及
// R2-040 口径一致），invalid 段一字不动（B08-003 逐条计数契约只覆
// 盖 invalid 段）；同机制相邻面 forwardBatchChangeTunnel 的
// ForwardIDs 中 valid 条目同样去重（invalid 条目仍逐条计失败）。

type fix3BatchResult struct {
	Code int `json:"code"`
	Data struct {
		SuccessCount int `json:"successCount"`
		FailCount    int `json:"failCount"`
		Failures     []struct {
			ID     int64  `json:"id"`
			Reason string `json:"reason"`
		} `json:"failures"`
		PendingCount int `json:"pendingCount"`
	} `json:"data"`
}

func decodeFix3BatchResult(t *testing.T, res *httptest.ResponseRecorder) fix3BatchResult {
	t.Helper()
	var out fix3BatchResult
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode batch response: %v body=%s", err, res.Body.String())
	}
	return out
}

func TestFix3C601IDsFromBodyWithInvalidDedupesValidKeepsInvalid(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/forward/batch-delete", strings.NewReader(`{"ids":[5,5,3,0,-2,0]}`))
	res := httptest.NewRecorder()
	valid, invalid := idsFromBodyWithInvalid(req, res)
	if len(valid) != 2 || valid[0] != 3 || valid[1] != 5 {
		t.Fatalf("valid = %v, want [3 5] (sorted, deduplicated)", valid)
	}
	if len(invalid) != 3 || invalid[0] != 0 || invalid[1] != -2 || invalid[2] != 0 {
		t.Fatalf("invalid = %v, want [0 -2 0] (request order, untouched per B08-003)", invalid)
	}
}

// 批量删 gost：重复 id 只删一次、只计一次成功，不得出现「转发不
// 存在」假失败（该行恰是本批删掉的）。无端口行种子使 gost 删除面
// 不依赖在线 agent（controlForwardServices 对空端口集直接放行，
// 与 C-C6 探针的无端口种子同口径）；带端口行的删除面由 nft 格覆
// 盖（假 manager 成功路径）。
func TestFix3C601ForwardBatchDeleteDuplicateID(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "c601d-a", "198.51.100.81", "10000-20000")
	tunnelID := fx.addTunnel(t, "c601d-t1", nodeA)
	id := fx.addForward(t, "c601d-fwd", tunnelID, nil)

	res := httptest.NewRecorder()
	fx.h.forwardBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{id, id}}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 || len(out.Data.Failures) != 0 {
		t.Fatalf("batch delete duplicated id = %+v body=%s, want success 1 / fail 0 / no failures", out.Data, res.Body.String())
	}
	if _, err := fx.h.getForwardRecord(id); err == nil {
		t.Fatalf("forward %d should have been deleted", id)
	}
}

// 批量删 nft（假 manager 成功路径）：重复 id 第二条旧实现被
// pending 隐藏规则挡在访问检查、记「转发不存在」假失败，而该行
// 已被本批删掉；去重后只处理一次。
func TestFix3C601NftBatchDeleteDuplicateID(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "c601-nft-del-tunnel", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)

	res := httptest.NewRecorder()
	h.forwardBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{forward.ID, forward.ID}}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 || len(out.Data.Failures) != 0 {
		t.Fatalf("nft batch delete duplicated id = %+v body=%s, want success 1 / fail 0 / no failures", out.Data, res.Body.String())
	}
	if _, err := h.getForwardRecord(forward.ID); err == nil {
		t.Fatalf("forward %d should have been deleted", forward.ID)
	}
}

// 批量暂停 gost：重复 id 只处理一次，成功数不得大于实体数。
func TestFix3C601ForwardBatchPauseDuplicateID(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "c601p-a", "198.51.100.82", "10000-20000")
	tunnelID := fx.addTunnel(t, "c601p-t1", nodeA)
	id := fx.addForward(t, "c601p-fwd", tunnelID, map[int64]int{nodeA: 15000})

	res := httptest.NewRecorder()
	fx.h.forwardBatchPause(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{id, id}}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 {
		t.Fatalf("batch pause duplicated id = %+v body=%s, want success 1 / fail 0", out.Data, res.Body.String())
	}
	fwd, err := fx.h.getForwardRecord(id)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.Status != model.ForwardStatusPaused {
		t.Fatalf("forward status = %d, want paused (%d)", fwd.Status, model.ForwardStatusPaused)
	}
}

// 批量重部署 gost：重复 id 只全量同步一次，只计一次。
func TestFix3C601ForwardBatchRedeployDuplicateID(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "c601r-a", "198.51.100.83", "10000-20000")
	tunnelID := fx.addTunnel(t, "c601r-t1", nodeA)
	id := fx.addForward(t, "c601r-fwd", tunnelID, map[int64]int{nodeA: 15000})

	res := httptest.NewRecorder()
	fx.h.forwardBatchRedeploy(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{id, id}}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 {
		t.Fatalf("batch redeploy duplicated id = %+v body=%s, want success 1 / fail 0 (processed exactly once)", out.Data, res.Body.String())
	}
}

// 批量换隧道：重复 id 只处理一次，不得在第二条记「规则已在目标
// 隧道中」假失败。
func TestFix3C601ForwardBatchChangeTunnelDuplicateID(t *testing.T) {
	fx := setupFix3ForwardPorts(t)
	nodeA := fx.addNode(t, "c601c-a", "198.51.100.84", "10000-20000")
	tunnelID := fx.addTunnel(t, "c601c-t1", nodeA)
	targetID := fx.addTunnel(t, "c601c-t2", nodeA)
	id := fx.addForward(t, "c601c-fwd", tunnelID, map[int64]int{nodeA: 15000})

	res := httptest.NewRecorder()
	fx.h.forwardBatchChangeTunnel(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"forwardIds":     []int64{id, id},
		"targetTunnelId": targetID,
	}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 || len(out.Data.Failures) != 0 {
		t.Fatalf("batch change tunnel duplicated id = %+v body=%s, want success 1 / fail 0 / no failures", out.Data, res.Body.String())
	}
	assertPortsEqual(t, fx.portsOf(t, id), map[int64]int{nodeA: 15000})
}

// nft 批量恢复：同一 id 重复出现时配额预留不得重复自增——用户
// Num=1、当前活跃 0 时，重复条目旧实现在第二条即撞「转发数量已
// 达上限」假失败（预留被同一实体重复占用）；去重后只计一次、
// 恢复成功。
func TestFix3C601NftBatchResumeDuplicateIDDoesNotDoubleReserve(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "c601-nft-tunnel", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, time.Now().UnixMilli()); err != nil {
		t.Fatalf("pause forward: %v", err)
	}
	if err := h.repo.DB().Exec(`UPDATE user SET num = 1 WHERE id = 1`).Error; err != nil {
		t.Fatalf("cap user num: %v", err)
	}

	res := httptest.NewRecorder()
	h.forwardBatchResume(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{forward.ID, forward.ID}}))
	out := decodeFix3BatchResult(t, res)
	if out.Code != 0 || out.Data.SuccessCount != 1 || out.Data.FailCount != 0 || len(out.Data.Failures) != 0 {
		t.Fatalf("nft batch resume duplicated id = %+v body=%s, want success 1 / fail 0 / no failures (no double quota reservation)", out.Data, res.Body.String())
	}
	fwd, err := h.getForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fwd.Status != model.ForwardStatusActive {
		t.Fatalf("forward status = %d, want active (%d)", fwd.Status, model.ForwardStatusActive)
	}
}
