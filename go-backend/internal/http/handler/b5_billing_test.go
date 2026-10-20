package handler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// ─── B5 计费/流量批（audit FINAL §5-B5）───
// 本文件的测试均按「修复前失败、修复后通过」纪律编写，逐条对应 F 编号。

// ---------- F-019: 限速换算精度 ----------

// parseBase2MBForTest 复刻 agent 侧 alecthomas/units.ParseBase2Bytes 对
// "X.XXXMB" 的解析（1 MB = 1024*1024 B），用于在面板侧断言生成行经
// agent 解析还原后的实际速率。
func parseBase2MBForTest(t *testing.T, s string) int64 {
	t.Helper()
	if !strings.HasSuffix(s, "MB") {
		t.Fatalf("limit value %q does not use MB unit", s)
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSuffix(s, "MB"), "%g", &v); err != nil {
		t.Fatalf("parse limit value %q: %v", s, err)
	}
	return int64(v * 1024 * 1024)
}

// TestSpeedToLimitLinePrecisionF019: 表驱动断言 1/5/10/100 Mbps 生成的
// limit 行经 agent 的 ParseBase2Bytes 还原后与目标 B/s（Mbps×10^6/8）
// 误差 <1%。修复前 %.1f 量化使 1 Mbps 偏低约 16%、10 Mbps 偏低约 4%。
func TestSpeedToLimitLinePrecisionF019(t *testing.T) {
	for _, mbps := range []int{1, 2, 5, 10, 25, 40, 80, 100, 333} {
		line := speedToLimitLine("$", mbps)
		parts := strings.Fields(line)
		if len(parts) != 3 {
			t.Fatalf("speed %d: malformed limit line %q", mbps, line)
		}
		got := parseBase2MBForTest(t, parts[1])
		target := int64(mbps) * 1000000 / 8
		diff := got - target
		if diff < 0 {
			diff = -diff
		}
		if float64(diff) > float64(target)*0.01 {
			t.Errorf("speed %d Mbps: line %q restores to %d B/s, target %d B/s (err %.2f%% > 1%%)",
				mbps, line, got, target, float64(diff)/float64(target)*100)
		}
		if parts[1] != parts[2] {
			t.Errorf("speed %d: in/out limits differ in %q", mbps, line)
		}
	}
}

// ---------- F-018: 配额执法 Mark 失败不得丢失 paused 列表 ----------

