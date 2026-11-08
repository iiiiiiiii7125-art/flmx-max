package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// ─── R4B03-001 节点在线状态启动清扫（Round 4 KR1）───

func seedR4Node(t *testing.T, r *Repository, name string, status int, forwardMode string) model.Node {
	t.Helper()
	n := model.Node{
		Name:        name,
		Secret:      name + "-secret",
		ServerIP:    "198.51.100.99",
		Port:        "1000-65535",
		Status:      status,
		ForwardMode: forwardMode,
		CreatedTime: 1700000000000,
	}
	if err := r.DB().Create(&n).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return n
}

func r4NodeStatus(t *testing.T, r *Repository, id int64) int {
	t.Helper()
	node, err := r.GetNodeByID(id)
	if err != nil || node == nil {
		t.Fatalf("get node %d: %v", id, err)
	}
	return node.Status
}

// TestResetLocalNodeStatusesAtStartup 钉启动清扫的范围：只有
// 非 nftables 的僵尸在线被复位；nftables（创建即 status=1 的既定
// 口径）、本就离线的节点一律不动。复位后 agent 重连经
// UpdateNodeOnline 置回 1 的路径不受影响（重连节点不被误清）。
func TestResetLocalNodeStatusesAtStartup(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r4-node-status.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	staleAgent := seedR4Node(t, r, "r4-stale-agent", 1, "agent")
	staleDefaultMode := seedR4Node(t, r, "r4-stale-default", 1, "")
	nft := seedR4Node(t, r, "r4-nftables", 1, "nftables")
	offline := seedR4Node(t, r, "r4-offline", 0, "agent")

	reset, err := r.ResetLocalNodeStatusesAtStartup()
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset != 2 {
		t.Fatalf("复位条数 = %d, want 2（仅本地非 nftables 僵尸在线）", reset)
	}
	if got := r4NodeStatus(t, r, staleAgent.ID); got != 0 {
		t.Fatalf("僵尸在线本地节点 status = %d, want 0", got)
	}
	if got := r4NodeStatus(t, r, staleDefaultMode.ID); got != 0 {
		t.Fatalf("空 forward_mode（归一化为 agent）节点 status = %d, want 0", got)
	}
	if got := r4NodeStatus(t, r, nft.ID); got != 1 {
		t.Fatalf("nftables 节点 status = %d, want 1（既定口径不误伤）", got)
	}
	if got := r4NodeStatus(t, r, offline.ID); got != 0 {
		t.Fatalf("离线节点 status = %d, want 0", got)
	}

	// 重连腿：清扫后 agent 重连经握手路径置回 1，后续清扫（模拟
	// 再次启动时该节点又已离线死亡以外的场景）不由本方法连带——
	// 本方法只在启动调用一次，这里仅钉 UpdateNodeOnline 置回路径
	// 与清扫互不冲突。
	if err := r.UpdateNodeOnline(staleAgent.ID, 1, "1.7.8", 0, 0, 0); err != nil {
		t.Fatalf("update online: %v", err)
	}
	if got := r4NodeStatus(t, r, staleAgent.ID); got != 1 {
		t.Fatalf("重连后 status = %d, want 1", got)
	}

	// 候选列表口径：status=1。
	candidates, err := r.ListOnlineLocalNodes()
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	ids := make(map[int64]bool, len(candidates))
	for _, n := range candidates {
		ids[n.ID] = true
	}
	if !ids[staleAgent.ID] || !ids[nft.ID] {
		t.Fatalf("候选应含重连的本地节点与 nftables 节点: %v", ids)
	}
	if ids[staleDefaultMode.ID] || ids[offline.ID] {
		t.Fatalf("候选不得含已复位/离线节点: %v", ids)
	}
}
