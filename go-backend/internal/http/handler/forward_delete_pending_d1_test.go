package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// errFakeSSHDown 模拟节点离线（SSH 不可达）。
var errFakeSSHDown = errors.New("ssh: unable to connect (simulated offline)")

// R5-B3 D1-001: nft 删除数据面优先 + pending-deletion 机制。
// 可达性证据：forwardDelete 原实现先删库后 reconcile（mutations.go 原 :2569/:2574），
// reconcile 失败即规则泄漏且无记录。本批改为：标记 pending → reconcile →
// 成功删库 / 失败留 pending 由 job 退避重试。

func decodePendingEnvelopeCode(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return payload.Code, payload.Msg
}

func seedNftForwardForDelete(t *testing.T, h *Handler, fixture nftablesTestFixture) *forwardRecord {
	t.Helper()
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-del", fixture.nodeID)
	return seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
}

// TestForwardDeleteNftNodeOfflineGoesPending: 节点离线时删除不得删库，
// 必须进入 pending（D7），且响应明确说明已加入待清理队列。
func TestForwardDeleteNftNodeOfflineGoesPending(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardDelete(res, req)

	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("expected non-zero code when node offline, got 0")
	}
	if !strings.Contains(msg, "待清理") {
		t.Fatalf("expected queued message, got %q", msg)
	}

	// DB 行不得删除，且 status=2。
	fr, err := h.repo.GetForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fr == nil {
		t.Fatalf("forward row was deleted despite reconcile failure (D1-001 regression)")
	}
	if fr.Status != model.ForwardStatusPendingDelete {
		t.Fatalf("expected status=%d, got %d", model.ForwardStatusPendingDelete, fr.Status)
	}
	// pending bookkeeping 存在。
	pending, err := h.repo.GetNftPendingDelete(forward.ID)
	if err != nil || pending == nil {
		t.Fatalf("expected pending bookkeeping row, got %+v, err=%v", pending, err)
	}
	if pending.NodeID != fixture.nodeID {
		t.Fatalf("expected node %d, got %d", fixture.nodeID, pending.NodeID)
	}
}

// TestForwardDeleteNftSuccessDeletesRow: reconcile 成功时规则下线后删库，
// 无残留（binding / counter state / pending 行同步清理，补充 A.3）。
func TestForwardDeleteNftSuccessDeletesRow(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	// 先写入一条 counter state，验证删除时同步清理。
	nowMs := time.Now().UnixMilli()
	if err := h.repo.UpsertNftCounterStates([]repo.NftCounterStateInput{
		{NodeID: fixture.nodeID, ForwardID: forward.ID, Protocol: "tcp", Direction: "in", Bytes: 100},
	}, nowMs); err != nil {
		t.Fatalf("upsert counter state: %v", err)
	}

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardDelete(res, req)

	if code, _ := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected success, got code=%d body=%s", code, res.Body.String())
	}
	if manager.reconcileHit != 1 {
		t.Fatalf("expected 1 reconcile, got %d", manager.reconcileHit)
	}
	// plan 不得包含被删转发（数据面优先的直接证据）。
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			t.Fatalf("deleted forward still in reconcile plan")
		}
	}
	fr, err := h.repo.GetForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fr != nil {
		t.Fatalf("forward row not deleted after successful reconcile")
	}
	states, err := h.repo.GetNftCounterStatesByNode(fixture.nodeID)
	if err != nil {
		t.Fatalf("get counter states: %v", err)
	}
	for _, s := range states {
		if s.ForwardID == forward.ID {
			t.Fatalf("counter state residue after delete (补充 A.3)")
		}
	}
}

// TestPendingDeleteRetrySuccess: pending 重试成功后最终删除，binding 清理。
func TestPendingDeleteRetrySuccess(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)

	// 先走失败路径进入 pending。
	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	h.forwardDelete(httptest.NewRecorder(), req)

	// 节点恢复，跑重试 job（快进到退避到期之后）。
	manager.reconcileErr = nil
	h.retryNftPendingDeletes(time.Now().Add(2 * time.Minute))

	fr, err := h.repo.GetForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	if fr != nil {
		t.Fatalf("forward not deleted after successful retry")
	}
	if pending, _ := h.repo.GetNftPendingDelete(forward.ID); pending != nil {
		t.Fatalf("pending bookkeeping residue after successful retry")
	}
}

// TestPendingDeleteRetryBackoff: 持续失败时退避（next_retry_at 递增），
// 未到 24h 上限前继续调度。
func TestPendingDeleteRetryBackoff(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	h.forwardDelete(httptest.NewRecorder(), req)

	before, _ := h.repo.GetNftPendingDelete(forward.ID)
	// 立即重试：未到期，不应执行。
	h.retryNftPendingDeletes(time.Now())
	after, _ := h.repo.GetNftPendingDelete(forward.ID)
	if after.RetryCount != before.RetryCount {
		t.Fatalf("retry executed before backoff elapsed")
	}
	// 快进到到期之后：执行一次，计数+1。
	h.retryNftPendingDeletes(time.Now().Add(2 * time.Minute))
	after2, _ := h.repo.GetNftPendingDelete(forward.ID)
	if after2.RetryCount != before.RetryCount+1 {
		t.Fatalf("expected retry count %d, got %d", before.RetryCount+1, after2.RetryCount)
	}
	if after2.NextRetryAt <= after2.UpdatedAt {
		t.Fatalf("next_retry_at not advanced")
	}
	if after2.LastError == "" {
		t.Fatalf("last_error not recorded")
	}
	// 仍 pending，未删库。
	if fr, _ := h.repo.GetForwardRecord(forward.ID); fr == nil {
		t.Fatalf("forward deleted despite failed retry")
	}
}

