package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FINAL-P2-1（P2，终局候选版审计立案）：备份导入重推谓词漏限速段。
// backupImportNeedsForwardRepush 只认五段，SpeedLimitsImported 与
// UserTunnelsImported 不在其中；单独导入限速段时 DB 已变、节点
// limiter 仍为旧值且无其他尾钩子补推，窗口无限期。先红后绿：
// 本文件先于谓词修复落盘，谓词两段补齐后方能全绿。

func finalP21PostImport(t *testing.T, h *Handler, types []string, data *model.BackupData) string {
	t.Helper()
	body := struct {
		Types []string `json:"types"`
		*model.BackupData
	}{Types: types, BackupData: data}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport types=%v: code=%d err=%v body=%s", types, env.Code, err, rec.Body.String())
	}
	return rec.Body.String()
}

func finalP21Receipt(t *testing.T, body string) model.ImportResult {
	t.Helper()
	var env struct {
		Data model.ImportResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode import receipt: %v", err)
	}
	return env.Data
}

// 触发谓词：限速两段各自单独触发；纯内核/纯订阅不触发；既有段锚点不动。
func TestFinalP21RepushTrigger(t *testing.T) {
	cases := []struct {
		name   string
		result *model.ImportResult
		want   bool
	}{
		{"nil", nil, false},
		{"speedLimits-only", &model.ImportResult{SpeedLimitsImported: 1}, true},
		{"userTunnels-only", &model.ImportResult{UserTunnelsImported: 1}, true},
		{"forwards-anchor", &model.ImportResult{ForwardsImported: 1}, true},
		{"kernels-only", &model.ImportResult{InboundKernelsImported: 2}, false},
		{"subscriptions-only", &model.ImportResult{InboundSubscriptionsImported: 2}, false},
	}
	for _, c := range cases {
		if got := backupImportNeedsForwardRepush(c.result); got != c.want {
			t.Errorf("FINAL-P2-1 trigger %s = %v, want %v", c.name, got, c.want)
		}
	}
}

// 限速段单独导入（改值）：在线节点必须收到新限速载荷（1 Mbps 线），
// 回执重推清单须含该节点、失败清单为空。
func TestFinalP21SpeedLimitsOnlyImportRepushesLimiter(t *testing.T) {
	s := r6x017Setup(t, 1, 0) // 转发直引规则 R=10 Mbps
	baseline := s.baseline()
	data, err := s.repo.ExportPartial([]string{"speedLimits"})
	if err != nil {
		t.Fatalf("export speedLimits: %v", err)
	}
	mutated := false
	for i := range data.SpeedLimits {
		if data.SpeedLimits[i].ID == s.ruleID {
			data.SpeedLimits[i].Speed = 1
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("setup: rule missing from speedLimits export")
	}
	body := finalP21PostImport(t, s.h, []string{"speedLimits"}, data)
	receipt := finalP21Receipt(t, body)
	if receipt.SpeedLimitsImported != len(data.SpeedLimits) {
		t.Fatalf("speedLimits segment must report %d imported rows, got %d body=%s", len(data.SpeedLimits), receipt.SpeedLimitsImported, body)
	}
	found := false
	for _, id := range receipt.RepushNodeIDs {
		if id == s.nodeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("FINAL-P2-1: receipt repushNodeIds must include online node %d, got %v", s.nodeID, receipt.RepushNodeIDs)
	}
	if len(receipt.RepushFailedNodeIDs) != 0 {
		t.Fatalf("FINAL-P2-1: receipt repushFailedNodeIds must be empty, got %v", receipt.RepushFailedNodeIDs)
	}
	want := []string{speedToLimitLine("$", 1)}
	waitForCondition(t, 10*time.Second, func() bool {
		limits, ok := r6x017LastLimiterLimits(s.cmdsAfter(baseline), fmt.Sprint(s.ruleID))
		return ok && fmt.Sprint(limits) == fmt.Sprint(want)
	}, "limiter re-dispatch with new speed 1 Mbps")
}

// 用户隧道段单独导入（改 UT 限速引用 R→F）：在线节点转发服务
// limiter 须回退为 F，且回执重推清单含该节点。
func TestFinalP21UserTunnelsOnlyImportRepushesLimiter(t *testing.T) {
	s := r6x017Setup(t, 0, 1) // 转发无直引、UT 引规则 R=10 Mbps
	if got, found := s.lastServiceLimiter(s.rc.snapshot()); !found || got != fmt.Sprint(s.ruleID) {
		t.Fatalf("setup: initial service limiter must be UT rule %d, got %q found=%v", s.ruleID, got, found)
	}
	baseline := s.baseline()
	data, err := s.repo.ExportPartial([]string{"userTunnels"})
	if err != nil {
		t.Fatalf("export userTunnels: %v", err)
	}
	mutated := false
	for i := range data.UserTunnels {
		if data.UserTunnels[i].UserID == 1 && data.UserTunnels[i].TunnelID == s.tunnelID {
			data.UserTunnels[i].SpeedID = s.fallbackID
			mutated = true
		}
	}
	if !mutated {
		t.Fatal("setup: user tunnel missing from userTunnels export")
	}
	body := finalP21PostImport(t, s.h, []string{"userTunnels"}, data)
	if !strings.Contains(body, `"userTunnelsImported":1`) {
		t.Fatalf("userTunnels segment must report 1 imported row, body=%s", body)
	}
	receipt := finalP21Receipt(t, body)
	found := false
	for _, id := range receipt.RepushNodeIDs {
		if id == s.nodeID {
			found = true
		}
	}
	if !found {
		t.Fatalf("FINAL-P2-1: receipt repushNodeIds must include online node %d, got %v", s.nodeID, receipt.RepushNodeIDs)
	}
	waitForCondition(t, 10*time.Second, func() bool {
		got, ok := s.lastServiceLimiter(s.cmdsAfter(baseline))
		return ok && got == fmt.Sprint(s.fallbackID)
	}, "service limiter fallback to UT rule after userTunnels import")
}

// 对照：纯订阅/纯内核导入在在线节点上也不触发重推（门级断言，
// 谓词表之上再钉一层 outcome：attempted 与 failed 均为空）。
func TestFinalP21SubscriptionOrKernelOnlyImportDoesNotRepush(t *testing.T) {
	s := r6x017Setup(t, 1, 0)
	for _, tc := range []struct {
		name   string
		result *model.ImportResult
	}{
		{"subscriptions-only", &model.ImportResult{InboundSubscriptionsImported: 2}},
		{"kernels-only", &model.ImportResult{InboundKernelsImported: 2}},
	} {
		out := s.h.repushForwardRuntimeAfterImport(tc.result, &model.BackupData{Version: "1.0"})
		if len(out.attempted) != 0 || len(out.failed) != 0 {
			t.Errorf("FINAL-P2-1 control %s: repush must attempt nothing, got attempted=%v failed=%v", tc.name, out.attempted, out.failed)
		}
	}
}
