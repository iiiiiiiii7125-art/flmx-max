package handler

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// R20-X-01（F-01）：更新路径孤儿 climiter。
//
// 机理（R20-X-verdicts V-02，基线 main @ b8fb567 已 pin 死）：
//   - 更新转发去掉限流时，更新路径无任何 DeleteCLimiters 生产方
//     （全仓生产方仅 control_plane.go:2257 的删除漏斗）；
//   - 删除按当前记录的限流值取名（forwardRuleConnLimiterName，无限制
//     返 "" 即跳过），更新已清零后删除再也算不出旧名，孤儿永久残留。
//
// 本文件先红后绿，分两件：
//   ① 更新转发时比对新旧配置，失势的 rule_conn_limit_<id> 在服务推送
//      成功后发 DeleteCLimiters（先推服务后删 limiter）；
//   ② 删除路径兜底：不得只按当前记录值取名——名只由转发 ID 决定
//      （agent 侧删不存在者为 noop，重复发送安全），确保更新-删除
//      序列后无孤儿。user_conn_limit_* 为同用户共享，永不在此删除。

// r20F01ClearLimits 模拟一次“更新去掉限流”后的 DB 状态：把转发行的
// MaxConn/IPMaxConn 清零并重新取行（forwardUpdate 落库后的形态）。
func r20F01ClearLimits(t *testing.T, a *cleanupAgent, forward *forwardRecord) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := a.h.repo.UpdateForward(forward.ID, forward.Name, forward.TunnelID, forward.RemoteAddr, forward.Strategy, now, forward.SpeedID, 0, 0, forward.IPSpeedID, forward.ProxyProtocol, forward.ProxyProtocolReceive, forward.ProxyProtocolSend); err != nil {
		t.Fatalf("clear limits: %v", err)
	}
	updated, err := a.h.getForwardRecord(forward.ID)
	if err != nil {
		t.Fatalf("refetch forward: %v", err)
	}
	if updated.MaxConn != 0 || updated.IPMaxConn != 0 {
		t.Fatalf("expected limits cleared, got maxConn=%d ipMaxConn=%d", updated.MaxConn, updated.IPMaxConn)
	}
	return updated
}

// TestR20F01DeleteAfterLimitsClearedSendsDeleteCLimiters —— ② 删除兜底。
// 建带限流转发 → 把限流清零（模拟更新去限流）→ 删转发，仍须发
// DeleteCLimiters {"limiter": "rule_conn_limit_<id>"}。基线红：
// forwardRuleConnLimiterName 按当前记录值取名，清零后返 "" 即跳过。
func TestR20F01DeleteAfterLimitsClearedSendsDeleteCLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)
	cleared := r20F01ClearLimits(t, a, forward)

	if err := a.h.deleteForwardServicesOnNodeBatch(cleared, 1); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}

// TestR20F01UpdateDropsLimitsDeletesStaleLimiter —— ① 更新路径。
// 建带限流转发 → 更新去限流（DB 已清零）→ 同步推送新配置，须在
// UpdateService 成功后补发 DeleteCLimiters {"limiter": "rule_conn_limit_<id>"}。
// 旧记录经 sync 的可选参数传入（forwardUpdate 持有的更新前快照）。
func TestR20F01UpdateDropsLimitsDeletesStaleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	oldForward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", oldForward.ID)
	updated := r20F01ClearLimits(t, a, oldForward)

	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, oldForward); err != nil {
		t.Fatalf("sync update: %v", err)
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
	// 顺序：先推服务、后删 limiter——服务仍引用 climiter 时不得先删。
	seenUpdateService := false
	for _, typ := range r19F03CommandTypes(t, a) {
		if typ == "UpdateService" {
			seenUpdateService = true
		}
		if typ == "DeleteCLimiters" && !seenUpdateService {
			t.Fatalf("DeleteCLimiters must be sent after UpdateService")
		}
	}
}

// TestR20F01UpdateKeepsLimitsSendsNoDeleteCLimiters —— ① 的反向守卫。
// 更新未动限流（新旧均带限流）时不得删 limiter；新 limiter 仍须下发
// （AddCLimiters 同名），user_conn_limit_* 永不在此删除。
func TestR20F01UpdateKeepsLimitsSendsNoDeleteCLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if _, err := a.h.syncForwardServicesWithWarnings(forward, "UpdateService", true, forward); err != nil {
		t.Fatalf("sync update: %v", err)
	}
	if cmds := r19F03DeleteCLimiterCommands(t, a); len(cmds) != 0 {
		t.Fatalf("expected no DeleteCLimiters when limits are kept, got %+v", cmds)
	}
	addCmds := a.commandsOfType("AddCLimiters")
	if len(addCmds) == 0 {
		t.Fatalf("expected AddCLimiters for the kept rule limiter")
	}
	var names []string
	for _, cmd := range addCmds {
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode AddCLimiters payload %s: %v", string(cmd.Data), err)
		}
		name, _ := payload["name"].(string)
		names = append(names, name)
	}
	found := false
	for _, n := range names {
		if n == want {
			found = true
		}
		if n == fmt.Sprintf("user_conn_limit_%d", forward.UserID) {
			t.Fatalf("update must not delete-or-touch user_conn_limit_*, got AddCLimiters %q", n)
		}
	}
	if !found {
		t.Fatalf("expected AddCLimiters %q, got %q", want, names)
	}
}

// TestR20F01UpdateThenDeleteLeavesNoOrphan —— 更新-删除全序列。
// 建带限流 → 更新去限流（① 删失势 limiter）→ 删转发（② 按 ID 名兜底
// 再发一次，agent 侧重复删除为 noop）。两段各恰发 1 次 DeleteCLimiters，
// 且分别落在 UpdateService / DeleteService 之后。
func TestR20F01UpdateThenDeleteLeavesNoOrphan(t *testing.T) {
	a := newCleanupAgent(t)
	oldForward := seedR19F03Forward(t, a, 0, 5)
	want := fmt.Sprintf("rule_conn_limit_%d", oldForward.ID)
	updated := r20F01ClearLimits(t, a, oldForward)

	if _, err := a.h.syncForwardServicesWithWarnings(updated, "UpdateService", true, oldForward); err != nil {
		t.Fatalf("sync update: %v", err)
	}
	if err := a.h.controlForwardServices(updated, "DeleteService", true); err != nil {
		t.Fatalf("control delete: %v", err)
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 2 {
		t.Fatalf("expected exactly 2 DeleteCLimiters commands (update + delete), got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 2 || names[0] != want || names[1] != want {
		t.Fatalf("expected DeleteCLimiters limiters [%q %q], got %q", want, want, names)
	}
	seenUpdate, seenDelete := false, false
	for _, typ := range r19F03CommandTypes(t, a) {
		if typ == "UpdateService" {
			seenUpdate = true
		}
		if typ == "DeleteService" {
			seenDelete = true
		}
		if typ == "DeleteCLimiters" && !seenUpdate {
			t.Fatalf("DeleteCLimiters must be sent after UpdateService")
		}
	}
	if !seenDelete {
		t.Fatalf("expected DeleteService to be sent")
	}
}
