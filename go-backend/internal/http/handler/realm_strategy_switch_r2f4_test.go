package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R2-F4-01：引擎切换 gost→realm 是 A4-004 策略门的第四写面。此前
// switchTunnelEngine 径直删 gost 服务、以 realm 重渲染，多目标
// fifo/rand 被静默改写为轮询。现四写面同口径：create/update/批量
// 换隧道/隧道编辑切引擎，任一面把多目标非 round/hash 转发置于 realm
// 之下都点名拒绝。
//
// R2-F4-02：策略门计数按归一后有效段（remotePartHasEffectiveTarget）
// ——"#"/纯 scheme 等归一后无实质内容的段不算目标，只渲染 1 个目标
// 的转发不得被误拒；归一后仍是真目标的 scheme 段照常计数。

func TestValidateRealmForwardStrategyCountsEffectiveTargets(t *testing.T) {
	realmTunnel := &tunnelRecord{Engine: "realm"}
	pass := []string{
		"1.2.3.4:80,#",          // 垃圾段不算目标：实际单目标
		"1.2.3.4:80,http://",    // 纯 scheme 段归一为空
		"1.2.3.4:80\n#",        // 换行分隔同口径
		"1.2.3.4:80, ,#frag",    // 空白/锚点段
		"http://1.2.3.4:80/,#", // 首段带 scheme 仍是同一个目标
	}
	for _, addr := range pass {
		if err := validateRealmForwardStrategy(realmTunnel, addr, "fifo"); err != nil {
			t.Fatalf("R2-F4-02: %q renders one effective target, fifo must pass: %v", addr, err)
		}
	}
	reject := []string{
		"1.2.3.4:80,#,5.6.7.8:80",        // 垃圾段之外仍有第二目标
		"1.2.3.4:80,http://5.6.7.8:80/p", // scheme 段归一后是真目标
		"1.2.3.4:80,5.6.7.8:80",          // 基线双目标
	}
	for _, addr := range reject {
		if err := validateRealmForwardStrategy(realmTunnel, addr, "fifo"); err == nil {
			t.Fatalf("R2-F4-02: %q renders two effective targets, fifo must be rejected", addr)
		}
	}
}

func TestValidateRealmEngineSwitchForwardsUnits(t *testing.T) {
	mk := func(name, addr, strategy string) forwardRecord {
		return forwardRecord{Name: name, RemoteAddr: addr, Strategy: strategy}
	}
	if err := validateRealmEngineSwitchForwards(nil); err != nil {
		t.Fatalf("no forwards must pass: %v", err)
	}
	ok := []forwardRecord{
		mk("multi-round", "203.0.113.5:443,203.0.113.6:443", "round"),
		mk("multi-hash", "203.0.113.5:443,203.0.113.6:443", "hash"),
		mk("single-fifo", "203.0.113.5:443", "fifo"),
		mk("junk-single-fifo", "203.0.113.5:443,#", "fifo"),
	}
	if err := validateRealmEngineSwitchForwards(ok); err != nil {
		t.Fatalf("switchable set must pass: %v", err)
	}
	bad := []forwardRecord{
		mk("fine", "203.0.113.5:443,203.0.113.6:443", "round"),
		mk("main-backup", "203.0.113.7:443,203.0.113.8:443", "fifo"),
	}
	err := validateRealmEngineSwitchForwards(bad)
	if err == nil {
		t.Fatal("R2-F4-01: multi-target fifo must block the engine switch")
	}
	if !strings.Contains(err.Error(), "main-backup") {
		t.Fatalf("rejection must name the offending forward, got: %v", err)
	}
	if !strings.Contains(err.Error(), "realm 引擎仅支持轮询") || !strings.Contains(err.Error(), "fifo") {
		t.Fatalf("rejection must carry the shared strategy wording, got: %v", err)
	}
}

