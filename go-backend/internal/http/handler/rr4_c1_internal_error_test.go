package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RR4-C1-01 钉死用例：普通用户可达面在仓储错误时只回通用文案
// （code -2 + "Internal error"），批量回执的逐条失败原因同样净化
// （"内部错误，请稍后重试"）；业务文案（转发不存在等）逐字不变。
// 故障注入沿用 DROP TABLE 口径（原文含 "no such table"）。

func TestRR4C1AnnouncementGetRepoErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "announcement")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/announcement/get", nil)
	rec := httptest.NewRecorder()
	h.getAnnouncement(rec, req)
	assertGenericInternalError(t, rec)
}

func TestRR4C1GetConfigsRepoErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "vite_config")
	req := newAuthenticatedJSONRequestAs(t, 2, "user2", 1, map[string]any{})
	rec := httptest.NewRecorder()
	h.getConfigs(rec, req)
	assertGenericInternalError(t, rec)
}

func TestRR4C1ForwardListRepoErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "forward")
	req := newAuthenticatedJSONRequestAs(t, 2, "user2", 1, map[string]any{})
	rec := httptest.NewRecorder()
	h.forwardList(rec, req)
	assertGenericInternalError(t, rec)
}

func TestRR4C1ForwardPauseRepoErrorIsGeneric(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "forward")
	req := newAuthenticatedJSONRequestAs(t, 2, "user2", 1, map[string]any{"id": 1})
	rec := httptest.NewRecorder()
	h.forwardPause(rec, req)
	assertGenericInternalError(t, rec)
}

// 业务文案保持钉：转发不存在仍回原文案，不被内部错误收口误伤。
func TestRR4C1ForwardPauseMissingKeepsBusinessCopy(t *testing.T) {
	h, _ := newPanelsecHandler(t)
	req := newAuthenticatedJSONRequestAs(t, 2, "user2", 1, map[string]any{"id": 424242})
	rec := httptest.NewRecorder()
	h.forwardPause(rec, req)
	code, msg := decodePendingEnvelopeCode(t, rec)
	if code != -1 || msg != "转发不存在" {
		t.Fatalf("expected business copy (-1, 转发不存在), got (%d, %q) body=%s", code, msg, rec.Body.String())
	}
}

// 批量回执通道钉：逐条失败原因中的仓储原文必须被净化为通用文案。
func TestRR4C1ForwardBatchPauseReceiptSanitizesRepoError(t *testing.T) {
	h, r := newPanelsecHandler(t)
	dropTableForTest(t, r, "forward")
	req := newAuthenticatedJSONRequestAs(t, 2, "user2", 1, map[string]any{"ids": []int64{1}})
	rec := httptest.NewRecorder()
	h.forwardBatchPause(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "no such table") || strings.Contains(body, "sql:") {
		t.Fatalf("batch receipt leaks raw storage error: %s", body)
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			FailCount int `json:"failCount"`
			Failures  []struct {
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if out.Code != 0 || out.Data.FailCount != 1 || len(out.Data.Failures) != 1 {
		t.Fatalf("expected one sanitized failure, got body=%s", body)
	}
	if out.Data.Failures[0].Reason != "内部错误，请稍后重试" {
		t.Fatalf("expected generic batch reason, got %q", out.Data.Failures[0].Reason)
	}
}
