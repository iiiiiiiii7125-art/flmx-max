package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// SEC-01 BC-01 (b)：导入前自动备份只留存服务端文件（最新一份、24h
// 过期），回执仍不得含 autoBackup。以下测试先于实现落盘（先红后绿）。
// 标记串均为合成测试值，绝非真实凭据。

func setupBC01Test(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	r, err := repo.Open(filepath.Join(dir, "bc01.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret"), dir
}

func bc01SnapshotPath(dir string) string {
	return filepath.Join(dir, importAutoBackupFileName)
}

func callBC01Import(t *testing.T, h *Handler, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.backupImport(rec, httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw)))
	return rec
}

func bc01ImportBody(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"version": "1.0",
		"types":   []string{"configs"},
		"configs": map[string]string{"imported_probe_key": "plain-value"},
	})
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	return raw
}

func nonAdminRequest(t *testing.T, method, path string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	return req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "2", User: "user@user", RoleID: 1}))
}

func decodeBC01Envelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string, json.RawMessage) {
	t.Helper()
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	return env.Code, env.Msg, env.Data
}

// (a)+(b)：导入成功后回执仍干净，且服务端留存了导入前快照；
// 经下载端点取回的字节与留存文件一致；快照是导入前状态（含旧凭据
// 标记、不含新导入的键），且文件权限为 0600。
func TestSEC01BC01ImportRetainsServerSideSnapshot(t *testing.T) {
	h, dir := setupBC01Test(t)
	r := gistTestRepo(t, h)
	seedSEC01Credentials(t, r)

	rec := callBC01Import(t, h, bc01ImportBody(t))
	code, _, _ := decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("backupImport code = %d body = %s", code, rec.Body.String())
	}
	assertSEC01ReceiptClean(t, rec.Body.String())

	path := bc01SnapshotPath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("snapshot file missing after import: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file perm = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap model.BackupData
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot is not valid BackupData JSON: %v", err)
	}
	if snap.Configs["sec01_probe_key"] != sec01MarkerToken {
		t.Fatalf("snapshot must hold pre-import state (sec01_probe_key marker), got %q", snap.Configs["sec01_probe_key"])
	}
	if _, ok := snap.Configs["imported_probe_key"]; ok {
		t.Fatal("snapshot must be pre-import state, must not contain the newly imported key")
	}

	// 状态端点：exists=true，expiresAt=文件 mtime+24h（unix 秒）。
	statusRec := httptest.NewRecorder()
	h.backupImportAutoBackupStatus(statusRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	code, _, statusData := decodeBC01Envelope(t, statusRec)
	if code != 0 {
		t.Fatalf("status code = %d body = %s", code, statusRec.Body.String())
	}
	var status struct {
		Exists    bool  `json:"exists"`
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode status data: %v body=%s", err, statusRec.Body.String())
	}
	if !status.Exists {
		t.Fatal("status.exists must be true after import")
	}
	if want := info.ModTime().Add(importAutoBackupTTL).Unix(); status.ExpiresAt != want {
		t.Fatalf("status.expiresAt = %d, want %d (mtime+24h)", status.ExpiresAt, want)
	}

	// 下载端点：字节与留存文件一致，头符合下载范式。
	dlRec := httptest.NewRecorder()
	h.backupImportAutoBackup(dlRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup", nil))
	if dlRec.Code != http.StatusOK {
		t.Fatalf("download HTTP status = %d, want 200 body = %s", dlRec.Code, dlRec.Body.String())
	}
	if ct := dlRec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("download Content-Type = %q, want application/json", ct)
	}
	wantCD := "attachment; filename=auto_backup_" + info.ModTime().Format("20060102_150405") + ".json"
	if cd := dlRec.Header().Get("Content-Disposition"); cd != wantCD {
		t.Fatalf("download Content-Disposition = %q, want %q", cd, wantCD)
	}
	if !bytes.Equal(dlRec.Body.Bytes(), data) {
		t.Fatal("downloaded bytes must equal the retained snapshot file bytes")
	}
	// 下载出的快照本身含导入前凭据（这是其用途）；回执不含（上文已断言）。
	if !strings.Contains(dlRec.Body.String(), sec01MarkerToken) {
		t.Fatal("downloaded snapshot must contain the pre-import marker (it is the pre-import state)")
	}
}

