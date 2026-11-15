// F4 移交：配额释放路径把离线节点当成功吞掉、仍把行标 Active。
// 真机实证：节点 14 全程离线，reset 后转发 29 由 0→1，agent 从未收到 resume。
// 根因不在 C-05 的 V6（它只看到 controlForwardServices 的返回值），而在
// controlForwardServices 非删除分支：离线/超时错对全部命令一律 continue
// 吞掉、返回 nil，调用方随即 ActivateForwardIfNotPending 标 Active，
// C-05 的 Resumed 清单又把它从 PausedForwardIDs 摘除——无重试载体。
// 根治：离线/超时只许 PauseService 吞（数据面已停、DB 暂停态即权威态）；
// ResumeService 必须明错返回，释放路径记 Failed（reason=node_offline）、
// 行保持暂停、留在待恢复清单，backstop 在节点上线后重试（ResumeService
// 幂等，未暂停即跳过，重试收敛）。
// 同机制全集：ResumeService 调用方共三处，均在控制函数返回值上推进 DB
// 状态——user_quota.go 释放、mutations.go 手动恢复、mutations.go 批量
// 恢复——本修复在控制函数一处堵住，三路同得；PauseService 四处调用方
// （含 syncForwardServicesWithWarnings 尾部保暂停）行为不变。

package handler

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// f4SeedForwardOnNode creates an active forward on tunnel 41 with one port
// on the given fake-agent node (node 1 online, node 2 offline in the
// nodeSwapFakeAgent harness). Shape mirrors c04SeedForward.
func f4SeedForwardOnNode(t *testing.T, a *nodeSwapFakeAgent, nodeID int64) *forwardRecord {
	t.Helper()
	r := a.h.repo
	nowMs := time.Now().UnixMilli()
	userID, err := r.CreateUser("f4-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(41, 'f4-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 41, 10, 100000, 0, 0, 0, 1, ?, 1)`, userID, nowMs+86400000).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'f4-user', 'f4-fwd', 41, '203.0.113.1:443', 'fifo', 0, 0, ?, ?, 1, 0)`, userID, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	fwdID := mustLastInsertID(t, r, "f4-fwd")
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, ?, 24510)`, fwdID, nodeID).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}
	return &forwardRecord{ID: fwdID, UserID: userID, TunnelID: 41, Status: 1}
}

// 控制层：离线节点上的 ResumeService 必须明错、且 agent 收不到命令；
// 同节点上的 PauseService 仍返回 nil（吞掉是正确语义，见文件头注释）。
func TestF4ResumeOfflineNodeFailsLoudPauseStillSwallows(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	forward := f4SeedForwardOnNode(t, a, 2)

	err := a.h.controlForwardServices(forward, "ResumeService", false)
	if err == nil {
		t.Fatalf("F4: ResumeService on an offline node must fail, got nil (row would be marked Active with no resume delivered)")
	}
	if !strings.Contains(err.Error(), "不在线") {
		t.Fatalf("F4: offline resume error must name the offline node, got %v", err)
	}
	if got := a.commandsOfType("ResumeService"); len(got) != 0 {
		t.Fatalf("F4: offline node must receive no ResumeService command, got %+v", got)
	}

	if err := a.h.controlForwardServices(forward, "PauseService", false); err != nil {
		t.Fatalf("F4: PauseService on an offline node must still swallow the offline error, got %v", err)
	}
}

// 释放路径（F4 原案复现）：离线节点上的暂停转发，reset 后必须保持
// 暂停、留在待恢复清单、进 Failed（reason=node_offline），agent 从未
// 收到 resume；待恢复清单仍列该用户，backstop 有重试载体。
func TestF4ReleaseOfflineNodeStaysPausedAndPending(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	now := time.Now()
	userID, forwardID := c05SeedBlockedUser(t, a.h.repo, now, false)
	if err := a.h.repo.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, 2, 24510)`, forwardID).Error; err != nil {
		t.Fatalf("insert forward_port on offline node: %v", err)
	}

	release, err := a.h.repo.ResetUserQuotaUsage(userID, "all", now)
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	result := a.h.applyUserQuotaRelease(release, now.UnixMilli())
	if len(result.Failed) != 1 || result.Failed[0].Reason != "node_offline" || result.Failed[0].ForwardID != forwardID {
		t.Fatalf("F4: release result = %+v, want one node_offline failure for forward %d", result, forwardID)
	}
	if len(result.Resumed) != 0 {
		t.Fatalf("F4: resumed = %v, want none (no resume was delivered)", result.Resumed)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("F4: dropped = %v, want none", result.Dropped)
	}
	if got := c05ForwardStatus(t, a.h.repo, forwardID); got != 0 {
		t.Fatalf("F4: forward %d status = %d after offline release, want 0 (still paused, never Active)", forwardID, got)
	}
	if got := c05PausedList(t, a.h.repo, userID); got != fmt.Sprintf("%d", forwardID) {
		t.Fatalf("F4: PausedForwardIDs = %q after offline release, want %q (retry basis must survive)", got, fmt.Sprintf("%d", forwardID))
	}
	pending, err := a.h.repo.ListPendingQuotaReleases(now)
	if err != nil {
		t.Fatalf("list pending releases: %v", err)
	}
	if len(pending) != 1 || pending[0].UserID != userID {
		t.Fatalf("F4: pending releases = %+v, want one for user %d (backstop retry vehicle)", pending, userID)
	}
	if got := a.commandsOfType("ResumeService"); len(got) != 0 {
		t.Fatalf("F4: offline node must receive no ResumeService command, got %+v", got)
	}
}
