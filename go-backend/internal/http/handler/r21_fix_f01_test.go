package handler

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// R21-X-01（F-01）：switchTunnelEngine gost→realm 遗留 rule climiter 孤儿。
//
// 机理（R21-X-ledger §三，基线 main @ 7d93792 已 pin 死）：
//   - switchTunnelEngine gost→realm 分支（realm.go:317）调
//     deleteForwardServicesOnNode（control_plane.go:591–601），只发
//     DeleteService，从不发 DeleteCLimiters；
//   - 删转发漏斗（deleteForwardServicesOnNodeBatch / controlForwardServices
//     的 DeleteService 分支）则在服务已删后按 ID 补发 DeleteCLimiters
//     （R19-X-03 + R20-X-01 ② 兜底）；
//   - 切换路径是唯一一条“服务已删、limiter 未删”的产品路径，
//     rule_conn_limit_<id> 成无引用孤儿（F-04 重启/SIGHUP 可兜底剪除，
//     故 P3）。
//
// 本文件先红后绿：切换引擎时须像删转发一样清掉旧引擎的 conn limiter。
// 反向 realm→gost 同理核查：realm 渲染无 limit 字段、切换回 gost 时
// syncForwardServices 重建服务并经 ensureConnLimiterOnNode 重建同名
// rule limiter，不留同型残留（断言 AddCLimiters 含该名、且切换回后不
// 再多发 DeleteCLimiters）。

// r21F01SeedSwitchForwards 建两条同隧道转发：一条带限流（maxConn=7），
// 一条从未带限流（按 ID 兜底亦须发一次 DeleteCLimiters，R20-X-01 ②）。
func r21F01SeedSwitchForwards(t *testing.T, a *cleanupAgent) (tunnelID int64, limited, unlimited *forwardRecord) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := a.h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tunnelID = seedTunnelForNftables(t, a.h, "r21-f01-tunnel", 1)
	mk := func(name string, port, maxConn, ipMaxConn int) *forwardRecord {
		t.Helper()
		forwardID, err := a.h.repo.CreateForwardTx(
			1, "admin", name, tunnelID, "203.0.113.9:9001", "fifo", now, 1,
			[]int64{1}, port, "", nil, maxConn, ipMaxConn, nil, 0, 0, 0,
		)
		if err != nil {
			t.Fatalf("create forward %s: %v", name, err)
		}
		forward, err := a.h.getForwardRecord(forwardID)
		if err != nil {
			t.Fatalf("get forward %s: %v", name, err)
		}
		return forward
	}
	limited = mk("r21-f01-limited", 22000, 7, 0)
	unlimited = mk("r21-f01-unlimited", 22001, 0, 0)
	return tunnelID, limited, unlimited
}

