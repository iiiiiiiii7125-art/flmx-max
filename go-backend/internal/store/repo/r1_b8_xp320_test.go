package repo

import (
	"sync"
	"testing"
	"time"
)

// X-P3-20 (L-08-2): 端口锁表清理竞态。Cleanup 无条件删表项时，
// 持锁者仍在临界区内，第二个 Acquire 会拿到全新 mutex 与之并发
// 进入同 nodeID 临界区（双持有）。改引用计数：有未释放引用时
// 清理只能打 zombie 标记，条目待引用归零后才真正删除。
func TestR1B8_XP320_PortAllocLockCleanupRefcount(t *testing.T) {
	const nodeID = 424201

	mu1 := AcquirePortAllocLock(nodeID) // 已持锁返回
	// 模拟节点删除时的清理（持锁期间发生）。
	CleanupPortAllocLock(nodeID)

	acquired := make(chan *sync.Mutex, 1)
	go func() {
		mu2 := AcquirePortAllocLock(nodeID)
		acquired <- mu2
	}()
	select {
	case mu2 := <-acquired:
		ReleasePortAllocLock(mu2)
		ReleasePortAllocLock(mu1)
		t.Fatalf("Cleanup 后第二个 Acquire 在首个持锁未释放时即进入临界区（双持有）")
	case <-time.After(300 * time.Millisecond):
		// 仍被阻塞：正确。
	}
	ReleasePortAllocLock(mu1)
	select {
	case mu2 := <-acquired:
		if mu2 != mu1 {
			t.Fatalf("引用计数下应复用同一 mutex, got different instance")
		}
		ReleasePortAllocLock(mu2)
	case <-time.After(2 * time.Second):
		t.Fatalf("首个释放后第二个 Acquire 应能进入")
	}

	// 引用归零 + 此前已标记清理 → 条目应被删除：再次 Acquire 是新实例。
	CleanupPortAllocLock(nodeID)
	mu3 := AcquirePortAllocLock(nodeID)
	if mu3 == mu1 {
		t.Fatalf("条目删除后应生成新 mutex")
	}
	ReleasePortAllocLock(mu3)
}