// TestEnforceUserQuotaMarkFailureRecoveryF018 复现 A4-001 链：
// 首次执法时 MarkUserQuotaDisabled 写库失败（用触发器精点 user_quota
// 的 UPDATE 失败），转发已被暂停；第二次执法不得用空列表覆写恢复
// 依据；最终 ResetUserQuotaUsage(scope=all) 必须能恢复全部转发。
func TestEnforceUserQuotaMarkFailureRecoveryF018(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now()
	nowMs := now.UnixMilli()

	userID, err := r.CreateUser("quotauser", "hash", 1, nowMs+86400000, 1, 1, 100000, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 隧道 + 用户隧道（恢复路径 ensureUserTunnelForwardAllowed 需要）。
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(41, 'b5-quota-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 41, 10, 100000, 0, 0, 0, 1, ?, 1)`, userID, nowMs+86400000).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	var forwardIDs []int64
	for i, name := range []string{"b5-quota-fwd-a", "b5-quota-fwd-b"} {
		if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
			VALUES(?, 'quotauser', ?, 41, '203.0.113.1:443', 'fifo', 0, 0, ?, ?, 1, ?)`, userID, name, nowMs, nowMs, i).Error; err != nil {
			t.Fatalf("insert forward %s: %v", name, err)
		}
		forwardIDs = append(forwardIDs, mustLastInsertID(t, r, name))
	}
	// 配额行：日限 1GB，已用 3GiB（超额）。先建行再改限额/用量。
	if _, err := r.AddUserQuotaUsage(userID, 0, now); err != nil {
		t.Fatalf("init quota row: %v", err)
	}
	if err := r.DB().Exec(`UPDATE user_quota SET daily_limit_gb = 1, monthly_limit_gb = 0, daily_used_bytes = ?, monthly_used_bytes = 0 WHERE user_id = ?`,
		int64(3)<<30, userID).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
	view, err := r.GetUserQuotaView(userID, now)
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	if !isUserQuotaExceeded(view) {
		t.Fatalf("seeded quota must be exceeded: %+v", view)
	}

	forwardStatus := func(id int64) int {
		return mustQueryInt(t, r, `SELECT status FROM forward WHERE id = ?`, id)
	}

	// 第一次执法：让 user_quota 的 UPDATE 精点失败（Mark 写库失败），
	// 其余表（forward）写正常——即 A4-001 的时序。
	if err := r.DB().Exec(`CREATE TRIGGER b5_fail_quota_update BEFORE UPDATE ON user_quota
		BEGIN SELECT RAISE(ABORT, 'b5 injected mark failure'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h.enforceUserQuotaIfNeeded(userID, view)
	if err := r.DB().Exec(`DROP TRIGGER b5_fail_quota_update`).Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	// 修复后语义：Mark 失败即整轮执法中止（先落库后暂停），转发一张
	// 都不得被暂停——否则恢复依据已丢失、暂停不可逆。
	for _, id := range forwardIDs {
		if got := forwardStatus(id); got != 1 {
			t.Fatalf("F-018: forward %d paused (status=%d) although Mark failed; its restore basis is lost", id, got)
		}
	}

	// 第二次执法（Mark 恢复正常）：应暂停全部并落库完整 paused 列表。
	view2, err := r.GetUserQuotaView(userID, time.Now())
	if err != nil || view2 == nil {
		t.Fatalf("quota view 2: %v / %v", view2, err)
	}
	h.enforceUserQuotaIfNeeded(userID, view2)
	for _, id := range forwardIDs {
		if got := forwardStatus(id); got != 0 {
			t.Fatalf("forward %d should be paused after successful enforce, status=%d", id, got)
		}
	}
	pausedRow := mustQueryInt(t, r, `SELECT disabled_by_quota FROM user_quota WHERE user_id = ?`, userID)
	if pausedRow != 1 {
		t.Fatalf("quota row must be disabled_by_quota=1 after enforce, got %d", pausedRow)
	}

	// 恢复：ResetUserQuotaUsage(all) 的 release 必须带回全部转发，
	// applyUserQuotaRelease 后转发恢复 status=1。
	release, err := r.ResetUserQuotaUsage(userID, "all", time.Now())
	if err != nil {
		t.Fatalf("reset quota: %v", err)
	}
	if release == nil || !release.UnblockUser {
		t.Fatalf("expected unblocking release, got %+v", release)
	}
	if len(release.ForwardIDs) != len(forwardIDs) {
		t.Fatalf("F-018: release carries %d forwards, want %d (paused list was lost/overwritten)", len(release.ForwardIDs), len(forwardIDs))
	}
	h.applyUserQuotaRelease(release, time.Now().UnixMilli())
	for _, id := range forwardIDs {
		if got := forwardStatus(id); got != 1 {
			t.Fatalf("F-018: forward %d not restored after quota reset, status=%d", id, got)
		}
	}
}

// ---------- F-020: Clear 必须进 applying/generation 协议 ----------

type blockingClearManager struct {
	fakeNftablesManager
	clearStarted chan struct{}
	clearRelease chan struct{}
	once         sync.Once
}

func (f *blockingClearManager) Clear(ctx context.Context, cfg runtimenft.SSHConfig) error {
	f.once.Do(func() { close(f.clearStarted) })
	<-f.clearRelease
	return f.fakeNftablesManager.Clear(ctx, cfg)
}

// TestSerializedClearMarksApplyingAndBumpsGenerationF020: Clear 执行
// 期间采集侧必须能感知（IsApplying=true），且 Clear 完成后
// generation 必须递增——修复前两者皆无，在途采集可把清表前样本
// 写回基线造成漏计。
func TestSerializedClearMarksApplyingAndBumpsGenerationF020(t *testing.T) {
	inner := &blockingClearManager{clearStarted: make(chan struct{}), clearRelease: make(chan struct{})}
	m := newSerializedNftManager(inner)
	cfg := runtimenft.SSHConfig{Host: "test", Port: 22}
	gen0 := m.GetGeneration(9)

	clearDone := make(chan error, 1)
	go func() { clearDone <- m.Clear(context.Background(), 9, cfg) }()
	<-inner.clearStarted // Clear 已进入临界区（持有 per-node 锁）

	if !m.IsApplying(9) {
		t.Fatal("F-020: Clear does not mark applying; in-flight collections cannot detect it")
	}
	close(inner.clearRelease)
	if err := <-clearDone; err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := m.GetGeneration(9); got != gen0+1 {
		t.Fatalf("F-020: Clear must bump generation (%d -> %d, got %d)", gen0, gen0+1, got)
	}
	if m.IsApplying(9) {
		t.Fatal("applying must be cleared after Clear returns")
	}
}

// orderRecordingManager 记录 Collect/Clear 的调用顺序（F-020 drain）。
type orderRecordingManager struct {
	fakeNftablesManager
	mu    sync.Mutex
	order []string
}

func (f *orderRecordingManager) CollectCounters(ctx context.Context, cfg runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	f.mu.Lock()
	f.order = append(f.order, "collect")
	f.mu.Unlock()
	return f.fakeNftablesManager.CollectCounters(ctx, cfg)
}

func (f *orderRecordingManager) Clear(ctx context.Context, cfg runtimenft.SSHConfig) error {
	f.mu.Lock()
	f.order = append(f.order, "clear")
	f.mu.Unlock()
	return f.fakeNftablesManager.Clear(ctx, cfg)
}

// TestClearNftablesNodeDrainsCountersFirstF020: 手动 Clear 前必须先
// best-effort drain（与 sync/reconcile 路径一致），把清表前累计增量
// 落库推进基线，而不是随清表静默丢失。修复前 clearNftablesNode
// 完全不采集，collect 调用次数为 0。
func TestClearNftablesNodeDrainsCountersFirstF020(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-clear-drain", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	now := time.Now().UnixMilli()
	if err := h.repo.UpsertNftRuleBinding(repo.NftRuleBindingInput{
		ForwardID:  forward.ID,
		NodeID:     fixture.nodeID,
		InPort:     20000,
		Protocols:  "tcp",
		TargetAddr: "203.0.113.9:8080",
		Status:     runtimenft.StatusApplied,
		RuleHash:   "hash-b5",
	}, now); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// 旧基线 400B；节点当前累计 1000B → drain 应落库 600B 增量并把
	// 基线推进到 1000。
	if err := h.repo.UpsertNftCounterStates([]repo.NftCounterStateInput{{
		NodeID: fixture.nodeID, ForwardID: forward.ID, Protocol: "tcp",
		Direction: runtimenft.CounterDirectionToTarget, RuleHash: "hash-b5",
		Bytes: 400, Packets: 4, CollectedTime: now - 1000,
	}}, now-1000); err != nil {
		t.Fatalf("seed counter state: %v", err)
	}
	manager := &orderRecordingManager{}
	manager.counterSamples = []runtimenft.CounterSample{
		{ForwardID: forward.ID, Protocol: "tcp", Direction: runtimenft.CounterDirectionToTarget, Bytes: 1000, Packets: 10},
	}
	h.nftablesManager = newSerializedNftManager(manager)

	if err := h.clearNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("clearNftablesNode: %v", err)
	}

	manager.mu.Lock()
	order := append([]string(nil), manager.order...)
	manager.mu.Unlock()
	collectIdx, clearIdx := -1, -1
	for i, op := range order {
		if op == "collect" && collectIdx < 0 {
			collectIdx = i
		}
		if op == "clear" && clearIdx < 0 {
			clearIdx = i
		}
	}
	if collectIdx < 0 {
		t.Fatalf("F-020: clearNftablesNode never collected counters before clearing (order=%v); pre-clear traffic is silently lost", order)
	}
	if clearIdx < 0 || collectIdx > clearIdx {
		t.Fatalf("F-020: drain must run before Clear, order=%v", order)
	}
	states, err := h.repo.GetNftCounterStatesByNode(fixture.nodeID)
	if err != nil {
		t.Fatalf("get counter states: %v", err)
	}
	found := false
	for _, st := range states {
		if st.ForwardID == forward.ID {
			found = true
			if st.Bytes != 1000 {
				t.Fatalf("F-020: baseline must advance to drained value 1000, got %d", st.Bytes)
			}
		}
	}
	if !found {
		t.Fatal("counter state row missing after clear")
	}
}

// ---------- F-135: 配额 reconcile 超时必须是真取消 ----------

type ctxBlockingReconcileManager struct {
	fakeNftablesManager
	mu       sync.Mutex
	calls    int
	started  chan struct{}
	returned chan struct{}
	once     sync.Once
}

func (f *ctxBlockingReconcileManager) Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call > 1 {
		return f.fakeNftablesManager.Reconcile(ctx, cfg, plan)
	}
	f.once.Do(func() { close(f.started) })
	<-ctx.Done() // 与真实 SSH runner 一致：尊重 ctx 取消
	close(f.returned)
	return runtimenft.ApplyResult{}, ctx.Err()
}

// TestReconcileNodeWithTimeoutCancelsUnderlyingWorkF135:
// reconcileNodeWithTimeout 超时后，底层 reconcile 必须收到取消并
// 退出（释放 per-node 锁），而不是拿着 context.Background() 继续
// 持锁排队后续同节点下发。修复前：超时仅停等，底层永不返回。
func TestReconcileNodeWithTimeoutCancelsUnderlyingWorkF135(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-timeout", fixture.nodeID)
	seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	manager := &ctxBlockingReconcileManager{started: make(chan struct{}), returned: make(chan struct{})}
	h.nftablesManager = newSerializedNftManager(manager)

	start := time.Now()
	if ok := h.reconcileNodeWithTimeout(fixture.nodeID, nil, nil, 300*time.Millisecond); ok {
		t.Fatal("expected timeout to report failure")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("reconcileNodeWithTimeout did not return after timeout (took %v)", elapsed)
	}
	<-manager.started
	select {
	case <-manager.returned:
	case <-time.After(3 * time.Second):
		t.Fatal("F-135: underlying reconcile was not cancelled after timeout; it keeps holding the per-node lock")
	}
	// 锁已释放：同节点第二次 reconcile 应能正常完成。
	if ok := h.reconcileNodeWithTimeout(fixture.nodeID, nil, nil, 5*time.Second); !ok {
		t.Fatal("F-135: subsequent reconcile on same node should succeed after cancelled one released the lock")
	}
}

var _ = model.UserQuota{}
