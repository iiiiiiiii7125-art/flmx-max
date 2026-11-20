package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// R18-C5-02 回归（摄入侧）：agent 时钟偏移可绕过保留期剪枝，并把
// 合法日志行挤出 5 万行帽。修复：入库前拒收 StartTime/EndTime 超
// 出面板时间 +1 小时（未来方向）的事件，整批 invalid_batch。
// 只拒未来方向：spool/断线重放会产生旧事件（过去方向无害——
// 时间剪枝与行数帽改按面板 created_time 后，旧事件只会被正常
// 清理，不会挤占合法行），故过去时间一律放行。
func TestConnlogIngestRejectsFutureStartTime(t *testing.T) {
	rig := newConnlogTestRig(t)
	now := time.Now().UnixMilli()
	day := int64(24 * 3600 * 1000)
	future := fmt.Sprintf(`[
		{"type":"open","connId":"f1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","clientPort":51000,"network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, now+30*day)
	rec := rig.postBatch(t, rig.entrySec, 1, future)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_batch") {
		t.Fatalf("future startTime batch: status = %d body = %s, want 400 invalid_batch", rec.Code, rec.Body.String())
	}
	if rows, _ := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10); len(rows) != 0 {
		t.Fatalf("future startTime rows = %d, want 0 (nothing ingested)", len(rows))
	}
	// 拒收不推进游标：同序号合法批次仍可入账。
	valid := fmt.Sprintf(`[
		{"type":"open","connId":"v1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","clientPort":51000,"network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, now)
	rec = rig.postBatch(t, rig.entrySec, 1, valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid batch after rejection: status = %d body = %s, want 200 (cursor not advanced)", rec.Code, rec.Body.String())
	}
}

func TestConnlogIngestRejectsFutureEndTime(t *testing.T) {
	rig := newConnlogTestRig(t)
	now := time.Now().UnixMilli()
	day := int64(24 * 3600 * 1000)
	futureEnd := fmt.Sprintf(`[
		{"type":"close","connId":"f2","entryKind":"forward","entryId":"77_2_3_tcp","endTime":%d}
	]`, now+30*day)
	rec := rig.postBatch(t, rig.entrySec, 1, futureEnd)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_batch") {
		t.Fatalf("future endTime batch: status = %d body = %s, want 400 invalid_batch", rec.Code, rec.Body.String())
	}
	if rows, _ := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10); len(rows) != 0 {
		t.Fatalf("future endTime rows = %d, want 0", len(rows))
	}
}

// 过去方向放行：spool/断线重放的旧事件必须能入账（只拒未来）。
func TestConnlogIngestAcceptsOldStartTime(t *testing.T) {
	rig := newConnlogTestRig(t)
	now := time.Now().UnixMilli()
	day := int64(24 * 3600 * 1000)
	old := fmt.Sprintf(`[
		{"type":"open","connId":"o1","entryKind":"forward","entryId":"77_2_3_tcp","clientIp":"203.0.113.7","clientPort":51000,"network":"tcp","sessionKind":"tcp_conn","startTime":%d}
	]`, now-30*day)
	rec := rig.postBatch(t, rig.entrySec, 1, old)
	if rec.Code != http.StatusOK {
		t.Fatalf("30-day-old startTime batch: status = %d body = %s, want 200 (past must be accepted)", rec.Code, rec.Body.String())
	}
	rows, _ := rig.r.ListNodeConnLogs(rig.entryID, "", 0, 10)
	if len(rows) != 1 || rows[0].ConnID != "o1" {
		t.Fatalf("old startTime rows = %v, want [o1]", rows)
	}
}
