package handler

import (
	"testing"
)

// X-P3-20 (L-08-2): nftables serialized manager 与端口锁同型竞态——
// ReleaseNode 无条件删锁表条目时，持锁的 Reconcile 仍在临界区，
// 第二个 lockFor 会生成新 mutex 并发进入。引用计数后：有未释放
// 引用时 ReleaseNode 只打标记，lockFor 仍返回同一 mutex。
func TestR1B8_XP320_NftManagerLockCleanupRefcount(t *testing.T) {
	m := newSerializedNftManager(nil)
	const nodeID = 977

	mu1 := m.lockFor(nodeID)
	mu1.Lock()
	m.ReleaseNode(nodeID)

	mu2 := m.lockFor(nodeID)
	if mu2 != mu1 {
		t.Fatalf("有未释放引用时 lockFor 应返回同一 mutex, got new instance（双持有窗口）")
	}
	if mu2.TryLock() {
		mu2.Unlock()
		t.Fatalf("首个持锁未释放时同一 mutex 不应可再次加锁")
	}
	m.unlockFor(nodeID, mu1)

	// mu1 已释放：mu2（同一 mutex）现在可加锁。
	mu2.Lock()
	m.unlockFor(nodeID, mu2)

	// 引用归零且已标记清理：条目删除，新 lockFor 生成新 mutex。
	mu3 := m.lockFor(nodeID)
	if mu3 == mu1 {
		t.Fatalf("条目删除后应生成新 mutex")
	}
	mu3.Lock()
	m.unlockFor(nodeID, mu3)
}