// 写面 1–3 普查钉格：与 A4-004 既有 create 钉格同夹具，逐面把同一
// 多目标 fifo 形态送进 realm——create 拒、update 拒、批量换隧道记
// 失败条目；写面 4（切引擎）由下方两格钉死。
func TestRealmStrategyGateCreateUpdateBatchFaces(t *testing.T) {
	h, realmTunnelID := setupRealmStrategyFixture(t)

	// 写面 1：create。
	code, msg := callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": realmTunnelID, "name": "census-create",
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443",
		"inPort":     30005, "strategy": "fifo",
	})
	if code == 0 || !strings.Contains(msg, "realm 引擎仅支持轮询") {
		t.Fatalf("create face must reject multi-target fifo, code=%d msg=%s", code, msg)
	}

	// 写面 2：update（先建单目标 fifo，再改成多目标）。
	code, msg = callForwardCreate(t, h, map[string]interface{}{
		"tunnelId": realmTunnelID, "name": "census-update",
		"remoteAddr": "203.0.113.5:443",
		"inPort":     30006, "strategy": "fifo",
	})
	if code != 0 {
		t.Fatalf("single-target fifo create must succeed, code=%d msg=%s", code, msg)
	}
	var fwdID int64
	if err := h.repo.DB().Raw(`SELECT id FROM forward WHERE name = 'census-update'`).Scan(&fwdID).Error; err != nil || fwdID == 0 {
		t.Fatalf("lookup created forward: id=%d err=%v", fwdID, err)
	}
	res := httptest.NewRecorder()
	h.forwardUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": fwdID, "tunnelId": realmTunnelID,
		"remoteAddr": "203.0.113.5:443,203.0.113.6:443", "strategy": "fifo",
	}))
	var upd fix2TunnelLockResp
	if err := json.Unmarshal(res.Body.Bytes(), &upd); err != nil {
		t.Fatalf("decode forwardUpdate: %v body=%s", err, res.Body.String())
	}
	if upd.Code == 0 || !strings.Contains(upd.Msg, "realm 引擎仅支持轮询") {
		t.Fatalf("update face must reject multi-target fifo, code=%d msg=%s", upd.Code, upd.Msg)
	}

	// 写面 3：批量换隧道（gost 隧道里的多目标 fifo 换入 realm 隧道）。
	var nodeID int64
	if err := h.repo.DB().Raw(`SELECT node_id FROM chain_tunnel WHERE tunnel_id = ? LIMIT 1`, realmTunnelID).Scan(&nodeID).Error; err != nil || nodeID == 0 {
		t.Fatalf("lookup entry node: id=%d err=%v", nodeID, err)
	}
	gostTunnelID := seedTunnelForNftables(t, h, "census-gost-tunnel", nodeID)
	if err := h.repo.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, gostTunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	mover := seedForwardForNftables(t, h, gostTunnelID, nodeID, "203.0.113.5:443,203.0.113.6:443", 30007)
	res = httptest.NewRecorder()
	h.forwardBatchChangeTunnel(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"forwardIds": []int64{mover.ID}, "targetTunnelId": realmTunnelID,
	}))
	out := decodeFix3BatchResult(t, res)
	if out.Data.FailCount != 1 || len(out.Data.Failures) != 1 ||
		!strings.Contains(out.Data.Failures[0].Reason, "realm 引擎仅支持轮询") {
		t.Fatalf("batch face must fail the move with the strategy wording, got %+v body=%s", out.Data, res.Body.String())
	}
}

// 写面 4 直调兜底：绕过 tunnelUpdate 直调 switchTunnelEngine 也不得
// 静默转换——删任何 gost 服务前先整单拒绝并点名转发。
func TestSwitchTunnelEngineRejectsMultiTargetFifo(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/realm-switch-gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.16")
	tunnelID := seedTunnelForNftables(t, h, "switch-gate-tunnel", nodeID)
	_ = seedForwardForNftables(t, h, tunnelID, nodeID, "203.0.113.5:443,203.0.113.6:443", 20000)

	err = h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID})
	if err == nil {
		t.Fatal("R2-F4-01: switch with multi-target fifo forward must be rejected")
	}
	if !strings.Contains(err.Error(), "nft-forward") || !strings.Contains(err.Error(), "realm 引擎仅支持轮询") {
		t.Fatalf("rejection must name the forward and the strategy wording, got: %v", err)
	}

	// 改为 round 后同一切换放行（节点离线按既有自愈语义不算硬失败）。
	if err := r.DB().Exec(`UPDATE forward SET strategy = 'round' WHERE tunnel_id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.switchTunnelEngine(tunnelID, false, []int64{nodeID}, []int64{nodeID}); err != nil {
		t.Fatalf("switch with multi-target round must proceed, got %v", err)
	}
}

// 写面 4 全链：tunnelUpdate 切引擎在落库前整单拒绝——回执点名转发、
// 隧道 engine 保持不变；多目标 round 则放行且 engine 落 realm。
func TestTunnelUpdateEngineSwitchToRealmStrategyGate(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 95, "r2-switch-reject", "", 0)
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateForwardTx(1, "admin", "switch-fifo", 95,
		"203.0.113.5:443,203.0.113.6:443", "fifo", now, 1,
		[]int64{10}, 30005, "", nil, 0, 0, nil, 0, 0, 0); err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":95,"name":"r2-switch-reject","type":1,"status":1,"engine":"realm","inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code == 0 {
		t.Fatal("R2-F4-01: engine switch to realm with multi-target fifo must be rejected")
	}
	if !strings.Contains(msg, "switch-fifo") || !strings.Contains(msg, "realm 引擎仅支持轮询") {
		t.Fatalf("rejection must name the forward and the strategy wording, got: %s", msg)
	}
	var engine string
	if err := h.repo.DB().Raw(`SELECT COALESCE(engine, '') FROM tunnel WHERE id = 95`).Scan(&engine).Error; err != nil {
		t.Fatal(err)
	}
	if engine != "" {
		t.Fatalf("rejected switch must not persist the engine, got %q", engine)
	}
}

func TestTunnelUpdateEngineSwitchToRealmAllowsMultiTargetRound(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 96, "r2-switch-ok", "", 0)
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateForwardTx(1, "admin", "switch-round", 96,
		"203.0.113.5:443,203.0.113.6:443", "round", now, 1,
		[]int64{10}, 30006, "", nil, 0, 0, nil, 0, 0, 0); err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":96,"name":"r2-switch-ok","type":1,"status":1,"engine":"realm","inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("engine switch with multi-target round must succeed, code=%d msg=%s", code, msg)
	}
	var engine string
	if err := h.repo.DB().Raw(`SELECT COALESCE(engine, '') FROM tunnel WHERE id = 96`).Scan(&engine).Error; err != nil {
		t.Fatal(err)
	}
	if engine != "realm" {
		t.Fatalf("accepted switch must persist realm engine, got %q", engine)
	}
}
