package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R19-X-01（F-01）：删隧道披露残留——R18-X-01 只修了 gost 分支，
// 两处同类点原样残留：
//  1. nft 分支删库失败（tunnel_delete_resolution.go:789）返裸 err——
//     此时 reconcile 已成功（全部规则已下线）、此前转发库行已删；
//     邻接点 MarkForwardPendingDelete 失败（:772）同样裸返；
//  2. deleteTunnelByID 失败（:832 deleteTunnelAndCleanup、
//     mutations.go:2609 tunnelDelete、:2697 tunnelBatchDelete）返裸
//     err——此前 cleanup 已返回 nil，即全部转发已删。
//
// 回归策略：SQLite 触发器模拟中途硬失败（R18 DROP TABLE 手法的同类
// 注入，但可精确定位到第 N 条）：BEFORE DELETE/INSERT 触发器只拦
// 目标行，其余行正常删除。先红后绿：修复前回执只含原始 DB 错误、
// 不含已删披露。

// seedR19F01NftTunnel 在 nft 入口节点上建隧道 + N 条转发（名/端口各异），
// 返回隧道 ID、按 ID 升序的转发清单（与 ListForwardsByTunnel 同序）
// 与 fake manager（用于断言 reconcile 命中次数）。
func seedR19F01NftTunnel(t *testing.T, h *Handler, fixture nftablesTestFixture, names []string) (int64, []model.ForwardRecord, *fakeNftablesManager) {
	t.Helper()
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "r19f01-nft-tunnel", fixture.nodeID)
	now := time.Now().UnixMilli()
	for i, name := range names {
		forwardID, err := h.repo.CreateForwardTx(
			1, "admin", name, tunnelID, "203.0.113.9:8080", "fifo", now, 1,
			[]int64{fixture.nodeID}, 20000+i, "", nil, 0, 0, nil, 0, 0, 0,
		)
		if err != nil {
			t.Fatalf("create forward %s: %v", name, err)
		}
		if _, err := h.getForwardRecord(forwardID); err != nil {
			t.Fatalf("get forward %s: %v", name, err)
		}
	}
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		t.Fatalf("list forwards: %v", err)
	}
	if len(forwards) != len(names) {
		t.Fatalf("expected %d forwards, got %d", len(names), len(forwards))
	}
	return tunnelID, forwards, manager
}

// createR19F01DeleteTrigger 在表上立 BEFORE DELETE 触发器：仅当
// OLD.id = id 时 RAISE(ABORT)，其余行删除不受影响。
func createR19F01DeleteTrigger(t *testing.T, h *Handler, table string, id int64, msg string) {
	t.Helper()
	sql := fmt.Sprintf(`CREATE TRIGGER r19f01_block_%s_delete BEFORE DELETE ON "%s" FOR EACH ROW WHEN OLD.id = %d BEGIN SELECT RAISE(ABORT, '%s'); END;`, table, table, id, msg)
	if err := h.repo.DB().Exec(sql).Error; err != nil {
		t.Fatalf("create delete trigger on %s: %v", table, err)
	}
}

// createR19F01InsertTrigger 在表上立 BEFORE INSERT 触发器：仅当
// NEW.forward_id = id 时 RAISE(ABORT)。
func createR19F01InsertTrigger(t *testing.T, h *Handler, table string, id int64, msg string) {
	t.Helper()
	sql := fmt.Sprintf(`CREATE TRIGGER r19f01_block_%s_insert BEFORE INSERT ON "%s" FOR EACH ROW WHEN NEW.forward_id = %d BEGIN SELECT RAISE(ABORT, '%s'); END;`, table, table, id, msg)
	if err := h.repo.DB().Exec(sql).Error; err != nil {
		t.Fatalf("create insert trigger on %s: %v", table, err)
	}
}

func r19F01ForwardLabel(fwd model.ForwardRecord) string {
	return formatForwardDeleteLabel(fwd)
}

