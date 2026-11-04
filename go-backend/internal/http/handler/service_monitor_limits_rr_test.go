package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/monitoring"
)

// RR-F5-01：service_monitor_* 7 键保存面接线——经
// normalizeAndValidateConfigValue 逐键验：界内值归一通过、超界值
// 点名拒绝、空串走可清空语义（本族非必填键）。

var rrServiceMonitorLimitKeys = []string{
	monitoring.ConfigServiceMonitorCheckerScanIntervalSec,
	monitoring.ConfigServiceMonitorWorkerLimit,
	monitoring.ConfigServiceMonitorMinIntervalSec,
	monitoring.ConfigServiceMonitorDefaultIntervalSec,
	monitoring.ConfigServiceMonitorMinTimeoutSec,
	monitoring.ConfigServiceMonitorDefaultTimeoutSec,
	monitoring.ConfigServiceMonitorMaxTimeoutSec,
}

func TestNormalizeServiceMonitorLimitKeysSaveFace(t *testing.T) {
	for _, key := range rrServiceMonitorLimitKeys {
		if got, err := normalizeAndValidateConfigValue(key, "5"); err != nil || got != "5" {
			t.Fatalf("%s value 5: got (%q, %v), want (\"5\", nil)", key, got, err)
		}
		if got, err := normalizeAndValidateConfigValue(key, ""); err != nil || got != "" {
			t.Fatalf("%s empty: got (%q, %v), want clear semantics (\"\", nil)", key, got, err)
		}
		for _, bad := range []string{"0", "-3", "abc", "2.5"} {
			if got, err := normalizeAndValidateConfigValue(key, bad); err == nil {
				t.Fatalf("%s value %q: got (%q, nil), want save-time rejection", key, bad, got)
			}
		}
	}
	// 本条的点名缺陷：max_timeout 超过文档 60 秒上限必须保存期拒绝。
	if _, err := normalizeAndValidateConfigValue(monitoring.ConfigServiceMonitorMaxTimeoutSec, "61"); err == nil ||
		!strings.Contains(err.Error(), "60") {
		t.Fatalf("max_timeout 61: err = %v, want rejection naming the 60 cap", err)
	}
	if _, err := normalizeAndValidateConfigValue(monitoring.ConfigServiceMonitorMaxTimeoutSec, "120"); err == nil {
		t.Fatal("max_timeout 120 must be rejected at save time")
	}
	// 三个 timeout 键同守 60（读面排序抬升决定三键必须同界）。
	for _, key := range []string{
		monitoring.ConfigServiceMonitorMinTimeoutSec,
		monitoring.ConfigServiceMonitorDefaultTimeoutSec,
	} {
		if _, err := normalizeAndValidateConfigValue(key, "61"); err == nil {
			t.Fatalf("%s value 61: want rejection (timeout family shares the 60 cap)", key)
		}
	}
	// worker_limit 与读面硬钳同界 50。
	if _, err := normalizeAndValidateConfigValue(monitoring.ConfigServiceMonitorWorkerLimit, "51"); err == nil {
		t.Fatal("worker_limit 51 must be rejected at save time (read side clamps at 50)")
	}
	if got, err := normalizeAndValidateConfigValue(monitoring.ConfigServiceMonitorWorkerLimit, "50"); err != nil || got != "50" {
		t.Fatalf("worker_limit 50: got (%q, %v), want pass", got, err)
	}
}

// RR-F5-01 端到端：update-single 写面——超界写入被拒且不落库，
// 界内值归一落库，清空（传空）删除该键回落默认。
func TestUpdateSingleConfigServiceMonitorMaxTimeout(t *testing.T) {
	h, r := newPanelsecHandler(t)
	key := monitoring.ConfigServiceMonitorMaxTimeoutSec

	call := func(value string) response.R {
		payload, _ := json.Marshal(map[string]string{"name": key, "value": value})
		req := adminRequest(t, http.MethodPost, "/api/v1/config/update-single", payload)
		rec := httptest.NewRecorder()
		h.updateSingleConfig(rec, req)
		var out response.R
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return out
	}
	stored := func() (string, bool) {
		cfg, err := r.GetConfigByName(key)
		if err != nil || cfg == nil {
			return "", false
		}
		return cfg.Value, true
	}

	if out := call("120"); out.Code == 0 {
		t.Fatalf("max_timeout 120 must be rejected at save time, got code=%d", out.Code)
	}
	if _, ok := stored(); ok {
		t.Fatal("rejected value 120 must not be stored")
	}
	if out := call("45"); out.Code != 0 {
		t.Fatalf("max_timeout 45 must save, got code=%d msg=%q", out.Code, out.Msg)
	}
	if v, ok := stored(); !ok || v != "45" {
		t.Fatalf("stored value = (%q, %v), want (\"45\", true)", v, ok)
	}
	if out := call(""); out.Code != 0 {
		t.Fatalf("clearing max_timeout must stay allowed (clearable key), got code=%d msg=%q", out.Code, out.Msg)
	}
	if _, ok := stored(); ok {
		t.Fatal("cleared key must be deleted so reads fall back to the default")
	}
	// 清空后读面回落默认 60，文档口径恢复成立。
	if limits := h.resolveServiceMonitorLimits(); limits.MaxTimeoutSec != 60 {
		t.Fatalf("after clear, resolved MaxTimeoutSec = %d, want default 60", limits.MaxTimeoutSec)
	}
}
