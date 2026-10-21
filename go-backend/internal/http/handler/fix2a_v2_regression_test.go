package handler

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/kernel"
	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// FIX2-A：对抗复审 V2 的回归批。每条对应 V2.md 编号，均为「修复前
// 失败、修复后通过」纪律（V2-02 探针除外，其结论见 FIX2-A 报告）。

// ---------- 通用小工具 ----------

func quotaPausedIDSet(t *testing.T, h *Handler, userID int64) map[int64]bool {
	t.Helper()
	view, err := h.repo.GetUserQuotaView(userID, time.Now())
	if err != nil || view == nil {
		t.Fatalf("quota view: %v / %v", view, err)
	}
	out := make(map[int64]bool)
	for _, part := range strings.Split(view.PausedForwardIDs, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 {
			out[id] = true
		}
	}
	return out
}

func seedExceededQuota(t *testing.T, h *Handler, userID int64) *model.UserQuotaView {
	t.Helper()
	r := h.repo
	now := time.Now()
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
	return view
}

// ---------- V2-01：配额暂停首轮失败必须在后续执法中重试 ----------

// TestFix2AEnforceQuotaRetriesAfterNftFailure 构造二段场景：首轮 nft
// reconcile 失败（转发保持 active——A4-001 已有语义），节点恢复后以
// 最新视图（disabled_by_quota=1）再执法，必须把该转发真暂停并写入
// 恢复依据。修复前入口对 DisabledByQuota==1 恒早退，永不重试。
func TestFix2AEnforceQuotaRetriesAfterNftFailure(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-fix2a-retry", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 22000)

	view := seedExceededQuota(t, h, 1)

	// 第一轮：reconcile 失败 → 转发保持 active（既有语义）。
	h.enforceUserQuotaIfNeeded(1, view)
	fr, err := h.repo.GetForwardRecord(f1.ID)
	if err != nil || fr == nil {
		t.Fatalf("get forward: %v", err)
	}
	if fr.Status != model.ForwardStatusActive {
		t.Fatalf("phase1: forward must stay active when reconcile fails, got %d", fr.Status)
	}
	if quotaPausedIDSet(t, h, 1)[f1.ID] {
		t.Fatalf("phase1: failed forward must not be in PausedForwardIDs")
	}

	// 节点恢复：reconcile 不再失败。
	manager.mu.Lock()
	manager.reconcileErr = nil
	manager.mu.Unlock()

	// 第二轮：调用方传入的是 DB 最新视图（disabled 已置位）。
	view2, err := h.repo.GetUserQuotaView(1, time.Now())
	if err != nil || view2 == nil {
		t.Fatalf("quota view phase2: %v / %v", view2, err)
	}
	if view2.DisabledByQuota != 1 {
		t.Fatalf("phase2 precondition: disabled_by_quota must be 1, got %d", view2.DisabledByQuota)
	}
	h.enforceUserQuotaIfNeeded(1, view2)

	fr, err = h.repo.GetForwardRecord(f1.ID)
	if err != nil || fr == nil {
		t.Fatalf("get forward phase2: %v", err)
	}
	if fr.Status != model.ForwardStatusPaused {
		t.Fatalf("V2-01: forward must be paused on retry after node recovery, got status %d", fr.Status)
	}
	if !quotaPausedIDSet(t, h, 1)[f1.ID] {
		t.Fatalf("V2-01: retried forward must enter PausedForwardIDs (restore basis)")
	}
}

// ---------- V2-02 探针：并发 enforce 的 trim 覆写竞态 ----------

// failFirstReconcileManager 只让第一次 Reconcile 失败（模拟首轮节点
// 故障、随后恢复），其余委托 fakeNftablesManager。
type failFirstReconcileManager struct {
	inner *fakeNftablesManager
	calls atomic.Int32
}

func (m *failFirstReconcileManager) Test(ctx context.Context, cfg runtimenft.SSHConfig) error {
	return m.inner.Test(ctx, cfg)
}

func (m *failFirstReconcileManager) Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error) {
	if m.calls.Add(1) == 1 {
		return runtimenft.ApplyResult{}, errFakeSSHDown
	}
	return m.inner.Reconcile(ctx, cfg, plan)
}

