package handler

// R2 审计第 2 轮修复回归：
//   - R2-α-01：onLatencyConfigChanged 的配置后重测不得计入延迟告警
//     streak——轮次语义由 probeRoundKind 显式声明，全量刷新轮
//     （probeRoundFull）保留整表替换、但不喂 streak。
//   - R2-α-02：以 len(ids)==0 推断定时轮的门控删除后，「展开-再读」
//     竞态即便产出空 ids 切片，定向轮也结构性不可达全量计数语义。
//   - B2-01：分享地址尾点（FQDN 根点）保存期/拉取期规范化；其余
//     非规范值（下划线标签、Unicode IDN、回环/链路本地字面量）
//     维持拒绝（BC，见 1.4.0 发版说明）。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func latencyAlertState(h *Handler) (streak, down int) {
	h.latencyAlerts.mu.Lock()
	defer h.latencyAlerts.mu.Unlock()
	return len(h.latencyAlerts.streak), len(h.latencyAlerts.down)
}

// TestProbeRoundKindSemantics pins the three round kinds at the probe
// mechanism itself: full rounds replace the whole cache without feeding
// streaks, targeted rounds merge, scheduled rounds count exactly as
// before (R2-α-01 mechanism, R2-α-02 by construction).
func TestProbeRoundKindSemantics(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	ctx := context.Background()
	a := mustCreateLatencyNode(t, r, "a", 1, 0)
	b := mustCreateLatencyNode(t, r, "b", 1, 0)
	stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) {
		return nil, errors.New("测试点连接失败")
	})

	// Full refresh rounds (the config-save shape): cache fills and
	// refreshes, alert state untouched no matter how often they run.
	for i := 0; i < latencyAlertStreak+1; i++ {
		if _, err := h.probeNodeLatency(ctx, nil, probeRoundFull); err != nil {
			t.Fatal(err)
		}
	}
	if s, d := latencyAlertState(h); s != 0 || d != 0 {
		t.Fatalf("full refresh rounds counted alert state: streak=%d down=%d", s, d)
	}
	if _, ok := h.nodeLatency.get(a); !ok {
		t.Fatal("full round must cache node a")
	}
	if _, ok := h.nodeLatency.get(b); !ok {
		t.Fatal("full round must cache node b")
	}

	// Targeted rounds merge: taking b offline must not prune its
	// cached entry when only a is re-probed.
	if err := r.DB().Model(&model.Node{}).Where("id = ?", b).Update("status", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := h.probeNodeLatency(ctx, []int64{a}, probeRoundTargeted); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.nodeLatency.get(b); !ok {
		t.Fatal("targeted round must merge, not prune unmeasured nodes")
	}

	// A full round prunes the now-offline node's entry — still
	// without touching streaks.
	if _, err := h.probeNodeLatency(ctx, nil, probeRoundFull); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.nodeLatency.get(b); ok {
		t.Fatal("full round must prune entries it did not measure")
	}
	if s, d := latencyAlertState(h); s != 0 || d != 0 {
		t.Fatalf("full round counted alert state: streak=%d down=%d", s, d)
	}

	// Control: a scheduled round counts, for the online node only.
	if _, err := h.probeNodeLatency(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	if s, _ := latencyAlertState(h); s != 1 {
		t.Fatalf("scheduled round must count one streak key (online node), got %d", s)
	}
}

// TestLatencyConfigChangeRefreshDoesNotCountStreak is the R2-α-01
// reproduction at the wiring level: saving the latency-point config
// re-probes everything, and repeated saves amid a total outage must
// neither raise nor feed a latency alert.
func TestLatencyConfigChangeRefreshDoesNotCountStreak(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	mustCreateLatencyNode(t, r, "a", 1, 0)
	mustCreateLatencyNode(t, r, "b", 1, 0)
	var pings atomic.Int64
	stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) {
		pings.Add(1)
		return nil, errors.New("测试点连接失败")
	})
	h.jobsMu.Lock()
	h.jobsCtx = context.Background()
	h.jobsMu.Unlock()

	const rounds = latencyAlertStreak + 1
	for i := 0; i < rounds; i++ {
		h.onLatencyConfigChanged(latencyTargetsConfigKey, h.latencyPool())
	}
	// Each refresh round pings both nodes once (one enabled point).
	deadline := time.Now().Add(15 * time.Second)
	for pings.Load() < rounds*2 {
		if time.Now().After(deadline) {
			t.Fatalf("config-change refresh rounds did not run: %d pings", pings.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s, d := latencyAlertState(h); s != 0 || d != 0 {
		t.Fatalf("config-save refresh counted alert state: streak=%d down=%d", s, d)
	}
}

// TestEmptyExpansionRoundsCannotCountStreak is the R2-α-02 structural
// proof: a manual probe racing a concurrent create can end up probing
// with an empty ids slice after the new row appeared (the old code
// then counted a full round). With the round kind declared by the
// caller, the expanded set is measured and cached — and no streak can
// move, on either prober.
func TestEmptyExpansionRoundsCannotCountStreak(t *testing.T) {
	ctx := context.Background()

	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	node := mustCreateLatencyNode(t, r, "late", 1, 0)
	stubLatencyPing(t, func(int64, string) (map[string]interface{}, error) {
		return nil, errors.New("测试点连接失败")
	})
	for i := 0; i < latencyAlertStreak+1; i++ {
		if _, err := h.probeNodeLatency(ctx, []int64{}, probeRoundTargeted); err != nil {
			t.Fatal(err)
		}
	}
	if s, d := latencyAlertState(h); s != 0 || d != 0 {
		t.Fatalf("empty-ids targeted rounds counted alert state: streak=%d down=%d", s, d)
	}
	if _, ok := h.nodeLatency.get(node); !ok {
		t.Fatal("the appeared node must still be measured and cached")
	}

	h2, r2 := setupLatencyHandler(t, "")
	row := &model.NodeInbound{
		Name: "late-ext", Protocol: "ss", Listen: "127.0.0.1", Port: 9,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli(),
	}
	if err := r2.DB().Create(row).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < notifyInboundDownStreak+1; i++ {
		if _, err := h2.probeExternalInbounds(ctx, []int64{}, probeRoundTargeted); err != nil {
			t.Fatal(err)
		}
	}
	if streak, down := externalStreakState(h2, row.ID); streak != 0 || down {
		t.Fatalf("empty-ids targeted rounds touched inbound alert state: streak=%d down=%v", streak, down)
	}
	if p, _ := h2.externalProbes.get(row.ID); p.OK {
		t.Fatal("the appeared inbound must still be measured (and failing)")
	}
}

// TestShareHostTrailingDotNormalization pins B2-01: a single trailing
// dot (the FQDN root dot) is normalized away at validation time, while
// the other non-canonical shapes stay rejected.
func TestShareHostTrailingDotNormalization(t *testing.T) {
	node := shareTestNode()
	if err := validateShareHostValue("example.com."); err != nil {
		t.Fatalf("trailing-dot FQDN must pass validation: %v", err)
	}
	if err := validateShareAddress("custom", "example.com.", node); err != nil {
		t.Fatalf("trailing-dot FQDN must pass save validation: %v", err)
	}
	for _, host := range []string{
		"my_node.example.com", // underscore label
		"中文域名.cn",             // Unicode IDN (not punycode)
		"127.0.0.1",           // loopback literal
		"169.254.169.254",     // link-local literal
		"example.com..",       // two trailing dots: only one is normalized
		"localhost",
	} {
		if err := validateShareHostValue(host); err == nil {
			t.Fatalf("%q must stay rejected", host)
		}
	}
	if got := normalizeShareHost(" example.com. "); got != "example.com" {
		t.Fatalf("normalizeShareHost stored form = %q, want example.com", got)
	}
	if got := normalizeShareHost("example.com.."); got != "example.com." {
		t.Fatalf("normalizeShareHost must strip exactly one dot, got %q", got)
	}
	if got := shareHostValue("[example.com.]"); got != "example.com" {
		t.Fatalf("shareHostValue bracket+dot = %q, want example.com", got)
	}
	if got := shareHostValue("[2001:db8::9]"); got != "2001:db8::9" {
		t.Fatalf("shareHostValue IPv6 = %q, want 2001:db8::9", got)
	}
}

// TestInboundSaveStoresNormalizedShareHost: the inbound save surface
// stores the normalized value, so a stored row never carries the dot.
func TestInboundSaveStoresNormalizedShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-save.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "save-node", "192.0.2.1")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	entry := seedChainInbound(t, r, box, "save-in", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "::", Port: 443}, 0)
	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	mode := "custom"
	mkBody := func(host string) string {
		body, err := json.Marshal(inboundRequest{
			ID: entry.ID, Name: "save-in", Protocol: old.Protocol, Listen: old.Listen,
			Settings: old.Settings, Transport: old.Transport, Security: old.Security,
			ShareAddressMode: &mode, ShareHost: host,
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	if out := callInboundSave(t, h, true, mkBody("entry.example.com.")); out.Code != 0 {
		t.Fatalf("trailing-dot share host save code = %d (%s)", out.Code, out.Msg)
	}
	got, err := r.GetNodeInbound(entry.ID)
	if err != nil || got == nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.ShareHost != "entry.example.com" || got.ShareAddressMode != "custom" {
		t.Fatalf("stored share host = %q mode = %q, want normalized entry.example.com/custom", got.ShareHost, got.ShareAddressMode)
	}
	if out := callInboundSave(t, h, true, mkBody("my_node.example.com")); out.Code == 0 {
		t.Fatal("underscore share host must stay rejected at save")
	}
}

// TestInboundRelayCreateStoresNormalizedShareHost: the relay create
// surface stores the normalized value too.
func TestInboundRelayCreateStoresNormalizedShareHost(t *testing.T) {
	r, h := r2b2Open(t)
	if _, err := r.CreateUser("admin", "hash", 0, time.Now().Add(24*time.Hour).UnixMilli(), 1, 1, 100, 1, 0, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nodeID := seedRelayTargetNode(t, r, "r2-relay-entry", "198.51.100.21")
	tunnelID := seedTunnelForNftables(t, h, "r2-relay-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	inbound := &model.NodeInbound{
		Name: "r2-imported", Protocol: "ss", Listen: "198.51.100.9", Port: 8388,
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.DB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	payload := map[string]interface{}{
		"inboundId": inbound.ID, "tunnelId": tunnelID, "inPort": 30007, "name": "r2-relay",
		"shareHost": "relay.example.com.",
	}
	r2b2AssertCode0(t, "inboundRelayCreate trailing-dot", r2b2AuthPost(t, h.inboundRelayCreate, "/api/v1/inbound/relay/create", payload, "r2-relay-key"))
	var rel model.NodeInboundRelay
	if err := r.DB().Where("name = ?", "r2-relay").First(&rel).Error; err != nil {
		t.Fatal(err)
	}
	if rel.ShareHost != "relay.example.com" {
		t.Fatalf("stored relay share host = %q, want normalized relay.example.com", rel.ShareHost)
	}
}

// TestInboundEndpointsStoredTrailingDotShareHost: a stored row that
// still carries the dot (written before the fix, or by another path)
// pulls normally — the endpoint uses the normalized host and is not
// skipped as invalid.
func TestInboundEndpointsStoredTrailingDotShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-dot.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "dotnode", "192.0.2.1")
	row := seedShareInbound(t, r, nodeID, "dot-in")
	row.ShareAddressMode = "custom"
	row.ShareHost = "entry.example.com."
	if err := r.SaveNodeInbound(row); err != nil {
		t.Fatal(err)
	}
	logs := captureHandlerLogs(t)
	endpoints, _ := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || endpoints[0].Host != "entry.example.com" {
		t.Fatalf("stored trailing-dot host must flow normalized: %+v", endpoints)
	}
	if strings.Contains(logs.String(), "invalid custom share host") {
		t.Fatalf("trailing-dot host must not be treated as invalid: %q", logs.String())
	}
}

// TestInboundEndpointsStoredTrailingDotRelayShareHost: the relay pull
// path likewise uses the normalized host instead of falling back to
// the entry node address.
func TestInboundEndpointsStoredTrailingDotRelayShareHost(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-dot-relay.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	entry := seedRelayTargetNode(t, r, "dot-entry", "198.51.100.10")
	landing := seedRelayTargetNode(t, r, "dot-landing", "192.0.2.30")
	row := seedShareInbound(t, r, landing, "dot-landing-in")
	now := time.Now().UnixMilli()
	db := r.DB()
	tunnel := model.Tunnel{Name: "dot-line", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	fwd := model.Forward{UserID: 1, UserName: "admin", Name: "dot-line", TunnelID: tunnel.ID, RemoteAddr: "203.0.113.1:443", Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := db.Create(&fwd).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ForwardPort{ForwardID: fwd.ID, NodeID: entry, Port: 45001}).Error; err != nil {
		t.Fatal(err)
	}
	rel := model.NodeInboundRelay{InboundID: row.ID, ForwardID: fwd.ID, Name: "dot-relay", ShareHost: "relay.example.com.", Managed: 1, CreatedTime: now}
	if err := db.Create(&rel).Error; err != nil {
		t.Fatal(err)
	}
	logs := captureHandlerLogs(t)
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	var relayEP *kernel.Endpoint
	for i, e := range endpoints {
		if kinds[i] == "relay" {
			ep := e
			relayEP = &ep
		}
	}
	if relayEP == nil {
		t.Fatalf("relay endpoint missing: %+v", endpoints)
	}
	if relayEP.Host != "relay.example.com" || relayEP.Port != 45001 {
		t.Fatalf("relay endpoint must use the normalized host, got %+v", relayEP)
	}
	if strings.Contains(logs.String(), "invalid relay share host") {
		t.Fatalf("trailing-dot relay host must not fall back: %q", logs.String())
	}
}
