package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go-backend/internal/http/response"
)

// SEC-01 BC-01 (b)：导入前自动备份只留存服务端文件，恢复 A10-10 承诺。
//
//  - 快照在 backupImport 的 ImportContext 改写 DB 之前生成（ExportAllContext
//    全量导出），只保留最新一份（每次导入覆盖同一文件），24h 过期，下载不删除。
//  - 落盘目录与 DB 同级（与 jwt_secret 同惯例：<DBPath 同级>，默认
//    /app/data/import-auto-backup.json），权限 0600；写临时文件后 rename
//    发布，避免下载端读到半写文件。
//  - 快照字节绝不进 JSON 回执、日志、错误信息（日志只记字节数）；
//    ImportResult.AutoBackup 仍永不赋值（SEC-01 不回退）。
//  - 过期清理为懒清理：status/download/import 三处入口遇到超过 24h 的
//    快照即删除并按无快照处理；每次成功导入后也顺带清理一次。
//  - 范围只含 backupImport；gistBackupRestore 不动。

const (
	// importAutoBackupFileName 导入前自动备份快照的文件名（目录内唯一一份）。
	importAutoBackupFileName = "import-auto-backup.json"
	// importAutoBackupTTL 快照有效期：创建时间（文件 mtime）+ 24h。
	importAutoBackupTTL = 24 * time.Hour
)

// SetImportAutoBackupDir 设置导入前自动备份快照的留存目录。
// 由 app.New 以 filepath.Dir(cfg.DBPath) 显式设置；未设置时回落到
// repo.DBPath() 的同级目录。必须在开始服务前调用。
func (h *Handler) SetImportAutoBackupDir(dir string) {
	h.importAutoBackupDir = dir
}

// importAutoBackupPath 返回快照文件的完整落盘路径；无法确定目录时返回 ""。
// ":memory:" 与空 DBPath（Postgres 且未显式设置）均无本地目录可落盘。
func (h *Handler) importAutoBackupPath() string {
	dir := h.importAutoBackupDir
	if dir == "" {
		dir = filepath.Dir(h.repo.DBPath())
	}
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, importAutoBackupFileName)
}

// purgeExpiredImportAutoBackup 删除超过 24h 的快照文件（懒清理）。
// 无文件、未过期、stat 失败时均静默返回，不删除未确认过期者。
func (h *Handler) purgeExpiredImportAutoBackup() {
	h.importAutoBackupMu.Lock()
	defer h.importAutoBackupMu.Unlock()
	path := h.importAutoBackupPath()
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > importAutoBackupTTL {
		if err := os.Remove(path); err != nil {
			log.Printf("backupImport: remove expired pre-import auto-backup snapshot failed: %v", err)
			return
		}
		log.Printf("backupImport: expired pre-import auto-backup snapshot removed")
	}
}

// importAutoBackupMeta 返回快照是否存在及其过期时间（unix 秒）。
// 先做懒清理：过期快照按无快照处理；无快照时 expiresAt 为 0。
func (h *Handler) importAutoBackupMeta() (exists bool, expiresAt int64) {
	h.purgeExpiredImportAutoBackup()
	path := h.importAutoBackupPath()
	if path == "" {
		return false, 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, 0
	}
	return true, info.ModTime().Add(importAutoBackupTTL).Unix()
}

// saveImportAutoBackup 生成导入前全量快照并落盘（只保留最新一份）。
// 失败时返回 error，调用方应使导入失败（fail-closed），避免无快照的
// 导入破坏「导入前必有快照」的承诺。日志只记字节数，不记快照内容。
func (h *Handler) saveImportAutoBackup(ctx context.Context) error {
	backup, err := h.repo.ExportAllContext(ctx)
	if err != nil {
		return fmt.Errorf("export pre-import snapshot: %w", err)
	}
	data, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshal pre-import snapshot: %w", err)
	}

	h.importAutoBackupMu.Lock()
	defer h.importAutoBackupMu.Unlock()
	path := h.importAutoBackupPath()
	if path == "" {
		return fmt.Errorf("no directory configured for pre-import auto-backup snapshot")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write pre-import snapshot: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod pre-import snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("publish pre-import snapshot: %w", err)
	}
	log.Printf("backupImport: pre-import auto-backup snapshot saved (%d bytes)", len(data))
	return nil
}

// backupImportAutoBackup 下载导入前自动备份快照。
// GET /api/v1/backup/import-auto-backup，仅管理员；无快照（或已过期，
// 懒清理后按无快照处理）时返回 HTTP 404 + JSON 信封。
func (h *Handler) backupImportAutoBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	h.purgeExpiredImportAutoBackup()
	path := h.importAutoBackupPath()
	if path == "" {
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	// AUDIT-R22 同款范式：先把文件读进内存（buffer），成功后再发头+体，
	// 读失败时仍能返回 JSON 信封而不是半截下载流。
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("backupImportAutoBackup: read snapshot failed: %v", err)
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=auto_backup_"+info.ModTime().Format("20060102_150405")+".json")
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(data); err != nil {
		// headers+body 已发出，此时只能记录日志，无法再返回错误信封。
		log.Printf("backupImportAutoBackup: write response failed: %v", err)
		return
	}
}

// backupImportAutoBackupStatus 查询导入前自动备份快照是否存在及过期时间。
// GET /api/v1/backup/import-auto-backup/status，仅管理员；
// response.OK({"exists":bool,"expiresAt":unix 秒，无快照时 0})。
func (h *Handler) backupImportAutoBackupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	if !isAdminRequest(r) {
		response.WriteJSON(w, response.Err(403, "禁止访问敏感配置"))
		return
	}
	exists, expiresAt := h.importAutoBackupMeta()
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"exists":    exists,
		"expiresAt": expiresAt,
	}))
}
