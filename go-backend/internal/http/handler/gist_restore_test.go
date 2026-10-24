package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/middleware"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// PORT-3342/G-recovery：Gist 指定版本恢复必须走与现有 backupImport
// 完全相同的导入尾钩子全集（计量对账、configs 缓存失效、内核
// reconcile、撤销清理、转发重推含 FINAL-P2-1 七段谓词、缓存失效、
// AutoBackup），恢复段用我方 16 段全集，不得照抄上游 11 段直调 Import。
// 全部用 httptest mock GitHub API，不联网、不使用真实令牌。

func gistRestoreMockServer(t *testing.T, content []byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/gists/gist-restore-id/ver-1" {
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{gistFileName: map[string]interface{}{"content": string(content)}}})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FLMX_GIST_API_BASE", srv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")
}

func callGistRestore(t *testing.T, h *Handler, version, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"version": version, "password": password})
	if err != nil {
		t.Fatalf("marshal restore body: %v", err)
	}
	rec := httptest.NewRecorder()
	h.gistBackupRestore(rec, adminRequest(t, http.MethodPost, "/api/v1/backup/gist/restore", body))
	return rec
}

func decodeGistRestore(t *testing.T, rec *httptest.ResponseRecorder) (int, string, model.ImportResult) {
	t.Helper()
	var env struct {
		Code int                `json:"code"`
		Msg  string             `json:"msg"`
		Data model.ImportResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode restore envelope: %v body=%s", err, rec.Body.String())
	}
	return env.Code, env.Msg, env.Data
}

func seedGistRestoreConfigs(t *testing.T, r *repo.Repository) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.UpsertConfigs(map[string]string{
		gistConfigToken: "test-token-restore", gistConfigPassword: "enc-pw", gistConfigID: "gist-restore-id",
	}, now); err != nil {
		t.Fatalf("seed gist configs: %v", err)
	}
}

// 16 段全集（T-3）：与 repository.go validBackupTypes / 前端
// BACKUP_TYPE_VALUES 同集，顺序与之一致；上游 11 段不在此列。
func TestGistRestoreTypesAreSixteenSegmentFullSet(t *testing.T) {
	want := []string{"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits",
		"tunnelGroups", "userGroups", "permissions", "configs", "inbounds", "userInbounds",
		"announcements", "monitorPermissions", "userSubscriptions", "serviceMonitors"}
	if len(gistRestoreTypes) != len(want) {
		t.Fatalf("gistRestoreTypes = %v (%d segments), want 16", gistRestoreTypes, len(gistRestoreTypes))
	}
	seen := map[string]bool{}
	for _, v := range gistRestoreTypes {
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			t.Errorf("gistRestoreTypes missing segment %q: %v", v, gistRestoreTypes)
		}
	}
}

