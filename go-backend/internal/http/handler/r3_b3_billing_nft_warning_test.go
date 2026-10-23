package handler

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3X-003 整链：repo meta（ratio=0）→ gost 与 nft/realm 两条 batch
// 构建路径的 quotaUsage 都必须为 0，且原始流量计数（forwardTraffic）
// 不得连带清零；O6-005 的 nft 零计费警告日志须复活可达。
// ratio=2 对照不变。
//
// R5X-013 断言更新登记（裁定驱动，verdicts §5.1 / N-35 名实对齐）：
// 原名 TestR3B3ZeroRatioWholeChainQuotaZeroRawPreserved 中的
// RawPreserved 实测对象仅为 forwardTraffic 指标桶与 quotaUsage，
// 从未覆盖 forward 行本身。R5X-013 裁定 delta 拆 raw/scaled 两组
// （forward 行落 raw、user/user_tunnel 行与 quota 落 scaled）后，
// 本用例更名为实测对象口径名并补 forward 行落账断言：经
// ApplyFlowUploadDeltasBatch 落账后断言 forward 行 = raw、
// user/user_tunnel 行 = scaled，并补 ratio=1 三面同值对照。
// 既有断言（quotaUsage / scaled delta / forwardTraffic / 警告日志）
// 逐字保留、未放宽。
func TestR3B3ZeroRatioWholeChainQuotaZeroForwardRowRawPreserved(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	tunnelID := seedTunnelForNftables(t, h, "r3b3-ratio-tunnel", fixture.nodeID)
	// 种子隧道默认 flow=1（单向，仅计入方向）；本用例按双向口径断言
	// 总额，须先置 flow=2，否则 ratio=2 对照的期望值不成立。
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatalf("set tunnel flow=2: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)

	setRatio := func(ratio float64) {
		t.Helper()
		if err := r.DB().Exec(`UPDATE tunnel SET traffic_ratio = ? WHERE id = ?`, ratio, tunnelID).Error; err != nil {
			t.Fatalf("set ratio: %v", err)
		}
	}

	setRatio(0)
	metas, err := r.GetFlowUploadForwardMetas([]int64{forward.ID})
	if err != nil {
		t.Fatalf("get metas: %v", err)
	}
	meta := metas[forward.ID]
	if meta.TrafficRatio != 0 {
		t.Fatalf("R3X-003: meta ratio must be 0, got %v", meta.TrafficRatio)
	}
	// R5X-013 落账断言需要真实 user_tunnel 行：种子夹具未建 UT 行时
	// meta.UserTunnelID=0（落账函数按 ID>0 跳过 UT 面），补建后重取 metas。
	if meta.UserTunnelID == 0 {
		if err := r.DB().Exec(`
			INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
			VALUES(?, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)
		`, meta.UserID, tunnelID).Error; err != nil {
			t.Fatalf("seed user_tunnel for landing assertions: %v", err)
		}
		metas, err = r.GetFlowUploadForwardMetas([]int64{forward.ID})
		if err != nil {
			t.Fatalf("re-get metas: %v", err)
		}
		meta = metas[forward.ID]
		if meta.UserTunnelID == 0 {
			t.Fatalf("user_tunnel seed did not surface in metas: %#v", meta)
		}
	}

	// R5X-013 落账断言助手：delta 落账后 forward 行须为 raw 口径，
	// user / user_tunnel 行须为 scaled 口径（配额增量另由 quotaUsage 断言）。
	assertLanded := func(label string, wantFwdIn, wantFwdOut, wantScaledIn, wantScaledOut int) {
		t.Helper()
		if got := mustQueryInt(t, r, `SELECT in_flow FROM forward WHERE id = ?`, forward.ID); got != wantFwdIn {
			t.Fatalf("%s: forward in_flow must be raw %d, got %d", label, wantFwdIn, got)
		}
		if got := mustQueryInt(t, r, `SELECT out_flow FROM forward WHERE id = ?`, forward.ID); got != wantFwdOut {
			t.Fatalf("%s: forward out_flow must be raw %d, got %d", label, wantFwdOut, got)
		}
		if got := mustQueryInt(t, r, `SELECT in_flow FROM user WHERE id = ?`, meta.UserID); got != wantScaledIn {
			t.Fatalf("%s: user in_flow must be scaled %d, got %d", label, wantScaledIn, got)
		}
		if got := mustQueryInt(t, r, `SELECT out_flow FROM user WHERE id = ?`, meta.UserID); got != wantScaledOut {
			t.Fatalf("%s: user out_flow must be scaled %d, got %d", label, wantScaledOut, got)
		}
		if got := mustQueryInt(t, r, `SELECT in_flow FROM user_tunnel WHERE id = ?`, meta.UserTunnelID); got != wantScaledIn {
			t.Fatalf("%s: user_tunnel in_flow must be scaled %d, got %d", label, wantScaledIn, got)
		}
		if got := mustQueryInt(t, r, `SELECT out_flow FROM user_tunnel WHERE id = ?`, meta.UserTunnelID); got != wantScaledOut {
			t.Fatalf("%s: user_tunnel out_flow must be scaled %d, got %d", label, wantScaledOut, got)
		}
	}
	resetLanded := func() {
		t.Helper()
		if err := r.DB().Exec(`UPDATE forward SET in_flow = 0, out_flow = 0 WHERE id = ?`, forward.ID).Error; err != nil {
			t.Fatalf("reset forward flows: %v", err)
		}
		if err := r.DB().Exec(`UPDATE user SET in_flow = 0, out_flow = 0 WHERE id = ?`, meta.UserID).Error; err != nil {
			t.Fatalf("reset user flows: %v", err)
		}
		if err := r.DB().Exec(`UPDATE user_tunnel SET in_flow = 0, out_flow = 0 WHERE id = ?`, meta.UserTunnelID).Error; err != nil {
			t.Fatalf("reset user_tunnel flows: %v", err)
		}
	}
	applyBatch := func(label string, batch flowUploadBatch) {
		t.Helper()
		if err := r.ApplyFlowUploadDeltasBatch(batch.flowDeltas); err != nil {
			t.Fatalf("%s: apply flow deltas: %v", label, err)
		}
	}

	// gost 路径：线格式 U=出 110、D=入 80。
	svcName := fmt.Sprintf("%d_%d_%d", forward.ID, meta.UserID, meta.UserTunnelID)
	gostBatch := h.buildFlowUploadBatch([]flowItem{{N: svcName, U: 110, D: 80}}, metas)
	if got := gostBatch.quotaUsage[meta.UserID]; got != 0 {
		t.Fatalf("gost quotaUsage with ratio=0 must be 0, got %d", got)
	}
	if len(gostBatch.flowDeltas) != 1 || gostBatch.flowDeltas[0].InFlow != 0 || gostBatch.flowDeltas[0].OutFlow != 0 {
		t.Fatalf("gost scaled deltas with ratio=0 must be 0/0, got %#v", gostBatch.flowDeltas)
	}
	if tr := gostBatch.forwardTraffic[forward.ID]; tr.bytesIn != 80 || tr.bytesOut != 110 {
		t.Fatalf("gost raw traffic must be preserved, got %#v", tr)
	}
	// R5X-013 ratio=0 落账：forward 行增长 raw 80/110、配额面（user/UT 行）0。
	applyBatch("gost ratio=0", gostBatch)
	assertLanded("gost ratio=0", 80, 110, 0, 0)
	resetLanded()

	// nft/realm 路径（同一 metas 来源）+ O6-005 警告复活。
	var logBuf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logBuf)
	nftBatch := buildNftFlowUploadBatch([]nftTrafficDelta{{ForwardID: forward.ID, BytesIn: 80, BytesOut: 110}}, metas)
	log.SetOutput(oldOut)
	_ = oldOut
	if got := nftBatch.quotaUsage[meta.UserID]; got != 0 {
		t.Fatalf("nft quotaUsage with ratio=0 must be 0, got %d", got)
	}
	if len(nftBatch.flowDeltas) != 1 || nftBatch.flowDeltas[0].InFlow != 0 || nftBatch.flowDeltas[0].OutFlow != 0 {
		t.Fatalf("nft scaled deltas with ratio=0 must be 0/0, got %#v", nftBatch.flowDeltas)
	}
	if tr := nftBatch.forwardTraffic[forward.ID]; tr.bytesIn != 80 || tr.bytesOut != 110 {
		t.Fatalf("nft raw traffic must be preserved, got %#v", tr)
	}
	if !strings.Contains(logBuf.String(), "nftables zero billing") {
		t.Fatalf("O6-005: zero-ratio nft batch must emit zero billing warn, log=%q", logBuf.String())
	}
	// R5X-013 ratio=0 落账（nft/realm 路径同口径）。
	applyBatch("nft ratio=0", nftBatch)
	assertLanded("nft ratio=0", 80, 110, 0, 0)
	resetLanded()

	// ratio=2 对照：两路径按 2 倍入账不变。
	setRatio(2)
	metas2, err := r.GetFlowUploadForwardMetas([]int64{forward.ID})
	if err != nil {
		t.Fatalf("get metas ratio=2: %v", err)
	}
	gost2 := h.buildFlowUploadBatch([]flowItem{{N: svcName, U: 110, D: 80}}, metas2)
	if got := gost2.quotaUsage[meta.UserID]; got != 380 {
		t.Fatalf("gost quotaUsage with ratio=2 must be 380, got %d", got)
	}
	nft2 := buildNftFlowUploadBatch([]nftTrafficDelta{{ForwardID: forward.ID, BytesIn: 80, BytesOut: 110}}, metas2)
	if got := nft2.quotaUsage[meta.UserID]; got != 380 {
		t.Fatalf("nft quotaUsage with ratio=2 must be 380, got %d", got)
	}
	if len(gost2.flowDeltas) != 1 || !gost2.flowDeltas[0].HasRaw || gost2.flowDeltas[0].RawInFlow != 80 || gost2.flowDeltas[0].RawOutFlow != 110 {
		t.Fatalf("gost ratio=2 delta raw group must be 80/110 with HasRaw, got %#v", gost2.flowDeltas)
	}
	if len(nft2.flowDeltas) != 1 || !nft2.flowDeltas[0].HasRaw || nft2.flowDeltas[0].RawInFlow != 80 || nft2.flowDeltas[0].RawOutFlow != 110 {
		t.Fatalf("nft ratio=2 delta raw group must be 80/110 with HasRaw, got %#v", nft2.flowDeltas)
	}
	// R5X-013 ratio=2/flow=2 落账：forward 行 80/110（raw）、
	// user/UT 行 160/220（scaled）、配额增量 380（上文 quotaUsage 已断言）。
	applyBatch("gost ratio=2", gost2)
	assertLanded("gost ratio=2", 80, 110, 160, 220)
	resetLanded()
	applyBatch("nft ratio=2", nft2)
	assertLanded("nft ratio=2", 80, 110, 160, 220)
	resetLanded()

	// R5X-013 ratio=1 对照：raw 与 scaled 同值，三面（forward/user/UT 行）不变口径、均为 80/110。
	setRatio(1)
	metas1, err := r.GetFlowUploadForwardMetas([]int64{forward.ID})
	if err != nil {
		t.Fatalf("get metas ratio=1: %v", err)
	}
	gost1 := h.buildFlowUploadBatch([]flowItem{{N: svcName, U: 110, D: 80}}, metas1)
	if got := gost1.quotaUsage[meta.UserID]; got != 190 {
		t.Fatalf("gost quotaUsage with ratio=1 must be 190, got %d", got)
	}
	applyBatch("gost ratio=1", gost1)
	assertLanded("gost ratio=1", 80, 110, 80, 110)
	resetLanded()
	nft1 := buildNftFlowUploadBatch([]nftTrafficDelta{{ForwardID: forward.ID, BytesIn: 80, BytesOut: 110}}, metas1)
	if got := nft1.quotaUsage[meta.UserID]; got != 190 {
		t.Fatalf("nft quotaUsage with ratio=1 must be 190, got %d", got)
	}
	applyBatch("nft ratio=1", nft1)
	assertLanded("nft ratio=1", 80, 110, 80, 110)
	resetLanded()
}

