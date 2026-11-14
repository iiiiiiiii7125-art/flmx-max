package handler

import (
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// NEW-V1-01：内核下发过渡期版本门回归测试。
//
// 先红后绿：本文件引用的门 API（agentSupportsNftIsolation /
// requireNftIsolatedAgent / applyKernelWithMode 内的门调用）在门落
// 地前不存在，go test 应以编译失败变红；门落地后全绿。
// 接线红证（门调用缺失时）：TestApplyKernelBlocksOldAgent 在无门
// 的基线上收到的不是“版本过旧”而是下发侧“不在线”，断言失败。

func TestAgentSupportsNftIsolation(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"2.1.13", true},
		{"2.1.13-cand (debian/amd64)", true},
		{"v2.1.13", true},
		{"2.2.0", true},
		{"2.1.14", true},
		{"2.1.12", false},
		{"2.1.12-cand (debian/amd64)", false},
		{"2.1.11", false},
		{"2.1.5", false},
		{"dev", false},
		{"", false},
		{"abc", false},
		{"2.1", false},
	}
	for _, c := range cases {
		if got := agentSupportsNftIsolation(c.raw); got != c.want {
			t.Errorf("agentSupportsNftIsolation(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// setNftGateNodeVersion 经握手落库口径改写节点上报版本。
func setNftGateNodeVersion(t *testing.T, r *repo.Repository, nodeID int64, version string) {
	t.Helper()
	v := version
	if err := r.UpdateNodeOnlineReport(nodeID, repo.NodeOnlineReport{Status: 1, Version: &v}); err != nil {
		t.Fatalf("UpdateNodeOnlineReport: %v", err)
	}
}

func TestRequireNftIsolatedAgent(t *testing.T) {
	h := setupHandlerTestRepo(t)
	r := handlerTestRepo(t, h)

	old := seedRelayTargetNode(t, r, "nft-gate-old", "198.51.100.71")
	setNftGateNodeVersion(t, r, old, "2.1.12")
	if err := h.requireNftIsolatedAgent(old); err == nil || !strings.Contains(err.Error(), "版本过旧") {
		t.Fatalf("old agent 2.1.12 must be blocked, got err=%v", err)
	}
	if err := h.requireNftIsolatedAgent(old); err == nil || !strings.Contains(err.Error(), "2.1.13") {
		t.Fatalf("block message must name the minimum version, got err=%v", err)
	}

	cand := seedRelayTargetNode(t, r, "nft-gate-cand", "198.51.100.72")
	setNftGateNodeVersion(t, r, cand, "2.1.13-cand (debian/amd64)")
	if err := h.requireNftIsolatedAgent(cand); err != nil {
		t.Fatalf("candidate 2.1.13-cand must pass, got err=%v", err)
	}

	next := seedRelayTargetNode(t, r, "nft-gate-next", "198.51.100.73")
	setNftGateNodeVersion(t, r, next, "2.2.0")
	if err := h.requireNftIsolatedAgent(next); err != nil {
		t.Fatalf("agent 2.2.0 must pass, got err=%v", err)
	}

	// 空串版本（从未上报）：不设此门，返回 nil。
	unreported := seedRelayTargetNode(t, r, "nft-gate-unreported", "198.51.100.74")
	setNftGateNodeVersion(t, r, unreported, "")
	if err := h.requireNftIsolatedAgent(unreported); err != nil {
		t.Fatalf("empty version must not be gated, got err=%v", err)
	}

	// dev：不可解析，fail-closed。
	dev := seedRelayTargetNode(t, r, "nft-gate-dev", "198.51.100.75")
	setNftGateNodeVersion(t, r, dev, "dev")
	if err := h.requireNftIsolatedAgent(dev); err == nil || !strings.Contains(err.Error(), "版本过旧") {
		t.Fatalf("dev agent must be blocked fail-closed, got err=%v", err)
	}

	// 不存在的节点：返回节点不存在。
	if err := h.requireNftIsolatedAgent(999999); err == nil || !strings.Contains(err.Error(), "节点不存在") {
		t.Fatalf("missing node must error, got err=%v", err)
	}
}

// TestApplyKernelBlocksOldAgent 接线证明：旧版 agent 的 KernelApply
// 在门处即被拦下（版本过旧），绝不到达下发侧——无门时同一调用
// 会走到发送侧并以“不在线”失败。
func TestApplyKernelBlocksOldAgent(t *testing.T) {
	h := setupHandlerTestRepo(t)
	r := handlerTestRepo(t, h)

	node := seedRelayTargetNode(t, r, "nft-gate-apply-old", "198.51.100.76")
	setNftGateNodeVersion(t, r, node, "2.1.12")
	k := seedChainKernel(t, r, node, kernel.TypeXray)

	err := h.applyKernel(k.ID, false)
	if err == nil || !strings.Contains(err.Error(), "版本过旧") {
		t.Fatalf("applyKernel on 2.1.12 agent must be blocked with version error, got err=%v", err)
	}
	if strings.Contains(err.Error(), "不在线") {
		t.Fatalf("gate must fire before the send (no offline error), got err=%v", err)
	}
}

// TestKernelControlRestartBlocksOldAgent 接线证明：旧版 agent 的
// 内核 restart 同样被门拦下，回执点名版本过旧。
func TestKernelControlRestartBlocksOldAgent(t *testing.T) {
	h := setupHandlerTestRepo(t)
	r := handlerTestRepo(t, h)

	node := seedRelayTargetNode(t, r, "nft-gate-restart-old", "198.51.100.77")
	setNftGateNodeVersion(t, r, node, "2.1.12")
	k := seedChainKernel(t, r, node, kernel.TypeXray) // Enabled=1

	env := c10CallControl(t, h, k.ID, "restart")
	if env.Code == 0 {
		t.Fatalf("restart on 2.1.12 agent must fail, got success")
	}
	if !strings.Contains(env.Msg, "版本过旧") {
		t.Fatalf("restart receipt must name the version gate, got msg=%q", env.Msg)
	}
}
