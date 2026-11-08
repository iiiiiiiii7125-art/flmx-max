package handler

import (
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
)

// FIX-BASELINE（R2-E2-02）：面板 nftables 模式首采集窗口回归测试。
//
// 机制（与 realm 本体同形）：入账按累计计数差分，buildNftCounterDeltas
// 对「无旧状态 / 规则哈希变化」只落基线零增量；新绑定落成后基线只
// 能等首个 30s 周期采集样本建立——绑定落成至首采集之间的流量被基
// 线整段吞掉、永不入账。reconcile 前的 drain 只结算旧表（新绑定尚
// 无 binding、样本被 filter 丢弃），首次创建无任何兜底。
//
// 本测试按真机时序复现：新建转发 → sync（drain→Reconcile→binding
// 落库）返回时基线必须已钉在落成时刻（sync 内采集次数断言 + 基线
// 行断言，此时计数器已有首批流量）→ 其后流量增长 → 周期采集到达
// ——增长部分必须全额入账。修复前：sync 后无基线行，首个周期采集
// 把累计值整段收为基线，流量恒 0。
func TestSyncNftablesPinsBaselineForNewBinding(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-baseline-tunnel", fixture.nodeID)
	// 双向计费（与采集全链路测试同口径）：in/out 两向都断言。
	if err := h.repo.DB().Model(&model.Tunnel{}).Where("name = ?", "nft-baseline-tunnel").Update("flow", 2).Error; err != nil {
		t.Fatalf("update tunnel flow: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20001)

	// 节点侧计数器在表落成后的最初时刻已有首批流量（真机上即绑定
	// 生效后数秒内的业务流量）：sync 返回前计数器读数为 1000/2000。
	manager.counterSamples = []runtimenft.CounterSample{
		{ForwardID: forward.ID, Protocol: "tcp", Direction: runtimenft.CounterDirectionToTarget, Bytes: 1000, Packets: 10},
		{ForwardID: forward.ID, Protocol: "tcp", Direction: runtimenft.CounterDirectionFromTarget, Bytes: 2000, Packets: 20},
	}

	if err := h.syncNftablesNode(fixture.nodeID); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// sync 内采集恰两次：drain（重建前结算旧表）+ settle（落成后钉
	// 基线）。修复前只有 drain 一次、且新绑定基线无从建立。
	if got := manager.collectCount(); got != 2 {
		t.Fatalf("FIX-BASELINE: sync must collect twice (drain + post-sync settle), got %d", got)
	}
	states, err := h.repo.GetNftCounterStatesByNode(fixture.nodeID)
	if err != nil {
		t.Fatalf("load states: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("baseline after sync: want 2 states pinned at sync time, got %+v", states)
	}
	byDir := map[string]int64{}
	for _, st := range states {
		if st.ForwardID != forward.ID || st.RuleHash == "" {
			t.Fatalf("unexpected baseline state: %+v", st)
		}
		byDir[st.Direction] = st.Bytes
	}
	if byDir[runtimenft.CounterDirectionToTarget] != 1000 || byDir[runtimenft.CounterDirectionFromTarget] != 2000 {
		t.Fatalf("baseline pinned values = %v, want to-target=1000 from-target=2000", byDir)
	}
	// 基线只钉不定格流量：sync 返回时不得有任何入账。
	if got := mustHandlerCount(t, h, `SELECT in_flow FROM forward WHERE id = ?`, forward.ID); got != 0 {
		t.Fatalf("no flow may be credited at baseline pin, got in_flow=%d", got)
	}

	// 其后流量增长 → 周期采集到达：增长部分必须全额入账（修复前
	// 本次采集会把 1400/2600 整段收为基线、入账恒 0）。
	manager.counterSamples = []runtimenft.CounterSample{
		{ForwardID: forward.ID, Protocol: "tcp", Direction: runtimenft.CounterDirectionToTarget, Bytes: 1400, Packets: 14},
		{ForwardID: forward.ID, Protocol: "tcp", Direction: runtimenft.CounterDirectionFromTarget, Bytes: 2600, Packets: 26},
	}
	cfg := mustCollectionSSHConfig(t, h, fixture.nodeID)
	h.collectNftablesNodeTraffic(fixture.nodeID, cfg, time.Unix(1700000060, 0))

	if got := mustHandlerCount(t, h, `SELECT in_flow FROM forward WHERE id = ?`, forward.ID); got != 400 {
		t.Fatalf("forward in_flow after first periodic collect = %d, want 400 (post-sync growth credited)", got)
	}
	if got := mustHandlerCount(t, h, `SELECT out_flow FROM forward WHERE id = ?`, forward.ID); got != 600 {
		t.Fatalf("forward out_flow after first periodic collect = %d, want 600 (post-sync growth credited)", got)
	}
	if got := mustHandlerCount(t, h, `SELECT COALESCE((SELECT daily_used_bytes FROM user_quota WHERE user_id = 1), 0)`); got != 1000 {
		t.Fatalf("daily quota usage = %d, want 1000", got)
	}
}
