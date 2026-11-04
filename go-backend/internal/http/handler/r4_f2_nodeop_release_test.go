package handler

import "testing"

// R4-F2-02 回归钉：删节点漏斗的在途登记释放所有权。
//
// 修复前释放只在级联段（deleteNodeByIDAfterGuards）内 defer，守卫
// 段取得登记与级联段之间的连带卸载步一旦 panic，登记无 TTL 永不
// 释放、该节点运维操作恒被误拒至面板重启。修复后释放经
// guard.releaseUpgradeOp 归一所有权：级联段先行释放并清标，三个
// 漏斗入口的兜底 defer 只在级联段从未进入时才真正释放。

// ① panic 缝形态：守卫取得登记后、级联段从未进入，入口 defer 的
// 兜底释放必须在 panic 展开期把登记放掉。
func TestR4F2NodeDeleteOpReleaseOnPanicGap(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "panic-gap-node", "secret-panic-gap-node")

	func() {
		defer func() { _ = recover() }()
		guard, err := h.checkNodeDeleteGuards(node.ID)
		if err != nil {
			t.Fatalf("guard: %v", err)
		}
		// 与 nodeDelete 入口同形：守卫成功后立即 defer 兜底释放，
		// 随后模拟连带卸载步 panic（级联段从未被调用）。
		defer guard.releaseUpgradeOp(h, node.ID)
		panic("simulated prepareNodeAgentUninstall panic")
	}()

	if _, ok := h.tryAcquireNodeUpgradeOp(node.ID, "升级"); !ok {
		t.Fatal("in-flight 删除 registration leaked after a panic in the guard-to-cascade gap")
	}
	h.releaseNodeUpgradeOp(node.ID)
}

// ② 所有权：级联段已释放并清标后，入口兜底释放必须 no-op——不得
// 误放其后由其他操作新占用的同节点登记（releaseNodeUpgradeOp 按
// 节点无条件删除、无身份校验，所有权只能靠 holdsUpgradeOp 标记）。
func TestR4F2GuardReleaseOwnership(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "owner-node", "secret-owner-node")

	guard, err := h.checkNodeDeleteGuards(node.ID)
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	// 级联段先行释放（其 defer 的等价直调）。
	guard.releaseUpgradeOp(h, node.ID)
	// 新操作占用同节点登记。
	if _, ok := h.tryAcquireNodeUpgradeOp(node.ID, "升级"); !ok {
		t.Fatal("precondition: acquire upgrade op after guard release")
	}
	// 入口兜底 defer 迟到触发：必须 no-op。
	guard.releaseUpgradeOp(h, node.ID)
	if existing, ok := h.tryAcquireNodeUpgradeOp(node.ID, "回退"); ok || existing != "升级" {
		t.Fatalf("stale guard release freed a newer registration: existing=%q ok=%v, want 升级/false", existing, ok)
	}
	h.releaseNodeUpgradeOp(node.ID)

	// nil 守卫与未占用守卫的释放恒为 no-op（级联段 nil 兜底路径）。
	var nilGuard *nodeDeleteGuard
	nilGuard.releaseUpgradeOp(h, node.ID)
	(&nodeDeleteGuard{}).releaseUpgradeOp(h, node.ID)
}