// (c)：下载鉴权与无快照处理。
func TestSEC01BC01DownloadAuth(t *testing.T) {
	h, _ := setupBC01Test(t)

	// 无快照：管理员 GET → HTTP 404 + 信封 code -2。
	rec := httptest.NewRecorder()
	h.backupImportAutoBackup(rec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("download without snapshot: HTTP status = %d, want 404 body = %s", rec.Code, rec.Body.String())
	}
	code, msg, _ := decodeBC01Envelope(t, rec)
	if code != -2 || msg != "暂无可下载的导入前自动备份" {
		t.Fatalf("download without snapshot: envelope code=%d msg=%q", code, msg)
	}

	// 非管理（无 claims / 普通用户 RoleID=1）→ 信封 403。
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/backup/import-auto-backup", nil),
		nonAdminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup", nil),
	} {
		rec := httptest.NewRecorder()
		h.backupImportAutoBackup(rec, req)
		code, msg, _ := decodeBC01Envelope(t, rec)
		if code != 403 || msg != "禁止访问敏感配置" {
			t.Fatalf("non-admin download: envelope code=%d msg=%q, want 403/禁止访问敏感配置", code, msg)
		}
	}
	rec = httptest.NewRecorder()
	h.backupImportAutoBackupStatus(rec, nonAdminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	code, msg, _ = decodeBC01Envelope(t, rec)
	if code != 403 || msg != "禁止访问敏感配置" {
		t.Fatalf("non-admin status: envelope code=%d msg=%q, want 403/禁止访问敏感配置", code, msg)
	}

	// 方法错误：POST → ErrDefault。
	rec = httptest.NewRecorder()
	h.backupImportAutoBackup(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/import-auto-backup", nil))
	code, _, _ = decodeBC01Envelope(t, rec)
	if code != -1 {
		t.Fatalf("POST download: envelope code=%d, want -1", code)
	}
	rec = httptest.NewRecorder()
	h.backupImportAutoBackupStatus(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/import-auto-backup/status", nil))
	code, _, _ = decodeBC01Envelope(t, rec)
	if code != -1 {
		t.Fatalf("POST status: envelope code=%d, want -1", code)
	}

	// 无快照时 status 为 exists=false、expiresAt=0。
	rec = httptest.NewRecorder()
	h.backupImportAutoBackupStatus(rec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	code, _, statusData := decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("status without snapshot: code = %d", code)
	}
	var status struct {
		Exists    bool  `json:"exists"`
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode status data: %v", err)
	}
	if status.Exists || status.ExpiresAt != 0 {
		t.Fatalf("status without snapshot: exists=%v expiresAt=%d, want false/0", status.Exists, status.ExpiresAt)
	}
}

// (d)：过期清理生效——过期后 status 为 false、下载 404、文件已删；
// 未过期（23h）仍有效；过期快照在下一次导入入口被清理并由新快照覆盖。
func TestSEC01BC01ExpiredSnapshotPurged(t *testing.T) {
	h, dir := setupBC01Test(t)
	r := gistTestRepo(t, h)
	seedSEC01Credentials(t, r)

	rec := callBC01Import(t, h, bc01ImportBody(t))
	code, _, _ := decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("backupImport code = %d body = %s", code, rec.Body.String())
	}
	path := bc01SnapshotPath(dir)

	// 未过期（23h 前）：仍有效。
	recent := time.Now().Add(-23 * time.Hour)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	statusRec := httptest.NewRecorder()
	h.backupImportAutoBackupStatus(statusRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	_, _, statusData := decodeBC01Envelope(t, statusRec)
	var status struct {
		Exists    bool  `json:"exists"`
		ExpiresAt int64 `json:"expiresAt"`
	}
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode status data: %v", err)
	}
	if !status.Exists {
		t.Fatal("23h-old snapshot must still exist")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("23h-old snapshot must not be deleted: %v", err)
	}

	// 过期（25h 前）：status 为 false、文件已删、下载 404。
	expired := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(path, expired, expired); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	statusRec = httptest.NewRecorder()
	h.backupImportAutoBackupStatus(statusRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	_, _, statusData = decodeBC01Envelope(t, statusRec)
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode status data: %v", err)
	}
	if status.Exists || status.ExpiresAt != 0 {
		t.Fatalf("expired snapshot: status exists=%v expiresAt=%d, want false/0", status.Exists, status.ExpiresAt)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired snapshot file must be deleted, stat err = %v", err)
	}
	dlRec := httptest.NewRecorder()
	h.backupImportAutoBackup(dlRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup", nil))
	if dlRec.Code != http.StatusNotFound {
		t.Fatalf("expired snapshot download: HTTP status = %d, want 404", dlRec.Code)
	}

	// 再次导入：过期快照在导入入口被清理，新快照覆盖留存。
	rec = callBC01Import(t, h, bc01ImportBody(t))
	code, _, _ = decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("second backupImport code = %d body = %s", code, rec.Body.String())
	}
	statusRec = httptest.NewRecorder()
	h.backupImportAutoBackupStatus(statusRec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	_, _, statusData = decodeBC01Envelope(t, statusRec)
	if err := json.Unmarshal(statusData, &status); err != nil {
		t.Fatalf("decode status data: %v", err)
	}
	if !status.Exists {
		t.Fatal("fresh snapshot must exist after second import")
	}
}

// 快照留存失败时导入失败（fail-closed）：无快照的导入不得破坏
// 「导入前必有快照」的承诺；DB 不被改写。
func TestSEC01BC01SnapshotFailureFailsImportClosed(t *testing.T) {
	h, dir := setupBC01Test(t)
	r := gistTestRepo(t, h)
	seedSEC01Credentials(t, r)

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	h.SetImportAutoBackupDir(blocker)

	rec := callBC01Import(t, h, bc01ImportBody(t))
	code, msg, _ := decodeBC01Envelope(t, rec)
	if code != -2 {
		t.Fatalf("import with unwritable snapshot dir: code=%d msg=%q, want -2", code, msg)
	}
	assertSEC01ReceiptClean(t, rec.Body.String())

	// DB 未被改写：新导入的键不存在。
	backup, err := r.ExportAllContext(context.Background())
	if err != nil {
		t.Fatalf("export after failed import: %v", err)
	}
	if _, ok := backup.Configs["imported_probe_key"]; ok {
		t.Fatal("failed import must not modify the DB")
	}
}
