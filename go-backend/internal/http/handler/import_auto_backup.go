package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"go-backend/internal/http/response"
)

// SEC-01 BC-01 (b)：导入前自动备份只留存服务端文件，恢复 A10-10 承诺。
//
//  - 快照在 backupImport 的 ImportContext 改写 DB
//    之前生成（ExportAllContext 全量导出），R18-B9-02 起按时间戳
//    多份保留（import-auto-backup-<unix>.json，最新 3 份），24h 过期，
//    下载不删除；升级前已存在的单份旧快照 import-auto-backup.json
//    仍被认作一份有效快照参与保留与下载，不被新导入覆盖销毁。
//  - 落盘目录与 DB 同级（与 jwt_secret 同惯例：<DBPath 同级>），权限
//    0600；写临时文件后 rename 发布，避免下载端读到半写文件。
//  - 快照字节绝不进 JSON 回执、日志、错误信息（日志只记字节数）；
//    ImportResult.AutoBackup 仍永不赋值（SEC-01 不回退）。
//  - 过期清理为懒清理：status/download/import 三处入口遇到超过 24h 的
//    快照即删除并按无快照处理；每次成功导入后也顺带清理一次。保留
//    份数只在成功落盘新快照后轮换（保存失败不缩减回滚覆盖）。

const (
	// importAutoBackupFileName 升级前单份形态的旧快照文件名（兼容
	// 读口径：仍计入保留份数、可下载；新快照不再用此名落盘）。
	importAutoBackupFileName = "import-auto-backup.json"
	// importAutoBackupTTL 快照有效期：创建时间（文件 mtime）+ 24h。
	importAutoBackupTTL = 24 * time.Hour
	// importAutoBackupRetain 保留的最新快照份数（R18-B9-02）。
	importAutoBackupRetain = 3
)

// importAutoBackupNameRe 匹配合法的快照文件名：旧单份名，或
// import-auto-backup-<unix 秒>[-<同秒序号>].json。
var importAutoBackupNameRe = regexp.MustCompile(`^import-auto-backup(-\d+(-\d+)?)?\.json$`)

// importAutoBackupSnapshot 是一份导入前快照的清单条目（只含元数据，
// 快照字节永不进回执）。
type importAutoBackupSnapshot struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	Size      int64  `json:"size"`
	path      string
	// modTime 是完整精度的 mtime（排序用）：同秒落盘的两份快照
	// Unix 秒相同，必须按完整 mtime 分先后——按文件名 tie-break
	// 会排错（同秒序号后缀 "-2" 的字典序小于 ".json"，会把旧份
	// 排到前面）。
	modTime time.Time
}

// SetImportAutoBackupDir 设置导入前自动备份快照的留存目录。
// 由 app.New 以 filepath.Dir(cfg.DBPath) 显式设置；未设置时回落到
// repo.DBPath() 的同级目录。必须在开始服务前调用。
func (h *Handler) SetImportAutoBackupDir(dir string) {
	h.importAutoBackupDir = dir
}

// importAutoBackupDir 返回快照留存目录；无法确定目录时返回 ""。
// ":memory:" 与空 DBPath（Postgres 且未显式设置）均无本地目录可落盘。
func (h *Handler) importAutoBackupDirPath() string {
	dir := h.importAutoBackupDir
	if dir == "" {
		dir = filepath.Dir(h.repo.DBPath())
	}
	if dir == "" || dir == "." {
		return ""
	}
	return dir
}

// listImportAutoBackupSnapshotsLocked 列出留存目录下的全部快照，按
// mtime 从新到旧排序（mtime 相同时按文件名降序，保证顺序确定）。
// 调用方须持 importAutoBackupMu。
func (h *Handler) listImportAutoBackupSnapshotsLocked() []importAutoBackupSnapshot {
	dir := h.importAutoBackupDirPath()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var snaps []importAutoBackupSnapshot
	for _, e := range entries {
		if e.IsDir() || !importAutoBackupNameRe.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snaps = append(snaps, importAutoBackupSnapshot{
			Name:      e.Name(),
			CreatedAt: info.ModTime().Unix(),
			ExpiresAt: info.ModTime().Add(importAutoBackupTTL).Unix(),
			Size:      info.Size(),
			path:      filepath.Join(dir, e.Name()),
			modTime:   info.ModTime(),
		})
	}
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].modTime.Equal(snaps[j].modTime) {
			return snaps[i].modTime.After(snaps[j].modTime)
		}
		return snaps[i].Name > snaps[j].Name
	})
	return snaps
}

// listImportAutoBackupSnapshots 是 listImportAutoBackupSnapshotsLocked
// 的加锁包装（不做过期清理，调用方按需先调 purge）。
func (h *Handler) listImportAutoBackupSnapshots() []importAutoBackupSnapshot {
	h.importAutoBackupMu.Lock()
	defer h.importAutoBackupMu.Unlock()
	return h.listImportAutoBackupSnapshotsLocked()
}

