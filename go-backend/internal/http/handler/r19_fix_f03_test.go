package handler

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// R19-X-03（机理先行）：删转发未清 climiter。
//
// 取证（见 R19-FIX-F03-REPORT.md）：
//   - 创建/更新只发 AddCLimiters（control_plane.go ensureConnLimiterOnNode），
//     全部删转发漏斗只发 DeleteService，全仓除 federation peer-share 外无任何
//     删转发代码发 DeleteCLimiters；
//   - 真机 R19-L1 已复现：forward 29 建时带 MaxConn 落盘 rule_conn_limit_29，
//     随后 DeleteService 删服务后该 climiter 仍在 gost.json；
//   - agent 启动自愈只按名去重（dedupe.go），从不剪除无引用 limiter；
//     agent 侧 deleteConnLimiter 经 OnUpdate 落盘，命令一旦发出即生效。
//
// 本文件先红后绿：删转发后须向节点补发 DeleteCLimiters
// {"limiter": "rule_conn_limit_<forwardID>"}，且在 DeleteService 之后。

func seedR19F03Forward(t *testing.T, a *cleanupAgent, maxConn, ipMaxConn int) *forwardRecord {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := a.h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, a.h, "r19-f03-tunnel", 1)
	forwardID, err := a.h.repo.CreateForwardTx(
		1, "admin", "r19-f03-forward", tunnelID, "203.0.113.9:9001", "fifo", now, 1,
		[]int64{1}, 22000, "", nil, maxConn, ipMaxConn, nil, 0, 0, 0,
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

func r19F03DeleteCLimiterCommands(t *testing.T, a *cleanupAgent) []cleanupAgentCommand {
	t.Helper()
	return a.commandsOfType("DeleteCLimiters")
}

func r19F03DeleteCLimiterNames(t *testing.T, cmds []cleanupAgentCommand) []string {
	t.Helper()
	var names []string
	for _, cmd := range cmds {
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode DeleteCLimiters payload %s: %v", string(cmd.Data), err)
		}
		name, _ := payload["limiter"].(string)
		names = append(names, name)
	}
	return names
}

func TestR19F03DeleteForwardBatchRemovesConnLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 7, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if err := a.h.deleteForwardServicesOnNodeBatch(forward, 1); err != nil {
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

// r19F03CommandTypes 按发送顺序返回全部命令类型（带锁快照，读端 goroutine
// 仍在追加命令时直接读 a.commands 会触发竞态）。
func r19F03CommandTypes(t *testing.T, a *cleanupAgent) []string {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	types := make([]string, 0, len(a.commands))
	for _, cmd := range a.commands {
		types = append(types, cmd.Type)
	}
	return types
}

func TestR19F03DeleteForwardControlRemovesConnLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 0, 5)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if err := a.h.controlForwardServices(forward, "DeleteService", true); err != nil {
		t.Fatalf("control delete: %v", err)
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters command, got %d", len(cmds))
	}
	if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
}

// R20-X-01 ②（F-01）：无限制转发删除时仍按 ID 名发 DeleteCLimiters。
// 旧断言“无限制即不发”已被 F-01 推翻——删除不得只按当前记录值取名，
// 否则更新清零限流后的删除会漏删孤儿；名只由 ID 决定，agent 侧删不
// 存在者为 noop，重复发送安全。本测试改名并改断言，两漏斗各恰发 1 次
// 同名 DeleteCLimiters。
func TestR19F03DeleteForwardWithoutLimitsStillDeletesRuleLimiterByID(t *testing.T) {
	a := newCleanupAgent(t)
	forward := seedR19F03Forward(t, a, 0, 0)
	want := fmt.Sprintf("rule_conn_limit_%d", forward.ID)

	if err := a.h.deleteForwardServicesOnNodeBatch(forward, 1); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	if cmds := r19F03DeleteCLimiterCommands(t, a); len(cmds) != 1 {
		t.Fatalf("expected exactly 1 DeleteCLimiters for a forward without current limits (R20-X-01 backstop), got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	} else if names := r19F03DeleteCLimiterNames(t, cmds); len(names) != 1 || names[0] != want {
		t.Fatalf("expected DeleteCLimiters limiter %q, got %q", want, names)
	}
	if err := a.h.controlForwardServices(forward, "DeleteService", true); err != nil {
		t.Fatalf("control delete: %v", err)
	}
	if cmds := r19F03DeleteCLimiterCommands(t, a); len(cmds) != 2 {
		t.Fatalf("expected exactly 2 DeleteCLimiters after both delete funnels (R20-X-01 backstop), got %d: %+v", len(cmds), a.commandsOfType("DeleteCLimiters"))
	}
}
