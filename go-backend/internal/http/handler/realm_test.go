package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// TestRealmForwardRenderAndTraffic 是 realm 的端到端计费契约测试：
// 渲染（端点/计数端口）→ 摄入分流 → 与 nftables 共用的入账段 →
// forward 流量 + 用户配额 + nft_counter_state 基线推进/epoch 重置。
func TestRealmForwardRenderAndTraffic(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.7")
	tunnelID := seedTunnelForNftables(t, h, "realm-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	// 我方 seed 隧道 Flow=1（单向）；本契约断言双向入账，改成双向（与
	// TestCollectNftablesNodeTrafficGrowthAppliesFlowAndUpdatesState 同口径）。
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443,2.2.2.2:443", 20000)

	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil || !tunnelIsRealm(tunnel) {
		t.Fatalf("tunnel engine not loaded: %+v %v", tunnel, err)
	}
	k, err := h.ensureRealmKernel(nodeID, true)
	if err != nil || k == nil || k.Version != kernel.RealmVersion {
		t.Fatalf("realm kernel: %+v %v", k, err)
	}

	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	cfg := payload["files"].([]kernel.File)[0].Content
	for _, want := range []string{`listen = "[::]:20000"`, `remote = "1.1.1.1:443"`, `extra_remotes = ["2.2.2.2:443"]`, `balance = "roundrobin: 1, 1"`} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("config misses %s:\n%s", want, cfg)
		}
	}
	ports := payload["ports"].([]map[string]interface{})
	if len(ports) != 1 || ports[0]["inboundId"] != kernel.RealmCounterID(forward.ID) || ports[0]["port"] != 20000 {
		t.Fatalf("ports = %v", ports)
	}
	if payload["enabled"] != true {
		t.Fatal("realm with a forward should run")
	}

	// Pausing renders before the status changes: the override drops it.
	paused, _, err := h.buildKernelApplyPayloadWith(k, map[int64]bool{forward.ID: false})
	if err != nil {
		t.Fatal(err)
	}
	if paused["enabled"] != false || len(paused["ports"].([]map[string]interface{})) != 0 {
		t.Fatalf("paused payload still runs: %v", paused)
	}

	// Counters: the first sample sets the baseline, later ones are credited.
	counter := func(dir string, bytes int64) agentKernelCounter {
		return agentKernelCounter{InboundID: kernel.RealmCounterID(forward.ID), Proto: "tcp", Dir: dir, Bytes: bytes}
	}
	now := time.Now()
	h.applyRealmCounters(nodeID, "e1", []agentKernelCounter{counter("in", 100), counter("out", 1000)}, now)
	h.applyRealmCounters(nodeID, "e1", []agentKernelCounter{counter("in", 300), counter("out", 1500)}, now.Add(time.Minute))
	var flow struct{ InFlow, OutFlow int64 }
	if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
		t.Fatal(err)
	}
	if flow.InFlow != 200 || flow.OutFlow != 500 {
		t.Fatalf("forward flow = %+v, want 200/500", flow)
	}
	// 用户配额同批入账（倍率 1、双向）：200 + 500 = 700。
	if got := mustHandlerCount(t, h, `SELECT COALESCE((SELECT daily_used_bytes FROM user_quota WHERE user_id = 1), 0)`); got != 700 {
		t.Fatalf("expected daily quota usage=700, got %d", got)
	}
	// 基线推进：nft_counter_state 以 "realm:e1" 为代际 hash 落到最新样本值。
	states, err := h.repo.GetNftCounterStatesByNode(nodeID)
	if err != nil {
		t.Fatalf("load states: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("expected two realm counter states, got %+v", states)
	}
	for _, st := range states {
		if st.ForwardID != forward.ID || st.RuleHash != "realm:e1" {
			t.Fatalf("unexpected realm state: %+v", st)
		}
	}
	// A new agent epoch restarts the counters without crediting them.
	h.applyRealmCounters(nodeID, "e2", []agentKernelCounter{counter("in", 50)}, now.Add(2*time.Minute))
	if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
		t.Fatal(err)
	}
	if flow.InFlow != 200 {
		t.Fatalf("epoch change credited traffic: %+v", flow)
	}
	states, err = h.repo.GetNftCounterStatesByNode(nodeID)
	if err != nil {
		t.Fatalf("reload states: %v", err)
	}
	for _, st := range states {
		if st.RuleHash == "realm:e2" && st.Bytes != 50 {
			t.Fatalf("epoch e2 baseline = %d, want 50", st.Bytes)
		}
	}
}

func TestNormalizeTunnelEngine(t *testing.T) {
	for in, want := range map[string]string{"": "", "gost": "", "realm": "realm", " Realm ": "realm"} {
		if got, err := normalizeTunnelEngine(in); err != nil || got != want {
			t.Fatalf("%q -> %q %v", in, got, err)
		}
	}
	if _, err := normalizeTunnelEngine("haproxy"); err == nil {
		t.Fatal("unknown engine accepted")
	}
}

// TestRealmEngineSwitchConverges 是引擎切换冒烟：gost→realm 后节点出现
// realm 内核且渲染含端点；realm→gost 后渲染收敛为空闲。节点命令发送层
// （ws.Server 具体类型）无测试拦截缝，调用顺序由 switchTunnelEngine 的
// 串行代码结构保证（与上游同序），此处断言两侧的收敛终态。
func TestRealmEngineSwitchConverges(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-switch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.9")
	tunnelID := seedTunnelForNftables(t, h, "switch-tunnel", nodeID)
	_ = seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 20000)

	if k, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeRealm); err != nil || k != nil {
		t.Fatalf("realm kernel before switch: %+v %v", k, err)
	}

	// gost -> realm（DB 已提交 engine，switch 负责运行时迁移）。
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID})
	k, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeRealm)
	if err != nil || k == nil {
		t.Fatalf("realm kernel after switch: %+v %v", k, err)
	}
	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if payload["enabled"] != true || len(payload["ports"].([]map[string]interface{})) != 1 {
		t.Fatalf("realm payload after switch not running: %v", payload)
	}

	// realm -> gost：realm 渲染先收敛为空闲（端口释放），随后 gost 服务同步。
	if err := r.DB().Exec(`UPDATE tunnel SET engine = '' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	h.switchTunnelEngine(tunnelID, true, []int64{nodeID}, []int64{nodeID})
	payload, _, err = h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	if payload["enabled"] != false || len(payload["ports"].([]map[string]interface{})) != 0 {
		t.Fatalf("realm payload after switch-back should be idle: %v", payload)
	}
}