func (m *failFirstReconcileManager) Clear(ctx context.Context, cfg runtimenft.SSHConfig) error {
	return m.inner.Clear(ctx, cfg)
}

func (m *failFirstReconcileManager) CollectCounters(ctx context.Context, cfg runtimenft.SSHConfig) ([]runtimenft.CounterSample, error) {
	return m.inner.CollectCounters(ctx, cfg)
}

// TestFix2AEnforceQuotaConcurrentInvariant 是 V2-02 的并发探针：多路
// 入账同刻对同一用户并发执法（首轮 reconcile 失败制造分叉的
// pausedSet），最终不变量：实际 paused 集合 == PausedForwardIDs
// 集合（恢复依据不得丢、不得多），且全部转发最终被暂停。
func TestFix2AEnforceQuotaConcurrentInvariant(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &failFirstReconcileManager{inner: &fakeNftablesManager{}}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "nft-tunnel-fix2a-conc", fixture.nodeID)
	f1 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 22010)
	f2 := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9002", 22011)

	view := seedExceededQuota(t, h, 1)

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h.enforceUserQuotaIfNeeded(1, view)
		}()
	}
	close(start)
	wg.Wait()
	// 收尾再执法一轮（模拟下一次流量上报）：任何首轮失败的转发都应
	// 在此被重试暂停。
	if v, err := h.repo.GetUserQuotaView(1, time.Now()); err == nil && v != nil {
		h.enforceUserQuotaIfNeeded(1, v)
	}

	pausedSet := quotaPausedIDSet(t, h, 1)
	for _, f := range []*forwardRecord{f1, f2} {
		fr, err := h.repo.GetForwardRecord(f.ID)
		if err != nil || fr == nil {
			t.Fatalf("get forward: %v", err)
		}
		paused := fr.Status == model.ForwardStatusPaused
		if !paused {
			t.Fatalf("V2-02: forward %d must end paused, got status %d", f.ID, fr.Status)
		}
		if paused != pausedSet[f.ID] {
			t.Fatalf("V2-02: forward %d paused=%v but in PausedForwardIDs=%v (restore basis diverged)",
				f.ID, paused, pausedSet[f.ID])
		}
	}
}

// ---------- V2-06：nodeUpdate 不得把非 22 的 SSH 端口改回 22 ----------