// TestR21F01EngineSwitchGostToRealmDeletesRuleLimiter —— 主回归。
// 建带限流 gost 转发 → 切换引擎为 realm → 须发 DeleteCLimiters
// {"limiter": "rule_conn_limit_<id>"}，且在 DeleteService 之后。
// 基线红：switchTunnelEngine 只调 deleteForwardServicesOnNode，
// 0 次 DeleteCLimiters。
func TestR21F01EngineSwitchGostToRealmDeletesRuleLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID, limited, unlimited := r21F01SeedSwitchForwards(t, a)
	want := map[string]bool{
		fmt.Sprintf("rule_conn_limit_%d", limited.ID):   true,
		fmt.Sprintf("rule_conn_limit_%d", unlimited.ID): true,
	}

	// 调用方（tunnelUpdate）先落库新引擎再调 switchTunnelEngine；
	// ListRealmForwardsByNode 只认 engine='realm' 的隧道。
	if err := a.h.repo.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("set engine realm: %v", err)
	}
	if err := a.h.switchTunnelEngine(tunnelID, false, []int64{1}, []int64{1}); err != nil {
		t.Fatalf("switch to realm: %v", err)
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent on engine switch")
	}
	cmds := r19F03DeleteCLimiterCommands(t, a)
	if len(cmds) != 2 {
		t.Fatalf("expected exactly 2 DeleteCLimiters commands (one per forward), got %d: %+v", len(cmds), cmds)
	}
	names := r19F03DeleteCLimiterNames(t, cmds)
	for _, name := range names {
		if !want[name] {
			t.Fatalf("unexpected DeleteCLimiters limiter %q, want one of %v", name, want)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing DeleteCLimiters for limiters %v", want)
	}
	// 顺序：每条转发的 DeleteCLimiters 须在其 DeleteService 之后——
	// 服务仍引用 climiter 时不得先删。
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

// TestR21F01EngineSwitchRealmToGostRecreatesLimiter —— 反向回归。
// gost→realm 清掉 limiter 后切回 gost：syncForwardServices 须重建
// 服务并经 AddCLimiters 重建同名 rule limiter，且切回过程不再多发
// DeleteCLimiters（无同型残留可清）。
func TestR21F01EngineSwitchRealmToGostRecreatesLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	tunnelID, limited, _ := r21F01SeedSwitchForwards(t, a)
	want := fmt.Sprintf("rule_conn_limit_%d", limited.ID)

	if err := a.h.repo.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("set engine realm: %v", err)
	}
	if err := a.h.switchTunnelEngine(tunnelID, false, []int64{1}, []int64{1}); err != nil {
		t.Fatalf("switch to realm: %v", err)
	}
	if got := len(r19F03DeleteCLimiterCommands(t, a)); got != 2 {
		t.Fatalf("switch to realm: expected 2 DeleteCLimiters, got %d", got)
	}

	if err := a.h.repo.DB().Exec(`UPDATE tunnel SET engine = '' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("set engine gost: %v", err)
	}
	if err := a.h.switchTunnelEngine(tunnelID, true, []int64{1}, []int64{1}); err != nil {
		t.Fatalf("switch back to gost: %v", err)
	}
	if got := len(a.commandsOfType("UpdateService")); got == 0 {
		t.Fatalf("expected UpdateService to be sent on switch back to gost")
	}
	// 切回后 limiter 被重建：AddCLimiters 含同名 rule limiter。
	found := false
	for _, cmd := range a.commandsOfType("AddCLimiters") {
		var payload map[string]interface{}
		if err := json.Unmarshal(cmd.Data, &payload); err != nil {
			t.Fatalf("decode AddCLimiters payload %s: %v", string(cmd.Data), err)
		}
		if name, _ := payload["name"].(string); name == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected AddCLimiters for %q on switch back to gost, got %+v", want, a.commandsOfType("AddCLimiters"))
	}
	// 切回过程无同型残留：DeleteCLimiters 总数仍为去程的 2 次。
	if got := len(r19F03DeleteCLimiterCommands(t, a)); got != 2 {
		t.Fatalf("switch back to gost: expected DeleteCLimiters total to stay 2, got %d", got)
	}
}

// TestR21F01DeleteForwardServicesOnNodeAloneKeepsLimiter —— 设计锁定：
// deleteForwardServicesOnNode 本身仍只删服务、不清 limiter。它的另两
// 个调用方（syncForwardServices 的 UpdateService 回退、默认监听回退）
// 是“删旧服务后立即重建同一服务”的重建路径，重建的服务仍引用原
// limiter——若 helper 内清 limiter，重建服务将挂上已删的 limiter。
// 因此 F-01 的清理只落在 switchTunnelEngine 的切换点（服务永久迁
// 出 gost），不在共享 helper 内。
func TestR21F01DeleteForwardServicesOnNodeAloneKeepsLimiter(t *testing.T) {
	a := newCleanupAgent(t)
	_, limited, _ := r21F01SeedSwitchForwards(t, a)

	if err := a.h.deleteForwardServicesOnNode(limited, 1); err != nil {
		t.Fatalf("delete services on node: %v", err)
	}
	if got := len(a.commandsOfType("DeleteService")); got == 0 {
		t.Fatalf("expected DeleteService to be sent")
	}
	if got := len(r19F03DeleteCLimiterCommands(t, a)); got != 0 {
		t.Fatalf("deleteForwardServicesOnNode alone must not send DeleteCLimiters (rebuild callers re-add the same service), got %d", got)
	}
}