// TestR19F01NftDbDeleteFailureDisclosesDeleted：nft 分支第 2 条
// （N=2/3）删库硬失败——reconcile 已成功、规则已下线，第 1 条库行
// 已删。回执须含「已删除 1 条转发」与第 1 条标识，并注明规则已下线、
// 库行已删与本条记录仍保留。
func TestR19F01NftDbDeleteFailureDisclosesDeleted(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID, forwards, manager := seedR19F01NftTunnel(t, h, fixture, []string{"r19f01-n-a", "r19f01-n-b", "r19f01-n-c"})
	createR19F01DeleteTrigger(t, h, "forward", forwards[1].ID, "simulated nft db delete failure")

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("nft mid-loop db delete failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 1 条转发") || !strings.Contains(msg, r19F01ForwardLabel(forwards[0])) {
		t.Fatalf("receipt must disclose deleted count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "规则已下线") || !strings.Contains(msg, "库行已删") {
		t.Fatalf("receipt must state rules are offline and deleted rows are gone, got msg=%q", msg)
	}
	if !strings.Contains(msg, "数据库记录仍保留") || !strings.Contains(msg, "r19f01-n-b") {
		t.Fatalf("receipt must name the failing forward and state its db row remains, got msg=%q", msg)
	}
	if manager.reconcileCount() != 1 {
		t.Fatalf("reconcile must have run once (rules offline), got %d", manager.reconcileCount())
	}
	assertR18F01ForwardRow(t, h, forwards[0].ID, true)
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01NftDbDeleteFailureFirstForwardDisclosesZero：nft 分支
// 第 1 条即删库失败——回执须含「已删除 0 条转发」，并说明本条规则
// 已下线、记录仍保留；全部转发与隧道保留。
func TestR19F01NftDbDeleteFailureFirstForwardDisclosesZero(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID, forwards, manager := seedR19F01NftTunnel(t, h, fixture, []string{"r19f01-n-a", "r19f01-n-b", "r19f01-n-c"})
	createR19F01DeleteTrigger(t, h, "forward", forwards[0].ID, "simulated nft db delete failure")

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("nft first-forward db delete failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 0 条转发") {
		t.Fatalf("receipt must disclose zero deleted forwards, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "规则已下线") || !strings.Contains(msg, "数据库记录仍保留") {
		t.Fatalf("receipt must state the failing forward's rule is offline and its db row remains, got msg=%q", msg)
	}
	if manager.reconcileCount() != 1 {
		t.Fatalf("reconcile must have run once (rules offline), got %d", manager.reconcileCount())
	}
	for _, fwd := range forwards {
		assertR18F01ForwardRow(t, h, fwd.ID, false)
	}
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01NftMarkPendingFailureDisclosesMarked：nft 分支第 2 条
// MarkForwardPendingDelete 失败（L2 §1a 邻接点）——reconcile 尚未执行，
// 规则未下线；第 1 条仅已标记待删除、库行仍保留。回执须含已标记数与
// 标识，并说明规则未下线。
func TestR19F01NftMarkPendingFailureDisclosesMarked(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID, forwards, manager := seedR19F01NftTunnel(t, h, fixture, []string{"r19f01-n-a", "r19f01-n-b", "r19f01-n-c"})
	createR19F01InsertTrigger(t, h, "nft_pending_delete", forwards[1].ID, "simulated mark pending failure")

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("nft mark-pending failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已标记 1 条转发待删除") || !strings.Contains(msg, r19F01ForwardLabel(forwards[0])) {
		t.Fatalf("receipt must disclose marked count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "规则未下线") {
		t.Fatalf("receipt must state rules are not offline yet (reconcile not run), got msg=%q", msg)
	}
	if manager.reconcileCount() != 0 {
		t.Fatalf("reconcile must not have run, got %d", manager.reconcileCount())
	}
	fr, err := h.repo.GetForwardRecord(forwards[0].ID)
	if err != nil || fr == nil {
		t.Fatalf("first forward row must remain, fr=%+v err=%v", fr, err)
	}
	if fr.Status != model.ForwardStatusPendingDelete {
		t.Fatalf("first forward must be marked pending-delete, status=%d", fr.Status)
	}
	assertR18F01ForwardRow(t, h, forwards[1].ID, false)
	assertR18F01ForwardRow(t, h, forwards[2].ID, false)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01DeleteTunnelByIDFailureDisclosesDeleted：单删路径
// （mutations.go:2609）——gost 离线节点清理成功、全部转发已删后，
// 删隧道行硬失败。回执须含「已删除 1 条转发」与转发标识，并说明
// 隧道记录仍保留；已删转发不回滚。
func TestR19F01DeleteTunnelByIDFailureDisclosesDeleted(t *testing.T) {
	h := openR1B3Handler(t, "r19f01-tunnel-row-fail.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)
	createR19F01DeleteTrigger(t, h, "tunnel", tunnelID, "simulated tunnel row delete failure")

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("tunnel row delete failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if !strings.Contains(msg, "已删除 1 条转发") || !strings.Contains(msg, r19F01ForwardLabel(model.ForwardRecord{ID: forward.ID, Name: forward.Name})) {
		t.Fatalf("receipt must disclose deleted count and identity, got msg=%q body=%s", msg, res.Body.String())
	}
	if !strings.Contains(msg, "隧道记录仍保留") {
		t.Fatalf("receipt must state the tunnel row remains, got msg=%q", msg)
	}
	assertR18F01ForwardRow(t, h, forward.ID, true)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01DeleteTunnelAndCleanupFailureDisclosesDeleted：
// deleteTunnelAndCleanup 内删隧道行失败（:832）——delete-with-forwards
// 等路径经此函数，错误文本本身须含已删披露（不依赖外层包装）。
func TestR19F01DeleteTunnelAndCleanupFailureDisclosesDeleted(t *testing.T) {
	h := openR1B3Handler(t, "r19f01-cleanup-tunnel-row-fail.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)
	createR19F01DeleteTrigger(t, h, "tunnel", tunnelID, "simulated tunnel row delete failure")

	err := h.deleteTunnelAndCleanup(tunnelID)
	if err == nil {
		t.Fatalf("tunnel row delete failure must return an error")
	}
	if !strings.Contains(err.Error(), "已删除 1 条转发") || !strings.Contains(err.Error(), r19F01ForwardLabel(model.ForwardRecord{ID: forward.ID, Name: forward.Name})) {
		t.Fatalf("error must disclose deleted count and identity, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "隧道记录仍保留") {
		t.Fatalf("error must state the tunnel row remains, got %q", err.Error())
	}
	assertR18F01ForwardRow(t, h, forward.ID, true)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01BatchDeleteTunnelByIDFailureDisclosesDeleted：批量删隧道
// 同型点（mutations.go:2697）——失败明细 Reason 须含已删披露。
func TestR19F01BatchDeleteTunnelByIDFailureDisclosesDeleted(t *testing.T) {
	h := openR1B3Handler(t, "r19f01-batch-tunnel-row-fail.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)
	createR19F01DeleteTrigger(t, h, "tunnel", tunnelID, "simulated tunnel row delete failure")

	res := httptest.NewRecorder()
	h.tunnelBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{tunnelID}}))
	var payload struct {
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
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if payload.Data.FailCount != 1 || len(payload.Data.Failures) != 1 {
		t.Fatalf("expected 1 batch failure, got %+v body=%s", payload.Data, res.Body.String())
	}
	reason := payload.Data.Failures[0].Reason
	if !strings.Contains(reason, "已删除 1 条转发") || !strings.Contains(reason, r19F01ForwardLabel(model.ForwardRecord{ID: forward.ID, Name: forward.Name})) {
		t.Fatalf("batch failure reason must disclose deleted forwards, got %q body=%s", reason, res.Body.String())
	}
	assertR18F01ForwardRow(t, h, forward.ID, true)
	assertR18F01TunnelRow(t, h, tunnelID, false)
}

// TestR19F01NftSuccessPathUnchanged：nft 分支成功路径行为不变——
// 接口成功、隧道与全部转发均删除、reconcile 恰好 1 次。
func TestR19F01NftSuccessPathUnchanged(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID, forwards, manager := seedR19F01NftTunnel(t, h, fixture, []string{"r19f01-n-a", "r19f01-n-b", "r19f01-n-c"})

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("nft success path must succeed, got code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if manager.reconcileCount() != 1 {
		t.Fatalf("expected 1 reconcile, got %d", manager.reconcileCount())
	}
	for _, fwd := range forwards {
		assertR18F01ForwardRow(t, h, fwd.ID, true)
	}
	assertR18F01TunnelRow(t, h, tunnelID, true)
}

// TestR19F01GostSuccessPathUnchanged：gost 离线单删成功路径行为不变
// （deleteTunnelByID 修复不得改动成功分支）。
func TestR19F01GostSuccessPathUnchanged(t *testing.T) {
	h := openR1B3Handler(t, "r19f01-gost-success.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("gost success path must succeed, got code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	assertR18F01ForwardRow(t, h, forward.ID, true)
	assertR18F01TunnelRow(t, h, tunnelID, true)
}
