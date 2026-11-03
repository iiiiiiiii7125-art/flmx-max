package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// R9-14 钉格：backupImport 的请求体解码失败与全局同文案口径一致——
// 「请求参数错误」回 ErrDefault（-1），与 decodeJSON 全部调用点同码
// （修复前此点为导入面内部口径 -2，是全树同文案唯一异码）。导入面
// 其余失败仍为 J-008 的 -2 内部口径，本钉同时锁住该边界不被误动。
func TestR9BackupImportMalformedJSONReturnsGlobalParamCode(t *testing.T) {
	h := r8NewHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader([]byte("{")))
	rec := httptest.NewRecorder()
	h.backupImport(rec, req)
	if code, msg := cb14Envelope(t, rec); code != -1 || msg != "请求参数错误" {
		t.Fatalf("malformed import body: code=%d msg=%q, want -1 请求参数错误", code, msg)
	}

	// 对照：合法 JSON 但未选导入类型，仍是导入面 -2 口径。
	req = httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader([]byte("{}")))
	rec = httptest.NewRecorder()
	h.backupImport(rec, req)
	if code, msg := cb14Envelope(t, rec); code != -2 || msg != "请选择要导入的数据类型" {
		t.Fatalf("empty types: code=%d msg=%q, want -2 请选择要导入的数据类型", code, msg)
	}
}
