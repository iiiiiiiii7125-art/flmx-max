package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/response"
)

// R2-048（B-admin-05）：notify_daily_hour 写面补 0–23 值域校验，
// 与同族监控键（保留天数 1–400、探测间隔 1–3600、开关枚举）
// 「保存期拒绝」的口径对齐；越界值不再落库后靠读面静默回落。
// 边界集：-1 / 0 / 23 / 24。

func TestNormalizeNotifyDailyHourBoundaries(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"-1", "", "0 到 23"},
		{"0", "0", ""},
		{"23", "23", ""},
		{"24", "", "0 到 23"},
		{"", "", ""}, // 可清空键：空 = 清空回落默认，与读面同义
		{" 9 ", "9", ""},
		{"09", "9", ""},
		{"abc", "", "必须是整数"},
		{"9.5", "", "必须是整数"},
	}
	for _, tc := range cases {
		got, err := normalizeAndValidateConfigValue(notifyConfigDailyHour, tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("value %q: expected error containing %q, got (%q, %v)", tc.in, tc.wantErr, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("value %q: got (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}
}

func TestUpdateSingleConfigNotifyDailyHourRange(t *testing.T) {
	h, r := newPanelsecHandler(t)

	call := func(value string) response.R {
		payload, _ := json.Marshal(map[string]string{"name": notifyConfigDailyHour, "value": value})
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
		cfg, err := r.GetConfigByName(notifyConfigDailyHour)
		if err != nil || cfg == nil {
			return "", false
		}
		return cfg.Value, true
	}

	if out := call("24"); out.Code == 0 {
		t.Fatalf("hour 24 must be rejected at save time, got code=%d", out.Code)
	}
	if _, ok := stored(); ok {
		t.Fatal("rejected value 24 must not be stored")
	}
	if out := call("-1"); out.Code == 0 {
		t.Fatalf("hour -1 must be rejected at save time, got code=%d", out.Code)
	}
	if out := call("23"); out.Code != 0 {
		t.Fatalf("hour 23 must save, got code=%d msg=%q", out.Code, out.Msg)
	}
	if v, ok := stored(); !ok || v != "23" {
		t.Fatalf("stored value = (%q, %v), want (\"23\", true)", v, ok)
	}
	if out := call("0"); out.Code != 0 {
		t.Fatalf("hour 0 must save, got code=%d msg=%q", out.Code, out.Msg)
	}
	if v, ok := stored(); !ok || v != "0" {
		t.Fatalf("stored value = (%q, %v), want (\"0\", true)", v, ok)
	}
}