// 全尾钩子 + 在线节点转发重推：复用 R13BR 在线 agent 场景，
// Gist 全量备份里转发端口改 20000→20001，恢复后在线入口节点
// 必须收到带新端口的 UpdateService，回执重推清单含该节点、
// 失败清单为空，且 AutoBackup（恢复前快照）非空。
func TestGistRestoreRunsFullTailHooksAndRepushesOnlineNode(t *testing.T) {
	s := setupR13BR(t)
	seedGistRestoreConfigs(t, s.repo)
	backup, err := s.repo.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	mutated := false
	for i := range backup.Forwards {
		if backup.Forwards[i].ID == s.forwardID && backup.Forwards[i].ForwardPorts != nil {
			for j := range *backup.Forwards[i].ForwardPorts {
				if (*backup.Forwards[i].ForwardPorts)[j].NodeID == s.frontID {
					(*backup.Forwards[i].ForwardPorts)[j].Port = 20001
					mutated = true
				}
			}
		}
	}
	if !mutated {
		t.Fatal("setup: forward port missing from full export")
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	gistRestoreMockServer(t, raw)
	baseline := len(s.frontUpdateServices())
	rec := callGistRestore(t, s.h, "ver-1", "")
	code, msg, result := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("gist restore code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	if result.ForwardsImported < 1 {
		t.Fatalf("forwardsImported = %d, want >= 1", result.ForwardsImported)
	}
	found := false
	for _, id := range result.RepushNodeIDs {
		if id == s.frontID {
			found = true
		}
	}
	if !found {
		t.Fatalf("repushNodeIds must include online front node %d, got %v", result.RepushNodeIDs, s.frontID)
	}
	if len(result.RepushFailedNodeIDs) != 0 {
		t.Fatalf("repushFailedNodeIds must be empty, got %v", result.RepushFailedNodeIDs)
	}
	if result.AutoBackup == nil || result.AutoBackup.Version != "1.0" {
		t.Fatalf("AutoBackup must be the pre-import snapshot (version 1.0), got %+v", result.AutoBackup)
	}
	s.waitFrontUpdateWith(t, baseline, "20001")
	// 恢复不得覆盖当前 Gist 设置与凭据（B1 隔离 + importConfigs 过滤双层）。
	cfg, err := s.repo.GetConfigsByNames([]string{gistConfigToken, gistConfigID})
	if err != nil {
		t.Fatalf("read back gist configs: %v", err)
	}
	if cfg[gistConfigToken] != "test-token-restore" || cfg[gistConfigID] != "gist-restore-id" {
		t.Fatalf("restore must not overwrite gist settings, got %v", cfg)
	}
}

// O7-010：恢复前自动备份失败只记日志、继续导入（不得照抄上游中止语义）。
// 注入法：删掉 ExportAll 末段 service_monitor 表，使 ExportAll 在末段
// 失败；待恢复备份的 serviceMonitors 段为空，Import 跳过该段仍成功。
func TestGistRestoreAutoBackupFailureContinuesImport(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("restore-user", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll before break: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	if err := r.DB().Exec(`DROP TABLE service_monitor`).Error; err != nil {
		t.Fatalf("drop service_monitor: %v", err)
	}
	if _, err := r.ExportAll(); err == nil {
		t.Fatal("setup: ExportAll must fail after dropping service_monitor")
	}
	gistRestoreMockServer(t, raw)
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, result := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("restore with failed auto-backup must continue: code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	if result.UsersImported < 1 {
		t.Fatalf("usersImported = %d, want >= 1 (import must have run)", result.UsersImported)
	}
	if result.AutoBackup != nil {
		t.Fatal("AutoBackup must be nil when the pre-import snapshot failed")
	}
}

// Version 为空拒绝：解码后 Version == "" 不得进入 Import。
func TestGistRestoreRejectsEmptyVersion(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	gistRestoreMockServer(t, []byte(`{"users":[]}`))
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, _ := decodeGistRestore(t, rec)
	if code == 0 || !strings.Contains(msg, "版本") {
		t.Fatalf("empty-Version restore code = %d msg = %q, want version rejection", code, msg)
	}
}

// 解密失败拒绝：加密信封 + 错误密码不得进入 Import。
func TestGistRestoreRejectsBadPassword(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	enc, err := gistEncrypt([]byte(`{"version":"1.0","users":[]}`), "right-pw")
	if err != nil {
		t.Fatalf("gistEncrypt: %v", err)
	}
	gistRestoreMockServer(t, enc)
	rec := callGistRestore(t, h, "ver-1", "wrong-pw")
	code, _, _ := decodeGistRestore(t, rec)
	if code == 0 {
		t.Fatalf("wrong-password restore must fail, body = %s", rec.Body.String())
	}
}

// 加密恢复走通：正确密码解密后按 16 段全集导入。
func TestGistRestoreEncryptedRoundTrip(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("enc-restore-user", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	enc, err := gistEncrypt(raw, "enc-pw")
	if err != nil {
		t.Fatalf("gistEncrypt: %v", err)
	}
	gistRestoreMockServer(t, enc)
	rec := callGistRestore(t, h, "ver-1", "enc-pw")
	code, msg, result := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("encrypted restore code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	if result.UsersImported < 1 {
		t.Fatalf("usersImported = %d, want >= 1", result.UsersImported)
	}
}

// 恢复不得覆盖 Gist 凭据：备份 configs 段即使夹带 gist_* 键，
// importConfigs 的 FilterBackupConfigs 也须将其滤掉。
func TestGistRestoreDoesNotOverwriteGistCredentials(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	raw := []byte(`{"version":"1.0","configs":{"gist_token":"pwned-token","gist_password":"pwned-pw","gist_id":"pwned-id","some_key":"kept"}}`)
	gistRestoreMockServer(t, raw)
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, _ := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("restore code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	cfg, err := r.GetConfigsByNames([]string{gistConfigToken, gistConfigPassword, gistConfigID, "some_key"})
	if err != nil {
		t.Fatalf("read back configs: %v", err)
	}
	if cfg[gistConfigToken] != "test-token-restore" || cfg[gistConfigPassword] != "enc-pw" || cfg[gistConfigID] != "gist-restore-id" {
		t.Fatalf("gist credentials must survive restore, got %v", cfg)
	}
	if cfg["some_key"] != "kept" {
		t.Fatalf("non-gist config must import, some_key = %q", cfg["some_key"])
	}
}

// 非管理员 403（与 versions 端点同口径）。
func TestGistRestoreNonAdminForbidden(t *testing.T) {
	h := setupGistTest(t)
	rec := httptest.NewRecorder()
	body := []byte(`{"version":"ver-1","password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/gist/restore", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClaimsContextKey,
		auth.Claims{Sub: "2", User: "user", RoleID: 1}))
	h.gistBackupRestore(rec, req)
	code, _, _ := decodeGistEnvelope(t, rec)
	if code != 403 {
		t.Fatalf("non-admin restore code = %d, want 403, body = %s", code, rec.Body.String())
	}
}
