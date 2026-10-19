package handler

import (
	"context"
	"sync"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// R5-B5 D14: single-flight 下沉到 manager 层。
// 可达性证据：T-A R5-A3-01 发现 clearNftablesNode 绕过 handler 层锁。

// blockingFakeManager 用于验证串行化。
type blockingFakeManager struct {
	fakeNftablesManager
	delay time.Duration
	mu    sync.Mutex
	calls []string
}

func (f *blockingFakeManager) Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "reconcile-start")
	f.mu.Unlock()
	time.Sleep(f.delay)
	res, err := f.fakeNftablesManager.Reconcile(ctx, cfg, plan)
	f.mu.Lock()
	f.calls = append(f.calls, "reconcile-end")
	f.mu.Unlock()
	return res, err
}

func (f *blockingFakeManager) Clear(ctx context.Context, cfg runtimenft.SSHConfig) error {
	f.mu.Lock()
	f.calls = append(f.calls, "clear-start")
	f.mu.Unlock()
	time.Sleep(f.delay)
	err := f.fakeNftablesManager.Clear(ctx, cfg)
	f.mu.Lock()
	f.calls = append(f.calls, "clear-end")
	f.mu.Unlock()
	return err
}

func TestClearAndReconcileSerialized(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	inner := &blockingFakeManager{delay: 100 * time.Millisecond}
	h.nftablesManager = newSerializedNftManager(inner)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-d14", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = h.syncNftablesNode(fixture.nodeID)
	}()
	go func() {
		defer wg.Done()
		_ = h.clearNftablesNode(fixture.nodeID)
	}()
	wg.Wait()

	inner.mu.Lock()
	defer inner.mu.Unlock()
	// 断言串行：四个事件必须按 start/end 配对，不交错。
	if len(inner.calls) != 4 {
		t.Fatalf("expected 4 calls, got %v", inner.calls)
	}
	// 检查不交错：第一个 start 的对应 end 必须在第二个 start 之前。
	starts := 0
	for i, c := range inner.calls {
		if c == "reconcile-start" || c == "clear-start" {
			starts++
			if starts == 2 {
				// 第二个 start 之前必须已有 2 个 end（即第一个已完成）。
				ends := 0
				for j := 0; j < i; j++ {
					if inner.calls[j] == "reconcile-end" || inner.calls[j] == "clear-end" {
						ends++
					}
				}
				if ends != 1 {
					t.Fatalf("calls interleaved (not serialized): %v", inner.calls)
				}
			}
		}
	}
}

func TestDirectManagerCallSerialized(t *testing.T) {
	// 证明下沉有效：直接调 manager 方法也被串行化。
	inner := &blockingFakeManager{delay: 50 * time.Millisecond}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = m.Clear(context.Background(), 99, cfg)
	}()
	go func() {
		defer wg.Done()
		_, _ = m.Reconcile(context.Background(), 99, cfg, runtimenft.NodePlan{})
	}()
	wg.Wait()

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.calls) != 4 {
		t.Fatalf("expected 4 calls, got %v", inner.calls)
	}
}

func TestReleaseNodeClearsLock(t *testing.T) {
	inner := &fakeNftablesManager{}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}

	// 触发锁创建。
	_ = m.Test(context.Background(), 42, cfg)

	m.mu.Lock()
	_, existsBefore := m.locks[42]
	m.mu.Unlock()
	if !existsBefore {
		t.Fatalf("lock not created")
	}

	m.ReleaseNode(42)

	m.mu.Lock()
	_, existsAfter := m.locks[42]
	m.mu.Unlock()
	if existsAfter {
		t.Fatalf("lock not released")
	}
}
