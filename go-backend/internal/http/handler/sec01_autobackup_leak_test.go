package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// SEC-01：backupImport / gistBackupRestore 的回执不得内嵌导入前全量快照
// （ImportResult.autoBackup）。快照是整库导出，含真实凭据——用户密码
// hash、节点 secret、配置 token；把它放进 API 回执即向任何能调该接口
// 的调用方（及一切记录回执的日志）泄露凭据。以下标记串均为合成测试值，
// 绝非真实凭据。

const (
	sec01MarkerPwdHash    = "SECRET-MARKER-SEC01-PWHASH-A1B2C3"
	sec01MarkerNodeSecret = "SECRET-MARKER-SEC01-NODESECRET-A1B2C3"
	sec01MarkerToken      = "SECRET-MARKER-SEC01-TOKEN-A1B2C3"
)

// seedSEC01Credentials 在 live 库中植入三类合成标记凭据：用户密码 hash、
// 节点 secret、配置 token。导入请求本身不含任何标记串，因此回执中一旦
// 出现标记串，只能来自导入前自动备份（autoBackup）。
func seedSEC01Credentials(t *testing.T, r *repo.Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("sec01-user", sec01MarkerPwdHash, 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, '', '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`,
		"sec01-node", sec01MarkerNodeSecret, "198.51.100.99", "198.51.100.99", now, now).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := r.UpsertConfigs(map[string]string{"sec01_probe_key": sec01MarkerToken}, now); err != nil {
		t.Fatalf("seed config: %v", err)
	}
}

// assertSEC01ReceiptClean 断言导入回执 JSON 既不含任一标记凭据串，
// 也不含 autoBackup 字段本身。
func assertSEC01ReceiptClean(t *testing.T, body string) {
	t.Helper()
	for _, m := range []string{sec01MarkerPwdHash, sec01MarkerNodeSecret, sec01MarkerToken} {
		if strings.Contains(body, m) {
			t.Fatalf("import receipt leaks credential marker %q", m)
		}
	}
	if strings.Contains(body, `"autoBackup"`) {
		t.Fatal(`import receipt must not contain "autoBackup" field`)
	}
}

// 文件导入入口：回执不得含导入前快照。
func TestSEC01BackupImportReceiptMustNotContainAutoBackup(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedSEC01Credentials(t, r)

	// 导入体只含一个普通 configs 键，不含任何标记串。
	raw, err := json.Marshal(map[string]interface{}{
		"version": "1.0",
		"types":   []string{"configs"},
		"configs": map[string]string{"imported_probe_key": "plain-value"},
	})
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	rec := httptest.NewRecorder()
	h.backupImport(rec, httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(raw)))
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code != 0 {
		t.Fatalf("backupImport code = %d err = %v body = %s", env.Code, err, rec.Body.String())
	}
	assertSEC01ReceiptClean(t, rec.Body.String())
}

// Gist 恢复入口：回执同样不得含导入前快照。
func TestSEC01GistRestoreReceiptMustNotContainAutoBackup(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	seedSEC01Credentials(t, r)

	// 恢复体只含一个普通 configs 键，不含任何标记串。
	gistRestoreMockServer(t, []byte(`{"version":"1.0","configs":{"imported_probe_key":"plain-value"}}`))
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, _ := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("gist restore code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	assertSEC01ReceiptClean(t, rec.Body.String())
}
