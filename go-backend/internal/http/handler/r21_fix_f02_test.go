package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// R21-X-02（F-02）：离线更新的孤儿在重连重推时不被清理。
//
// 机理（R21-X-verdicts V-03，基线 main @ 7d93792 已 pin 死）：
//   - 离线更新只改 DB、未推节点；重连时 redeployLocalNodeRuntime
//     （upgrade.go）调薄封装 syncForwardServices，无更新前快照，
//     sync 内的失势清理（R20-X-01 ①）因 oldForward == nil 直接返回；
//   - 重推把已去引用的新服务配置下发后，节点上 rule_conn_limit_<id>
//     成无引用孤儿，重启后窗口内 DeleteCLimiters 计数 0（真机 Leg B）。
//   - F-01 报告 §6.2 与代码注释（control_plane.go:505–508）
//     “留待下次成功同步”与代码不符——此前根本不存在这样的同步。
//
// 修复（选 ①）：重连重推成功后，若当前记录已无限流，按 ID 删
// rule_conn_limit_<id>（agent 侧删不存在者为 noop，与 R20-X-01 ②
// 同口径）。重推时 DB 已是新态、无更新前快照可传，故不用“带旧快照”
// 的字面形式，而用“当前无限流 ⇒ 失势”判定——服务 climiter 字段只由
// 当前记录的限流值赋值（control_plane.go:2072），当前无限流时节点上
// 同名 limiter 若仍存在必为孤儿。

// TestR21F02RedeployAfterOfflineUpdateDeletesStaleLimiter —— 主回归。
// 建带限流转发 → 把限流清零（模拟离线更新落库后的 DB 态）→ 重连重推
// （redeployLocalNodeRuntime），须在 UpdateService 成功后补发恰 1 次
// DeleteCLimiters {"limiter": "rule_conn_limit_<id>"}。基线红：重推
// 不带旧快照，失势清理直接返回，DeleteCLimiters 计数 0。
func TestR21F02RedeployAfterOfflineUpdateDeletesStaleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)
	_ = r20F01ClearLimits(t, a, forward)

	if ok := a.h.redeployLocalNodeRuntime(1); !ok {
		t.Fatalf("redeployLocalNodeRuntime should succeed, got false")
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent on redeploy")
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

// TestR21F02RedeployKeepsLimitsSendsNoDeleteCLimiters —— 反向守卫。
// 重推的转发仍带限流时不得删 limiter；新 limiter 仍须下发
// （AddCLimiters 同名），user_conn_limit_* 永不在此删除。
func TestR21F02RedeployKeepsLimitsSendsNoDeleteCLimiters(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if ok := a.h.redeployLocalNodeRuntime(1); !ok {
		t.Fatalf("redeployLocalNodeRuntime should succeed, got false")
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
			t.Fatalf("redeploy must not touch user_conn_limit_*, got AddCLimiters %q", n)
		}
	}
	if !found {
		t.Fatalf("expected AddCLimiters %q, got %q", want, names)
	}
}

// TestR21F02RedeployRetrySuccessDeletesStaleLimiter —— 重试腿。
// 首轮重推失败进 retryFailedRedeploys，重试成功后同样须补发
// DeleteCLimiters（否则失败-重试成功的转发仍留孤儿）。
func TestR21F02RedeployRetrySuccessDeletesStaleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 0, 5)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)
	cleared := r20F01ClearLimits(t, a, forward)

	if ok := a.h.retryFailedRedeploys(1, map[int64]struct{}{}, []failedForward{{id: cleared.ID, forward: cleared, err: errors.New("boom")}}); !ok {
		t.Fatalf("retryFailedRedeploys should succeed, got false")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command after retry success, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}

// TestR21F02RedeployTunnelRetrySuccessDeletesStaleLimiter —— 隧道重试腿。
// 首轮隧道失败时其下转发既不同步（首轮 forward 循环跳过）、亦不进
// failedForwards；隧道重试成功后转发在 redeployTunnelAndForwards 内重推
// 成功，但该路径不带失势清理——此处须同样补发 DeleteCLimiters（否则
// 失败-重试成功的隧道下转发仍留孤儿）。基线红：隧道重试分支无任何清理。
func TestR21F02RedeployTunnelRetrySuccessDeletesStaleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)
	cleared := r20F01ClearLimits(t, a, forward)

	if ok := a.h.retryFailedRedeploys(1, map[int64]struct{}{cleared.TunnelID: {}}, nil); !ok {
		t.Fatalf("retryFailedRedeploys should succeed, got false")
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent on tunnel retry")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command after tunnel retry success, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}

// TestR21F02RedeployNeverLimitedForwardSendsNoopDelete —— 口径钉死。
// 从未带过限流的转发在重推时同样会收到一次 DeleteCLimiters：面板在
// 重推时无法区分“从未带限流”与“离线更新去限流”（DB 已是新态、节点
// 侧快照无 CLimiters 字段），按 ID 删是唯一不依赖旧快照的判定；
// agent 侧删不存在者为 noop（与 R20-X-01 ② 已接受的口径一致），
// 故该命令无害。此测试把该取舍钉死，防后人误当 bug“修复”掉。
func TestR21F02RedeployNeverLimitedForwardSendsNoopDelete(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 0, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if ok := a.h.redeployLocalNodeRuntime(1); !ok {
		t.Fatalf("redeployLocalNodeRuntime should succeed, got false")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 (noop) DeleteCLimiters command, got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}
