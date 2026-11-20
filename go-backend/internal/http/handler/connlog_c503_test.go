package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R18-C5-03 回归：开关关闭只在标记下发侧执法，摄入侧无门——
// 离线/重推丢失窗口内旧标记 agent 继续上报，面板照单全收。
// 修复：flowConnlogV2 在鉴权后执法总开关 AND 节点开关；开关关
// 时整批拒收并回专用错误码 collection_disabled（409，与
// invalid_batch 区分，避免 agent 二分误判为批次非法）；开关
// 查询失败 fail-closed 拒收 503。
func TestConnlogIngestRejectedWhenSwitchOff(t *testing.T) {
	rig := newConnlogTestRig(t)
	now := time.Now().UnixMilli()
	open := func(connID string, start int64) string {
		return fmt.Sprintf(`[
			{"type":"open","connId":%q,"entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","clientPort":51000,"network":"tcp","sessionKind":"tcp_conn","startTime":%d}
		]`, connID, start)
	}

	// 基线：开关开时正常入账。
	if rec := rig.postBatch(t, rig.entrySec, 1, open("on-1", now)); rec.Code != http.StatusOK {
		t.Fatalf("switch-on ingest: status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}

	// 节点开关关：整批拒收 409 collection_disabled，不落行、不推进游标。
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 0); err != nil {
		t.Fatalf("disable node switch: %v", err)
	}
	rec := rig.postBatch(t, rig.entrySec, 2, open("off-1", now))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "collection_disabled") {
		t.Fatalf("switch-off ingest: status = %d body = %s, want 409 collection_disabled", rec.Code, rec.Body.String())
	}
	if rows, _ := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10); len(rows) != 1 {
		t.Fatalf("switch-off rows = %d, want 1 (only the switch-on row)", len(rows))
	}
	// 他节点开关仍开，不受影响。
	if rec := rig.postBatch(t, rig.landingSec, 1, open("other-1", now)); rec.Code != http.StatusOK {
		t.Fatalf("other node ingest while entry disabled: status = %d, want 200", rec.Code)
	}
	// 重开开关：同序号批次可入账（游标未推进）。
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 1); err != nil {
		t.Fatalf("re-enable node switch: %v", err)
	}
	if rec := rig.postBatch(t, rig.entrySec, 2, open("off-1", now)); rec.Code != http.StatusOK {
		t.Fatalf("re-enabled ingest: status = %d body = %s, want 200 (cursor not advanced by rejection)", rec.Code, rec.Body.String())
	}

	// 总开关关：同样拒收；恢复后可入账。
	if err := rig.r.UpsertConfig(repo.ConnLogEnabledConfigKey, "false", time.Now().UnixMilli()); err != nil {
		t.Fatalf("disable master switch: %v", err)
	}
	rec = rig.postBatch(t, rig.entrySec, 3, open("off-2", now))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "collection_disabled") {
		t.Fatalf("master-off ingest: status = %d body = %s, want 409 collection_disabled", rec.Code, rec.Body.String())
	}
	if err := rig.r.DeleteConfig(repo.ConnLogEnabledConfigKey); err != nil {
		t.Fatalf("restore master switch: %v", err)
	}
	if rec := rig.postBatch(t, rig.entrySec, 3, open("off-2", now)); rec.Code != http.StatusOK {
		t.Fatalf("master-restored ingest: status = %d, want 200", rec.Code)
	}
}

// R18-C5-03 回归（探测面）：readiness 在开关关且最近 1 小时内
// 仍有该节点新行入库时，必须点名“agent 仍持旧配置上报”，而不
// 是静默显示 off。
func TestConnlogReadinessDetectsStaleReportingAfterSwitchOff(t *testing.T) {
	rig := newConnlogTestRig(t)
	admin := &auth.Claims{Sub: "1", RoleID: 0}
	now := time.Now().UnixMilli()

	// 开关开时上报一行（最近时刻）。
	events := fmt.Sprintf(`[{"type":"open","connId":"s1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","network":"tcp","sessionKind":"tcp_conn","startTime":%d}]`, now-1000)
	if rec := rig.postBatch(t, rig.entrySec, 1, events); rec.Code != http.StatusOK {
		t.Fatalf("seed ingest status = %d, body %s", rec.Code, rec.Body.String())
	}
	// 开关关：最近 1 小时内有行到达 → reason 须点名旧配置上报。
	if err := rig.r.UpdateNodeConnLogEnabled(rig.entryID, 0); err != nil {
		t.Fatalf("disable node switch: %v", err)
	}
	if code, data := rig.callReadiness(t, admin, rig.entryID); code != 0 || data.Readiness != "off" {
		t.Fatalf("switch-off readiness = (%d, %+v), want 0 / off", code, data)
	} else if !strings.Contains(data.Reason, "仍持旧配置") {
		t.Fatalf("stale-report reason = %q, want it to name the stale agent config", data.Reason)
	}
	// 行全部变旧（2 小时前摄入）：不再报旧配置上报，只报开关未开。
	if err := rig.r.DB().Where("entry_node_id = ?", rig.entryID).Delete(&model.InboundConnLog{}).Error; err != nil {
		t.Fatalf("clear rows: %v", err)
	}
	old := &model.InboundConnLog{EntryNodeID: rig.entryID, ConnID: "s0", EntryKind: repo.ConnlogEntryForward, EntryID: "77_2_3_tcp", ClientIP: "203.0.113.7", Network: "tcp", SessionKind: "tcp_conn", StartTime: now - 2*60*60*1000, Status: repo.ConnlogStatusOpen, CreatedTime: now - 2*60*60*1000}
	if err := rig.r.DB().Create(old).Error; err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	if code, data := rig.callReadiness(t, admin, rig.entryID); code != 0 || data.Readiness != "off" {
		t.Fatalf("old-rows readiness = (%d, %+v), want 0 / off", code, data)
	} else if strings.Contains(data.Reason, "仍持旧配置") {
		t.Fatalf("old-rows reason = %q, must not claim stale reporting", data.Reason)
	} else if !strings.Contains(data.Reason, "开关未开启") {
		t.Fatalf("old-rows reason = %q, want plain switch-off reason", data.Reason)
	}
}