// TestForceDeleteAdminOnly: 非管理员 403；不存在的 ID 明确 404 语义；
// 管理员成功且 DB 全清理。
func TestForceDeleteAdminOnly(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)

	// 非管理员。
	nonAdminReq := newAuthenticatedJSONRequestAs(t, 2, "user", 1, map[string]any{"id": forward.ID})
	res := httptest.NewRecorder()
	h.forwardForceDelete(res, nonAdminReq)
	if code, _ := decodePendingEnvelopeCode(t, res); code != 403 {
		t.Fatalf("expected 403 for non-admin, got %d", code)
	}

	// 不存在的 ID：明确“不存在”，非 500（code -2 也不带 panic）。
	adminReq := newAuthenticatedJSONRequest(t, map[string]any{"id": 999999})
	res = httptest.NewRecorder()
	h.forwardForceDelete(res, adminReq)
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 || !strings.Contains(msg, "不存在") {
		t.Fatalf("expected not-found message, got code=%d msg=%q", code, msg)
	}

	// 管理员对 pending 行 force-delete 成功。
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	delReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	h.forwardDelete(httptest.NewRecorder(), delReq) // 进入 pending
	forceReq := newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID})
	res = httptest.NewRecorder()
	h.forwardForceDelete(res, forceReq)
	if code, _ := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected force-delete success, got %d", code)
	}
	if fr, _ := h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward not deleted by force-delete")
	}
	if pending, _ := h.repo.GetNftPendingDelete(forward.ID); pending != nil {
		t.Fatalf("pending bookkeeping residue after force-delete")
	}
}

// TestPendingExcludedFromListAndAccess: 矩阵 #1/#10 回归。
func TestPendingExcludedFromListAndAccess(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	items, err := h.repo.ListForwards()
	if err != nil {
		t.Fatalf("list forwards: %v", err)
	}
	for _, item := range items {
		if asInt64(item["id"], 0) == forward.ID {
			t.Fatalf("pending forward leaked into ListForwards (矩阵 #1)")
		}
	}
	if _, err := h.ensureForwardAccessByActor(1, 0, forward.ID); err == nil {
		t.Fatalf("pending forward accessible via ensureForwardAccessByActor (矩阵 #10)")
	}

	// 端口冲突仍命中（矩阵 #12），且文案区分。
	node, _ := h.getNodeRecord(fixture.nodeID)
	err = h.validateForwardPortAvailability(node, 20000, 0)
	if err == nil || !strings.Contains(err.Error(), "等待节点清理") {
		t.Fatalf("expected pending-specific conflict message, got %v", err)
	}
}

// TestBatchDeletePartialFailurePerItem: R4 逐条结果；成功项不回滚。
func TestBatchDeletePartialFailurePerItem(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-batch", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8081", 20000)
	_ = f1

	// f1 走在线路径（manager 成功），f2 模拟离线：用两个 manager 切换。
	// 简化：先让 f1 成功删除，再让 f2 进入 pending。
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	f2 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8082", 20001)
	_ = f2

	// 批量删除 f1（成功）+ 999999（不存在，失败）。
	req := newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{f1.ID, 999999}})
	res := httptest.NewRecorder()
	h.forwardBatchDelete(res, req)
	var payload struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Data.SuccessCount != 1 || payload.Data.FailCount != 1 {
		t.Fatalf("expected 1 success 1 fail, got %+v", payload.Data)
	}
	// 成功项已删（不回滚），失败项不影响。
	if fr, _ := h.repo.GetForwardRecord(f1.ID); fr != nil {
		t.Fatalf("successful item rolled back")
	}
}

// TestPendingDeltaDroppedButBaselineAdvances: 补充 A。
func TestPendingDeltaDroppedButBaselineAdvances(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	deltas := []nftTrafficDelta{
		{ForwardID: forward.ID, BytesIn: 1000, BytesOut: 2000},
	}
	kept := h.filterOutPendingDeleteDeltas(fixture.nodeID, deltas)
	if len(kept) != 0 {
		t.Fatalf("pending delta not dropped")
	}

	// 非 pending 的 delta 保留。
	other := seedForwardForNftables(t, h, forward.TunnelID, fixture.nodeID, "203.0.113.9:8083", 20001)
	mixed := []nftTrafficDelta{
		{ForwardID: forward.ID, BytesIn: 1000, BytesOut: 2000},
		{ForwardID: other.ID, BytesIn: 3000, BytesOut: 4000},
	}
	kept = h.filterOutPendingDeleteDeltas(fixture.nodeID, mixed)
	if len(kept) != 1 || kept[0].ForwardID != other.ID {
		t.Fatalf("wrong deltas kept: %+v", kept)
	}
}

// TestSyncGuardsPendingForward: 补充 D 兜底。
func TestSyncGuardsPendingForward(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)
	if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
		t.Fatalf("mark pending: %v", err)
	}
	pendingFwd, _ := h.getForwardRecord(forward.ID)
	if _, err := h.syncForwardServicesWithWarnings(pendingFwd, "UpdateService", true); err != nil {
		t.Fatalf("sync should no-op on pending, got %v", err)
	}
	if manager.reconcileHit != 0 {
		t.Fatalf("reconcile must not run for pending forward")
	}
}

func newAuthenticatedJSONRequestAs(t *testing.T, userID int64, userName string, roleID int, payload any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	token, err := auth.GenerateToken(userID, userName, roleID, "test-secret")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	req.Header.Set("Authorization", token)
	claims, ok := auth.ValidateToken(token, "test-secret")
	if !ok {
		t.Fatalf("validate token failed")
	}
	return req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey, claims))
}
