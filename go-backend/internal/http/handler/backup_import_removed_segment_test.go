package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 已移除功能段的旧备份导入容忍：备份文件携带本版已不认识的顶层段
// 时，导入成功、该段被丢弃并在回执 ImportWarnings 中按名披露，
// 不整单失败。机制是通用的（按顶层键名判定，不认具体段名），
// 此处用任意未知段名覆盖同一路径；嵌套未知字段仍严格拒绝。
func TestBackupImportUnknownTopSegmentIgnoredWithWarning(t *testing.T) {
	h := r8NewHandler(t)

	body := `{"version":"1.0","exportedAt":0,"types":["configs"],"configs":{},"bogusSegment":[{"id":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	var out struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImportWarnings []string `json:"importWarnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("import with unknown top segment: code=%d msg=%q, want 0", out.Code, out.Msg)
	}
	found := false
	for _, w := range out.Data.ImportWarnings {
		if strings.Contains(w, "bogusSegment") && strings.Contains(w, "忽略") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped segment must be disclosed in ImportWarnings, got %v", out.Data.ImportWarnings)
	}
}

func TestBackupImportNestedUnknownFieldStillRejected(t *testing.T) {
	h := r8NewHandler(t)

	// users 段内嵌未知字段：仍按 R1-07-F4 口径拒绝并点名。
	body := `{"version":"1.0","exportedAt":0,"types":["users"],"users":[{"user":"u1","bogusField":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	code, msg := cb14Envelope(t, rec)
	if code != -1 || !strings.Contains(msg, "bogusField") {
		t.Fatalf("nested unknown field: code=%d msg=%q, want -1 naming bogusField", code, msg)
	}
}

// RM-ANN v1：含 announcements 段的旧备份在删表迁移后仍须容忍导入——
// code=0、警告按名披露 "announcements"，不整单失败。删表迁移只动
// 库表，不碰导入路径；本用例锁定该兼容不被迁移破坏。
func TestBackupImportLegacyAnnouncementsSegmentIgnoredWithWarning(t *testing.T) {
	h := r8NewHandler(t)

	body := `{"version":"1.0","exportedAt":0,"types":["configs"],"configs":{},"announcements":[{"id":1,"content":"","enabled":0,"created_time":1,"updated_time":1}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	var out struct {
		Code int `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImportWarnings []string `json:"importWarnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("import with legacy announcements segment: code=%d msg=%q, want 0", out.Code, out.Msg)
	}
	found := false
	for _, w := range out.Data.ImportWarnings {
		if strings.Contains(w, "announcements") && strings.Contains(w, "忽略") {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropped announcements segment must be disclosed in ImportWarnings, got %v", out.Data.ImportWarnings)
	}
}