// purgeExpiredImportAutoBackup 删除超过 24h 的快照文件（懒清理）。
// 无文件、未过期、stat 失败时均静默返回，不删除未确认过期者。
func (h *Handler) purgeExpiredImportAutoBackup() {
	h.importAutoBackupMu.Lock()
	defer h.importAutoBackupMu.Unlock()
	now := time.Now()
	removed := 0
	for _, snap := range h.listImportAutoBackupSnapshotsLocked() {
		if now.Unix() <= snap.ExpiresAt {
			continue
		}
		if err := os.Remove(snap.path); err != nil {
			log.Printf("backupImport: remove expired pre-import auto-backup snapshot failed: %v", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Printf("backupImport: %d expired pre-import auto-backup snapshot(s) removed", removed)
	}
}

// pruneImportAutoBackupRetentionLocked 在成功落盘新快照后把保留份数
// 轮换到最新 importAutoBackupRetain 份（只删未过期的旧份中超出者；
// 调用方须持 importAutoBackupMu，且须在新快照已落盘后调用）。
func (h *Handler) pruneImportAutoBackupRetentionLocked() {
	snaps := h.listImportAutoBackupSnapshotsLocked()
	if len(snaps) <= importAutoBackupRetain {
		return
	}
	for _, snap := range snaps[importAutoBackupRetain:] {
		if err := os.Remove(snap.path); err != nil {
			log.Printf("backupImport: prune old pre-import auto-backup snapshot failed: %v", err)
			continue
		}
		log.Printf("backupImport: old pre-import auto-backup snapshot pruned (%s)", snap.Name)
	}
}

// importAutoBackupMeta 返回最新快照是否存在及其过期时间（unix 秒）。
// 先做懒清理：过期快照按无快照处理；无快照时 expiresAt 为 0。
func (h *Handler) importAutoBackupMeta() (exists bool, expiresAt int64) {
	h.purgeExpiredImportAutoBackup()
	snaps := h.listImportAutoBackupSnapshots()
	if len(snaps) == 0 {
		return false, 0
	}
	return true, snaps[0].ExpiresAt
}

// importAutoBackupNewPath 为一次新快照挑一个不冲突的时间戳文件名
// （同秒重复导入时追加序号后缀）。调用方须持 importAutoBackupMu。
func (h *Handler) importAutoBackupNewPath(now time.Time) string {
	dir := h.importAutoBackupDirPath()
	if dir == "" {
		return ""
	}
	unix := now.Unix()
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("import-auto-backup-%d.json", unix)
		if i > 0 {
			name = fmt.Sprintf("import-auto-backup-%d-%d.json", unix, i+1)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}
	return ""
}

// saveImportAutoBackup 生成导入前全量快照并落盘（R18-B9-02：带时间戳
// 的多份保留，最新 3 份）。失败时返回 error，调用方只记警告并继续
// 导入（O7-010：库已损坏致 ExportAll 失败时，用户正是最需要恢复的
// 场景，不得中断导入）。日志只记字节数，不记快照内容。
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
	path := h.importAutoBackupNewPath(time.Now())
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
	// 轮换只在成功落盘后做：保存失败不缩减已有的回滚覆盖。
	h.pruneImportAutoBackupRetentionLocked()
	log.Printf("backupImport: pre-import auto-backup snapshot saved (%d bytes)", len(data))
	return nil
}

// backupImportAutoBackup 下载导入前自动备份快照。
// GET /api/v1/backup/import-auto-backup，仅管理员；无快照（或已过期，
// 懒清理后按无快照处理）时返回 HTTP 404 + JSON 信封。
// R18-B9-02：默认下载最新一份；?snapshot=<文件名> 下载指定一份，
// 文件名须在当前快照清单内（防路径穿越与任意文件读取）。
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
	snaps := h.listImportAutoBackupSnapshots()
	if len(snaps) == 0 {
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	want := r.URL.Query().Get("snapshot")
	snap := snaps[0]
	if want != "" {
		found := false
		for _, s := range snaps {
			if s.Name == want {
				snap = s
				found = true
				break
			}
		}
		if !found {
			response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "指定的导入前自动备份不存在或已过期"))
			return
		}
	}
	info, err := os.Stat(snap.path)
	if err != nil {
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	// AUDIT-R22 同款范式：先把文件读进内存（buffer），成功后再发头+体，
	// 读失败时仍能返回 JSON 信封而不是半截下载流。
	data, err := os.ReadFile(snap.path)
	if err != nil {
		log.Printf("backupImportAutoBackup: read snapshot failed: %v", err)
		response.WriteJSONStatus(w, http.StatusNotFound, response.Err(-2, "暂无可下载的导入前自动备份"))
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=auto_backup_"+info.ModTime().Format("20060102_150405")+".json")
	w.Header().Set("Content-Type", "application/json")
	// C-17：快照含全量敏感配置的导入前备份，与 backupExport 同口径
	// 禁存——直接写头下载面绕过 JSON 信封（WriteJSON 自带 no-store），
	// 此处须显式补 Cache-Control: no-store。
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(data); err != nil {
		// headers+body 已发出，此时只能记录日志，无法再返回错误信封。
		log.Printf("backupImportAutoBackup: write response failed: %v", err)
		return
	}
}

// backupImportAutoBackupStatus 查询导入前自动备份快照是否存在及过期时间。
// GET /api/v1/backup/import-auto-backup/status，仅管理员；
// response.OK({"exists":bool,"expiresAt":unix 秒，无快照时 0，
// "snapshots":[{"name","createdAt","expiresAt","size"}]})。
// exists/expiresAt 仍指最新一份（兼容旧客户端）；snapshots 列出全部
// 未过期份（R18-B9-02）。
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
	snaps := h.listImportAutoBackupSnapshots()
	if snaps == nil {
		snaps = []importAutoBackupSnapshot{}
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{
		"exists":    exists,
		"expiresAt": expiresAt,
		"snapshots": snaps,
	}))
}
