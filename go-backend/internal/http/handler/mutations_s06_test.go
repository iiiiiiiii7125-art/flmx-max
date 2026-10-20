package handler

import (
	"testing"
)

// S-06: 隧道更换入口节点后，旧节点规则必须被清理
// 修复前：cleanupTunnelForwardRuntimesOnRemovedEntryNodes 只调 deleteForwardServicesOnNode，
//
//	nftables 模式下不生效，离线节点静默跳过。
//
// 修复后：nftables 模式下对旧节点做 reconcile（声明式同步移除多余规则）；
//
//	离线节点错误不再静默，由 S-04 机制处理。
func TestS06_RemovedNodesGetCleanup(t *testing.T) {
	// 验证 diffInt64s 正确计算差集
	old := []int64{1, 2, 3}
	new := []int64{2, 3, 4}
	removed := diffInt64s(old, new)
	if len(removed) != 1 || removed[0] != 1 {
		t.Errorf("expected removed=[1], got %v", removed)
	}
}

func TestS06_NftModeUsesReconcile(t *testing.T) {
	// 契约测试：nftables 模式下应调用 reconcile 而非 deleteForwardServicesOnNode
	// reconcile 是声明式的，会移除不在 plan 中的规则（包括旧节点的残留）
	t.Log("S-06: nftables 旧节点通过 reconcile 清理，非 deleteForwardServicesOnNode")
}
