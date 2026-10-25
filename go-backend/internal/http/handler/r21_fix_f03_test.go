package handler

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R21-X-03（F-03）：forwardForceDelete 的 gost 路径不发任何节点清理命令。
//
// 立案证据（R21-L2 Leg D，真机）：建转发 43 maxConn=5 → force-delete
// code 0、DB 转发回 6 → gost 仍 14 服务、limiter 43 在且被引用、服务仍
// 监听 13285、journal 0 次清理命令；面板审计日志仅记 residue 提示。
// 代码 pin 死：mutations.go forwardForceDelete 先删库；realm 走
// syncRealmNodes；nft 走 reconcile；gost 面无节点命令。
//
// 本文件先红后绿：gost 路径的 force-delete 须与普通删转发一致，下发
// DeleteService + DeleteCLimiters（按 ID 兜底、DeleteService 在先）；
// 节点离线时 force-delete 仍须成功删库（best-effort，不得硬失败——
// 这是 force-delete 区别于普通删除的语义）。

func seedR21F03Forward(t *testing.T, a *cleanupAgent, nodeID int64, maxConn, ipMaxConn int) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := a.h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, a.h, "r21-f03-tunnel", nodeID)
	forwardID, err := a.h.repo.CreateForwardTx(
		1, "admin", "r21-f03-forward", tunnelID, "203.0.113.9:9001", "fifo", now, 1,
		[]int64{nodeID}, 22000, "", nil, maxConn, ipMaxConn, nil, 0, 0, 0,
	)
	if err != nil {
		t.Fatalf("create forward: %v", err)
	}
	forward, err := a.h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatalf("get forward: %v", err)
	}
	return forward
}

func TestR21F03ForceDeleteGostCleansServiceAndLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR21F03Forward(t, a, 1, 5, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	res := httptest.NewRecorder()
	a.h.forwardForceDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected force-delete success, got code=%d msg=%q", code, msg)
	}
	if fr, _ := a.h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward not deleted by force-delete")
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent by force-delete (gost path), got 0")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
	// 顺序：先删服务、后清 climiter——服务仍引用 climiter 时不得先删。
	seenDeleteService := false
	for _, typ := range r19F03CommandTypes(t, a) {
		if typ == "DeleteService" {
			seenDeleteService = true
		}
		if typ == "DeleteCLimiters" && !seenDeleteService {
			t.Fatalf("DeleteCLimiters must be sent after DeleteService")
		}
	}
}

// TestR21F03ForceDeleteOfflineNodeStillDeletesDB：节点离线时 force-delete
// 不得因节点命令失败而硬失败——删库成功、code 0（残留待节点重连后由
// 面板清扫 / agent 自愈收敛；force-delete 的语义即“强制”）。
func TestR21F03ForceDeleteOfflineNodeStillDeletesDB(t *testing.T) {
	a := newCleanupAgent(t)
	// 节点 2 仅有 DB 行、从未连 WS：sendNodeCommand 立即报“节点不在线”。
	if err := a.h.repo.DB().Create(&model.Node{ID: 2, Name: "offline", Secret: "offline-node", ServerIP: "127.0.0.1", Port: "30001", Status: 0}).Error; err != nil {
		t.Fatalf("create offline node: %v", err)
	}
	forward := seedR21F03Forward(t, a, 2, 5, 0)

	res := httptest.NewRecorder()
	a.h.forwardForceDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected force-delete success despite offline node, got code=%d msg=%q", code, msg)
	}
	if fr, _ := a.h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward not deleted by force-delete (offline node)")
	}
}

// TestR21F03NormalDeleteStillCleansServiceAndLimiter：普通删转发回归——
// HTTP 级 forwardDelete 仍发 DeleteService + DeleteCLimiters（与
// force-delete 同口径，F-03 不得改动该路径）。
func TestR21F03NormalDeleteStillCleansServiceAndLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR21F03Forward(t, a, 1, 5, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	res := httptest.NewRecorder()
	a.h.forwardDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.ID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("expected forwardDelete success, got code=%d msg=%q", code, msg)
	}
	if fr, _ := a.h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward not deleted by forwardDelete")
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService from forwardDelete, got 0")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters from forwardDelete, got %d", len(cmds))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}
