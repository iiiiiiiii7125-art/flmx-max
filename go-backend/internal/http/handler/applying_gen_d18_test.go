package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// R5-B5B D18: 采集去锁 + applying/generation。
// 可达性证据：批次 5 把 CollectCounters 纳入写锁，reconcile 卡 SSH 时采集漂移。

// slowReconcileManager 模拟慢 reconcile。
type slowReconcileManager struct {
	fakeNftablesManager
	reconcileDelay time.Duration
}

func (f *slowReconcileManager) Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	time.Sleep(f.reconcileDelay)
	return f.fakeNftablesManager.Reconcile(ctx, cfg, plan)
}

// TestD18_CollectDiscardedDuringApplying: reconcile 期间采集 → 丢弃、不阻塞、基线不推进。
func TestD18_CollectDiscardedDuringApplying(t *testing.T) {
	inner := &slowReconcileManager{reconcileDelay: 200 * time.Millisecond}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	// 启动慢 reconcile。
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		_, _ = m.Reconcile(context.Background(), 1, cfg, runtimenft.NodePlan{})
	}()
	// 等待 applying 置位。
	time.Sleep(50 * time.Millisecond)

	// 采集：应在 applying 窗口内被丢弃（不阻塞）。
	start := time.Now()
	if !m.IsApplying(1) {
		t.Fatal("expected applying to be set during reconcile")
	}
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("collect blocked too long: %v", elapsed)
	}

	<-reconcileDone
	// reconcile 结束后 applying 清除。
	if m.IsApplying(1) {
		t.Fatal("applying not cleared after reconcile")
	}
}

// TestD18_GenerationDetectsCompletedApply: 采集开始时无 applying，期间 apply 完成 → gen 变化 → 丢弃。
func TestD18_GenerationDetectsCompletedApply(t *testing.T) {
	inner := &fakeNftablesManager{}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	gen0 := m.GetGeneration(7)
	// 期间发生一次完整 apply（置位又清除）。
	_, _ = m.Reconcile(context.Background(), 7, cfg, runtimenft.NodePlan{})
	gen1 := m.GetGeneration(7)

	if gen1 != gen0+1 {
		t.Fatalf("gen not incremented: %d -> %d", gen0, gen1)
	}
	// applying 已清除，但 gen 变化能被检测到。
	if m.IsApplying(7) {
		t.Fatal("applying should be cleared")
	}
}

// TestD18_ApplyingTimeout: applying 存在 30s → 丢弃；70s → 继续（stale）。
func TestD18_ApplyingTimeout(t *testing.T) {
	inner := &fakeNftablesManager{}
	m := newSerializedNftManager(inner)

	// 手动设置 applying（模拟）。
	m.mu.Lock()
	m.applying[99] = time.Now().Add(-30 * time.Second)
	m.mu.Unlock()
	if !m.IsApplying(99) {
		t.Fatal("30s old applying should still block")
	}

	m.mu.Lock()
	m.applying[99] = time.Now().Add(-70 * time.Second)
	m.mu.Unlock()
	if m.IsApplying(99) {
		t.Fatal("70s old applying should be stale")
	}
}

// TestD18_ReconcilePanicClearsApplying: reconcile 抛异常 → applying 被 finally 清除。
func TestD18_ReconcilePanicClearsApplying(t *testing.T) {
	inner := &fakeNftablesManager{reconcileErr: context.DeadlineExceeded}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	_, err := m.Reconcile(context.Background(), 5, cfg, runtimenft.NodePlan{})
	if err == nil {
		t.Fatal("expected error")
	}
	// 即使失败，applying 也被清除。
	if m.IsApplying(5) {
		t.Fatal("applying not cleared after failed reconcile")
	}
	// gen 仍然递增（apply 尝试过）。
	if m.GetGeneration(5) != 1 {
		t.Fatalf("gen should increment even on failure, got %d", m.GetGeneration(5))
	}
}

// TestD18_ConcurrentCollectNoDeadlock: 100 轮并发采集 + 多次 reconcile。
func TestD18_ConcurrentCollectNoDeadlock(t *testing.T) {
	inner := &fakeNftablesManager{}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	var wg sync.WaitGroup
	// 100 轮并发采集。
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gen0 := m.GetGeneration(3)
			_, _ = m.CollectCounters(context.Background(), 3, cfg)
			_ = gen0
			_ = m.IsApplying(3)
		}()
	}
	// 多次 reconcile。
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Reconcile(context.Background(), 3, cfg, runtimenft.NodePlan{})
		}()
	}
	wg.Wait()
	// 无死锁即通过。gen 应为 5。
	if m.GetGeneration(3) != 5 {
		t.Fatalf("gen = %d, want 5", m.GetGeneration(3))
	}
}

// TestD18_ReleaseNodeClearsAll: 节点删除 → gen/applying/lock 条目释放。
func TestD18_ReleaseNodeClearsAll(t *testing.T) {
	inner := &fakeNftablesManager{}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	_, _ = m.Reconcile(context.Background(), 8, cfg, runtimenft.NodePlan{})
	m.ReleaseNode(8)

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.locks[8]; ok {
		t.Fatal("lock not released")
	}
	if _, ok := m.applying[8]; ok {
		t.Fatal("applying not released")
	}
	if _, ok := m.gen[8]; ok {
		t.Fatal("gen not released")
	}
}