func TestFix2ANodeUpdatePreservesSSHPort(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix2a-ssh-port.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, forward_mode)
		VALUES('nft-ssh-port', 's', '198.51.100.30', '30000-30010', ?, ?, 0, '[::]', '[::]', 'nftables')
	`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.UpsertNodeSSHConfig(nodeID, repo.NftSSHConfigInput{
		Host: "198.51.100.30", Port: 2222, Username: "root",
		AuthType: "password", Password: "secret", SudoMode: "none",
	}, nowMs); err != nil {
		t.Fatalf("seed ssh config: %v", err)
	}
	readPort := func() int {
		cfg, err := r.GetNodeSSHConfig(nodeID)
		if err != nil || cfg == nil {
			t.Fatalf("get ssh config: %v", err)
		}
		return cfg.Port
	}

	// 不带 sshConfig 的部分更新（改名/改备注类调用）：整段不动。
	res := postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{"id": nodeID, "remark": "x"})
	if code, _ := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("nodeUpdate failed: code=%d body=%s", code, res.Body.String())
	}
	if got := readPort(); got != 2222 {
		t.Fatalf("V2-06: ssh port rewritten to %d by sshConfig-less update, want 2222", got)
	}

	// 带部分 sshConfig（只有 username、无 port）：port 保既有值。
	res = postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id": nodeID, "sshConfig": map[string]interface{}{"username": "root"},
	})
	if code, _ := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("nodeUpdate partial sshConfig failed: code=%d body=%s", code, res.Body.String())
	}
	if got := readPort(); got != 2222 {
		t.Fatalf("V2-06: ssh port rewritten to %d by port-less sshConfig update, want 2222", got)
	}

	// create 路径（preserveSecrets=false）不带 port：不得再默认 22，
	// 必须由既有校验明确报错。
	err = h.persistNodeSSHConfig(nodeID, map[string]interface{}{
		"sshConfig": map[string]interface{}{
			"host": "198.51.100.30", "username": "root",
			"authType": "password", "password": "secret",
		},
	}, "nftables", nowMs, false)
	if err == nil {
		t.Fatalf("V2-06: create-path persist without port must fail validation, got nil error (port silently defaulted)")
	}
	if got := readPort(); got != 2222 {
		t.Fatalf("failed create-path persist must not touch stored config, port=%d", got)
	}
}

// ---------- V2-08 / V2-05：转发计数入账（并发同一样本、realm epoch 跳变） ----------

type fix2aAccountingFixture struct {
	h         *Handler
	nodeID    int64
	tunnelID  int64
	forwardID int64
}

func seedFix2AAccounting(t *testing.T) fix2aAccountingFixture {
	t.Helper()
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "fix2a-acct-tunnel", fixture.nodeID)
	if err := h.repo.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:9001", 22020)
	return fix2aAccountingFixture{h: h, nodeID: fixture.nodeID, tunnelID: tunnelID, forwardID: forward.ID}
}

func (f fix2aAccountingFixture) seedBaseline(t *testing.T, hash string, inBytes, outBytes uint64) {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	inputs := []repo.NftCounterStateInput{
		{NodeID: f.nodeID, ForwardID: f.forwardID, Protocol: "tcp", Direction: runtimenft.CounterDirectionToTarget, RuleHash: hash, Bytes: inBytes, CollectedTime: nowMs},
		{NodeID: f.nodeID, ForwardID: f.forwardID, Protocol: "tcp", Direction: runtimenft.CounterDirectionFromTarget, RuleHash: hash, Bytes: outBytes, CollectedTime: nowMs},
	}
	if err := f.h.repo.UpsertNftCounterStates(inputs, nowMs); err != nil {
		t.Fatalf("seed baseline: %v", err)
	}
}

func (f fix2aAccountingFixture) readFlow(t *testing.T) (int64, int64) {
	t.Helper()
	var flow struct{ InFlow, OutFlow int64 }
	if err := f.h.repo.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, f.forwardID).Scan(&flow).Error; err != nil {
		t.Fatal(err)
	}
	return flow.InFlow, flow.OutFlow
}

func (f fix2aAccountingFixture) readBaseline(t *testing.T) (map[string]int64, map[string]string) {
	t.Helper()
	states, err := f.h.repo.GetNftCounterStatesByNode(f.nodeID)
	if err != nil {
		t.Fatalf("load states: %v", err)
	}
	bytes := map[string]int64{}
	hashes := map[string]string{}
	for _, st := range states {
		if st.ForwardID != f.forwardID {
			continue
		}
		bytes[st.Direction] = st.Bytes
		hashes[st.Direction] = st.RuleHash
	}
	return bytes, hashes
}

func realmSamples(forwardID int64, inBytes, outBytes uint64) []runtimenft.CounterSample {
	return []runtimenft.CounterSample{
		{ForwardID: forwardID, Protocol: "tcp", Direction: runtimenft.CounterDirectionToTarget, Bytes: inBytes},
		{ForwardID: forwardID, Protocol: "tcp", Direction: runtimenft.CounterDirectionFromTarget, Bytes: outBytes},
	}
}

// TestFix2AForwardAccountingConcurrentSameSample 并发以同一样本入账
// （切换 settle 轮询 × 周期轮询 × 重连对账轮询同刻到达）：同一增量
// 只能入账一次。修复前基线在事务外读、无 per-node 串行化，同一样
// 本可被多轮重复差分、重复入账。
func TestFix2AForwardAccountingConcurrentSameSample(t *testing.T) {
	for iter := 0; iter < 5; iter++ {
		f := seedFix2AAccounting(t)
		f.seedBaseline(t, "realm:e1", 1000, 5000)
		hashes := map[int64]string{f.forwardID: "realm:e1"}
		samples := realmSamples(f.forwardID, 1600, 5600)

		const workers = 8
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, _, _, _ = f.h.accountForwardCounterSamples(f.nodeID, samples, hashes, time.Now())
			}()
		}
		close(start)
		wg.Wait()

		inF, outF := f.readFlow(t)
		if inF != 600 || outF != 600 {
			t.Fatalf("V2-08 iter %d: concurrent identical samples credited %d/%d, want exactly 600/600 (double count)", iter, inF, outF)
		}
	}
}

// TestFix2ARealmEpochJumpBaselineOnly 是 V2-05 的转发侧断言（与订阅
// 侧 repo 测试同口径）：epoch 跳变（hash 变化）视同基线重建——只
// 推进基线、不把新 epoch 的累计值计入用量；同 epoch 后续样本照常
// 差分。
func TestFix2ARealmEpochJumpBaselineOnly(t *testing.T) {
	f := seedFix2AAccounting(t)
	f.seedBaseline(t, "realm:e1", 1000, 5000)

	// epoch 跳变：agent 重启后累计自零起（300/900），不得全量计入。
	batch, _, _, _, err := f.h.accountForwardCounterSamples(f.nodeID, realmSamples(f.forwardID, 300, 900),
		map[int64]string{f.forwardID: "realm:e2"}, time.Now())
	if err != nil {
		t.Fatalf("epoch jump accounting: %v", err)
	}
	if batch != nil {
		t.Fatalf("V2-05: epoch jump must not produce a billing batch, got %+v", batch)
	}
	if inF, outF := f.readFlow(t); inF != 0 || outF != 0 {
		t.Fatalf("V2-05: epoch jump credited %d/%d, want 0/0 (baseline rebuild only)", inF, outF)
	}
	bytes, hashes := f.readBaseline(t)
	if bytes[runtimenft.CounterDirectionToTarget] != 300 || bytes[runtimenft.CounterDirectionFromTarget] != 900 ||
		hashes[runtimenft.CounterDirectionToTarget] != "realm:e2" {
		t.Fatalf("V2-05: baseline after epoch jump = %v / %v, want 300/900 @ realm:e2", bytes, hashes)
	}

	// 同 epoch（e2）后续样本照常差分：+100/+100。
	if _, _, _, _, err := f.h.accountForwardCounterSamples(f.nodeID, realmSamples(f.forwardID, 400, 1000),
		map[int64]string{f.forwardID: "realm:e2"}, time.Now()); err != nil {
		t.Fatalf("same-epoch accounting: %v", err)
	}
	if inF, outF := f.readFlow(t); inF != 100 || outF != 100 {
		t.Fatalf("same-epoch delta credited %d/%d, want 100/100", inF, outF)
	}
}

// ---------- V2-07：部分更新「在场但不可解析」必须 fail-closed ----------

func TestFix2AUpdateRejectsInvalidNumericFields(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix2a-invalid-fields.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	userID, err := r.CreateUser("fix2a-user", "hash", 1, nowMs+86400000, 1, 1, 100, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`UPDATE "user" SET num = 3 WHERE id = ?`, userID).Error; err != nil {
		t.Fatal(err)
	}

	// userUpdate.num 传垃圾值：不得静默套硬默认 10 落库。
	res := postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "fix2a-user", "num": "abc",
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatalf("V2-07: userUpdate with unparsable num must fail, got success body=%s", res.Body.String())
	}
	u, err := r.GetUserByID(userID)
	if err != nil || u == nil {
		t.Fatalf("get user: %v", err)
	}
	if u.Num != 3 {
		t.Fatalf("V2-07: num silently rewritten to %d on invalid input, want unchanged 3", u.Num)
	}

	// userUpdate.flow 传垃圾值：不得套 parseTrafficLimit 的默认 100。
	res = postJSONToHandler(t, h.userUpdate, map[string]interface{}{
		"id": userID, "user": "fix2a-user", "flow": "abc",
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatalf("V2-07: userUpdate with unparsable flow must fail, got success")
	}

	// nodeUpdate.expiryTime 传垃圾值：不得被 asInt64 默认 0 清空到期。
	nodeID := seedRelayTargetNode(t, r, "fix2a-node", "198.51.100.40")
	exp := nowMs + 90*86400000
	if err := r.DB().Exec(`UPDATE node SET expiry_time = ? WHERE id = ?`, exp, nodeID).Error; err != nil {
		t.Fatal(err)
	}
	res = postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id": nodeID, "expiryTime": "not-a-time",
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatalf("V2-07: nodeUpdate with unparsable expiryTime must fail, got success")
	}
	n, err := r.GetNodeByID(nodeID)
	if err != nil || n == nil {
		t.Fatalf("get node: %v", err)
	}
	if !n.ExpiryTime.Valid || n.ExpiryTime.Int64 != exp {
		t.Fatalf("V2-07: expiryTime silently cleared/changed on invalid input: %+v, want %d", n.ExpiryTime, exp)
	}

	// tunnelUpdate.flow 传垃圾值：不得套硬默认 1 落库。
	tunnelID := seedTunnelForNftables(t, h, "fix2a-invalid-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	res = postJSONToHandler(t, h.tunnelUpdate, map[string]interface{}{
		"id": tunnelID, "flow": "abc",
		"inNodeId": []map[string]interface{}{{"nodeId": nodeID, "protocol": "tls"}},
	})
	if code, _ := decodeEnvelopeA204(t, res); code == 0 {
		t.Fatalf("V2-07: tunnelUpdate with unparsable flow must fail, got success body=%s", res.Body.String())
	}
	var flowNow int64
	if err := r.DB().Raw(`SELECT flow FROM tunnel WHERE id = ?`, tunnelID).Scan(&flowNow).Error; err != nil {
		t.Fatal(err)
	}
	if flowNow != 2 {
		t.Fatalf("V2-07: tunnel flow silently rewritten to %d on invalid input, want unchanged 2", flowNow)
	}
}

// ---------- V2-04：离线切走 realm 后，重连必须先结算再下发 ----------

// TestFix2AOfflineRealmSwitchSettlesOnReconnect 全链场景：realm 隧道
// 在线建基线 → 产生未结算增量 → 节点离线 → 管理员切回 gost（settle
// 轮询静默跳过）→ 节点重连。重连后增量必须被结算入账（修复前
// onNodeOnline 的异步对账轮询与 redeploy 并发无序，redeploy 先到时
// agent 弃 realm 计数、增量永久漏计；且无任何 pending 标记）。
func TestFix2AOfflineRealmSwitchSettlesOnReconnect(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/fix2a-offline-switch.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.15")
	tunnelID := seedTunnelForNftables(t, h, "fix2a-offline-realm", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, nodeID, "1.1.1.1:443", 22030)

	readFlow := func() (int64, int64) {
		var flow struct{ InFlow, OutFlow int64 }
		if err := r.DB().Raw(`SELECT in_flow, out_flow FROM forward WHERE id = ?`, forward.ID).Scan(&flow).Error; err != nil {
			t.Fatal(err)
		}
		return flow.InFlow, flow.OutFlow
	}

	mock := &switchAcctMock{}
	mock.setCounters(1000, 5000)
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startSwitchAcctMockNode(t, srv.URL, "entry-secret", kernel.RealmCounterID(forward.ID), mock)
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")

	// 切到 realm（在线）：基线钉在 1000/5000、无增量入账。
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID}); err != nil {
		t.Fatalf("switch to realm: %v", err)
	}
	if inF, outF := readFlow(); inF != 0 || outF != 0 {
		t.Fatalf("flow after realm switch = %d/%d, want 0/0", inF, outF)
	}

	// realm 继续产生流量（未结算增量 600/600），随后节点离线。
	mock.setCounters(1600, 5600)
	stop()
	waitForCondition(t, 5*time.Second, func() bool { return !h.wsServer.IsNodeOnline(nodeID) }, "mock node offline")

	// 离线切回 gost：settle 轮询够不到节点（既有口径不算硬失败），
	// 但必须留下 pending 结算标记。
	if err := r.DB().Exec(`UPDATE tunnel SET engine = '' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	_ = h.switchTunnelEngine(tunnelID, true, []int64{nodeID}, []int64{nodeID})
	if !h.hasPendingRealmSettle(nodeID) {
		t.Fatalf("V2-04: offline realm->gost switch must record a pending settle marker for node %d", nodeID)
	}

	// 节点重连（agent 仍持 realm 计数）：onNodeOnline 必须先结算
	// 600/600 再下发 gost 运行时。
	stop2 := startSwitchAcctMockNode(t, srv.URL, "entry-secret", kernel.RealmCounterID(forward.ID), mock)
	defer stop2()
	waitForCondition(t, 10*time.Second, func() bool {
		inF, outF := readFlow()
		return inF == 600 && outF == 600
	}, "offline realm increment settled on reconnect")
	if h.hasPendingRealmSettle(nodeID) {
		t.Fatalf("V2-04: pending settle marker must be consumed after reconnect settle")
	}
}