// R3X-019 (R3-F-02): nftables 同步路径须按 realm 同谓词（转发级
// SpeedID/IPSpeedID 经 status=1 且 speed>0 判定、MaxConn/IPMaxConn
// 裸值、UT 级限速同谓词）生成「nftables 模式不支持限速和连接数
// 限制」警告并入 syncForwardServicesWithWarnings 的既有回执通道；
// 禁用/零速不误报、无限速无新警告。
func TestR3B3NftSyncWarnsOnEffectiveLimitsOnly(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "r3b3-nft-warn-tunnel", fixture.nodeID)

	mkLimit := func(name string, speed, status int) int64 {
		t.Helper()
		sl := &model.SpeedLimit{Name: name, Speed: speed, CreatedTime: time.Now().UnixMilli(), Status: status}
		if err := r.DB().Create(sl).Error; err != nil {
			t.Fatalf("seed speed_limit %s: %v", name, err)
		}
		return sl.ID
	}
	disabledID := mkLimit("r3b3-disabled", 100, 0)
	zeroID := mkLimit("r3b3-zero", 0, 1)
	validID := mkLimit("r3b3-valid", 100, 1)

	hasNftWarning := func(warnings []string) bool {
		for _, w := range warnings {
			if strings.Contains(w, "nftables 模式不支持限速和连接数限制") {
				return true
			}
		}
		return false
	}
	syncWarn := func(forward *forwardRecord) bool {
		t.Helper()
		warnings, err := h.syncForwardServicesWithWarnings(forward, "UpdateService", true)
		if err != nil {
			t.Fatalf("syncForwardServicesWithWarnings: %v", err)
		}
		return hasNftWarning(warnings)
	}
	setSpeedIDs := func(forwardID int64, speedID, ipSpeedID any) *forwardRecord {
		t.Helper()
		if err := r.DB().Exec(`UPDATE forward SET speed_id = ?, ip_speed_id = ? WHERE id = ?`, speedID, ipSpeedID, forwardID).Error; err != nil {
			t.Fatal(err)
		}
		fwd, err := h.getForwardRecord(forwardID)
		if err != nil || fwd == nil {
			t.Fatalf("reload forward %d: %v", forwardID, err)
		}
		return fwd
	}

	// 无任何限速：无新警告。
	fNone := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20101)
	if syncWarn(fNone) {
		t.Fatalf("forward without limits must not produce nft limit warning")
	}
	// 有效转发级限速：警告。
	fValid := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20102)
	if !syncWarn(setSpeedIDs(fValid.ID, validID, nil)) {
		t.Fatalf("valid forward speed limit must produce nft limit warning")
	}
	// 禁用/零速：不误报。
	fDisabled := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20103)
	if syncWarn(setSpeedIDs(fDisabled.ID, disabledID, zeroID)) {
		t.Fatalf("disabled/zero forward speed limits must not produce nft limit warning")
	}
	// 仅连接数（转发自有字段）：警告。
	fConn := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20104)
	fConn.MaxConn = 3
	if !syncWarn(fConn) {
		t.Fatalf("MaxConn-only forward must produce nft limit warning")
	}
	// UT 级有效限速：警告；禁用后不误报。
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, ?, 99999, 99999, 0, 0, 1, 2727251700000, 1)
	`, tunnelID, validID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	fUT := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "1.1.1.1:443", 20105)
	if !syncWarn(fUT) {
		t.Fatalf("UT-level limiter must produce nft limit warning")
	}
	if err := r.DB().Exec(`UPDATE speed_limit SET status = 0 WHERE id = ?`, validID).Error; err != nil {
		t.Fatal(err)
	}
	if syncWarn(fUT) {
		t.Fatalf("disabled UT-level limiter must not produce nft limit warning")
	}
}
