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
func TestR3B3ZeroRatioWholeChainQuotaZeroRawPreserved(t *testing.T) {
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
