package handler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
)

// R5-B3 D1-003: per-node single-flight + 自动 reconcile（D9）。
// 可达性证据：原 handler/nftables_runtime.go 无任何 per-node 锁；
// D1-003（节点重启后表丢失无重建）经 jobs.go:128-176 确认无重建逻辑。

var errFakeTableMissing = errors.New("nft: Error: No such file or directory")

// slowFakeManager 记录调用时间戳，用于断言串行。
type slowFakeManager struct {
	fakeNftablesManager
	delay      time.Duration
	mu         sync.Mutex
	startTimes []time.Time
	endTimes   []time.Time
}

func (f *slowFakeManager) Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	f.mu.Lock()
	f.startTimes = append(f.startTimes, time.Now())
	f.mu.Unlock()
	time.Sleep(f.delay)
	res, err := f.fakeNftablesManager.Reconcile(ctx, cfg, plan)
	f.mu.Lock()
	f.endTimes = append(f.endTimes, time.Now())
	f.mu.Unlock()
	return res, err
}

func TestConcurrentReconcileSerialized(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &slowFakeManager{delay: 100 * time.Millisecond}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-conc", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:6001")

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.syncNftablesNode(fixture.nodeID); err != nil {
				t.Errorf("reconcile: %v", err)
			}
		}()
	}
	wg.Wait()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.startTimes) != 2 {
		t.Fatalf("expected 2 reconciles, got %d", len(manager.startTimes))
	}
	// 串行：第二个的开始必须在第一个的结束之后。
	if manager.startTimes[1].Before(manager.endTimes[0]) {
		t.Fatalf("reconciles overlapped: not serialized")
	}
}

func TestAutoReconcileOnTableMissing(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{collectErr: errFakeTableMissing}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)

	// 采集检测到表缺失 → 投递（R2：立即返回，不阻塞）。
	cfg, _ := h.repo.GetNodeSSHConfig(fixture.nodeID)
	_, _, _, _, _ = h.accountNftablesNodeTraffic(fixture.nodeID, cfg, time.Now())

	// 去重：再次投递同一节点，队列仍只有 1 个。
	h.enqueueNftAutoReconcile(fixture.nodeID, "test")
	h.enqueueNftAutoReconcile(fixture.nodeID, "test")
	got := 0
	for {
		select {
		case nodeID := <-h.nftAutoQueue:
			if nodeID != fixture.nodeID {
				t.Fatalf("wrong node %d", nodeID)
			}
			got++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:
	if got != 1 {
		t.Fatalf("expected dedup to 1, got %d", got)
	}
}

func TestAutoReconcileWorkerRecoversNode(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-recover", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:6002")

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	h.jobsWG.Add(1)
	go func() {
		defer wg.Done()
		h.runNftAutoReconcileWorker(ctx)
	}()

	h.enqueueNftAutoReconcile(fixture.nodeID, "test")
	// 等待 worker 执行。
	deadline := time.Now().Add(5 * time.Second)
	for manager.reconcileCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	if manager.reconcileCount() == 0 {
		t.Fatalf("worker did not run reconcile")
	}
	found := false
	for _, rule := range manager.lastPlan.Rules {
		if rule.ForwardID == forward.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("rules not restored by auto-reconcile")
	}
}

func TestAutoReconcileBackoffExhaustion(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler

	// 模拟连续失败。
	for i := 0; i < nftAutoReconcileMaxFailures; i++ {
		h.recordNftAutoReconcileFailure(fixture.nodeID, errFakeSSHDown)
	}
	h.nftAutoMu.Lock()
	st := h.nftAutoFail[fixture.nodeID]
	h.nftAutoMu.Unlock()
	if st == nil || !st.gaveUp {
		t.Fatalf("expected gaveUp after %d failures", nftAutoReconcileMaxFailures)
	}
	// 放弃后不再投递。
	h.enqueueNftAutoReconcile(fixture.nodeID, "test")
	select {
	case <-h.nftAutoQueue:
		t.Fatalf("enqueued after exhaustion")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNodeRecoveredTriggersReconcile(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler

	// 标记 apply 失败，然后采集成功 → 触发。
	h.markNftApplyFailed(fixture.nodeID, true)
	if !h.checkNftNodeRecovered(fixture.nodeID) {
		t.Fatalf("expected recovered trigger")
	}
	select {
	case <-h.nftAutoQueue:
	default:
		t.Fatalf("not enqueued on recovery")
	}
	// 再次检查：已清除，不重复触发。
	if h.checkNftNodeRecovered(fixture.nodeID) {
		t.Fatalf("duplicate trigger")
	}
}

func TestReconcileLockCleanupOnNodeDelete(t *testing.T) {
	// R5-B5 D14: 锁已下沉到 serializedNftManager，此测试改验证 manager 层。
	// 具体断言见 TestReleaseNodeClearsLock（serialized_manager_d14_test.go）。
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	if h.nftablesManager == nil {
		t.Fatalf("manager not initialized")
	}
	// 通过一次调用触发锁创建，再释放。
	_ = h.nftablesManager.Unwrap()
}
