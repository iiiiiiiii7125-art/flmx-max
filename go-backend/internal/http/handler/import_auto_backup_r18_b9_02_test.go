package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R18-B9-02：导入前自动备份只有单份、每次导入覆盖同一文件，第二单
// 导入静默销毁第一单的回滚点。以下测试先于修复落盘（先红后绿）。

func r18B902ImportBody(key string) []byte {
	raw, err := json.Marshal(map[string]interface{}{
		"version": "1.0",
		"types":   []string{"configs"},
		"configs": map[string]string{key: "plain-value"},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// r18B902SnapshotNames 列出 dir 下的全部导入前快照文件（新旧两种
// 文件名形态），按名排序返回。
func r18B902SnapshotNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if n == importAutoBackupFileName ||
			(strings.HasPrefix(n, "import-auto-backup-") && strings.HasSuffix(n, ".json")) {
			names = append(names, n)
		}
	}
	return names
}

func r18B902StatusSnapshots(t *testing.T, h *Handler) []struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	Size      int64  `json:"size"`
} {
	t.Helper()
	rec := httptest.NewRecorder()
	h.backupImportAutoBackupStatus(rec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup/status", nil))
	code, _, data := decodeBC01Envelope(t, rec)
	if code != 0 {
		t.Fatalf("status code = %d body = %s", code, rec.Body.String())
	}
	var status struct {
		Exists    bool  `json:"exists"`
		ExpiresAt int64 `json:"expiresAt"`
		Snapshots []struct {
			Name      string `json:"name"`
			CreatedAt int64  `json:"createdAt"`
			ExpiresAt int64  `json:"expiresAt"`
			Size      int64  `json:"size"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatalf("decode status: %v body=%s", err, rec.Body.String())
	}
	return status.Snapshots
}

func r18B902Download(t *testing.T, h *Handler, query string) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	h.backupImportAutoBackup(rec, adminRequest(t, http.MethodGet, "/api/v1/backup/import-auto-backup"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("download %q code = %d body = %s", query, rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

func r18B902UserImportBody(id int64, username string) []byte {
	raw, err := json.Marshal(map[string]interface{}{
		"version": "1.0",
		"types":   []string{"users"},
		"users": []map[string]interface{}{
			{"id": id, "user": username, "pwd": "", "roleId": 1, "status": 1,
				"flow": 0, "num": 0, "flowResetTime": 0, "createdTime": 1000},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func r18B902SnapshotUsernames(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	var snap model.BackupData
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot is not valid BackupData JSON: %v", err)
	}
	names := map[string]bool{}
	for _, u := range snap.Users {
		names[u.User] = true
	}
	return names
}

// TestR18B902TwoImportsKeepBothSnapshots：连续两次导入后，两次导入
// 前的快照都还在（旧代码：第二单覆盖同一文件，只剩 1 份）。
// 以 users 段作区分标记（configs 未分类键不进导出物，无法区分）。
func TestR18B902TwoImportsKeepBothSnapshots(t *testing.T) {
	h, dir := setupBC01Test(t)

	if rec := callBC01Import(t, h, r18B902UserImportBody(501, "first-user")); rec.Code != http.StatusOK {
		t.Fatalf("first import code = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec := callBC01Import(t, h, r18B902UserImportBody(502, "second-user")); rec.Code != http.StatusOK {
		t.Fatalf("second import code = %d body = %s", rec.Code, rec.Body.String())
	}

	names := r18B902SnapshotNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("R18-B9-02：两次导入后快照应为 2 份，got %d（%v）", len(names), names)
	}
	snaps := r18B902StatusSnapshots(t, h)
	if len(snaps) != 2 {
		t.Fatalf("R18-B9-02：status 应列出 2 份快照，got %d", len(snaps))
	}

	// 最新一份 = 第二单导入前的状态（含 first-user、不含 second-user）。
	latestNames := r18B902SnapshotUsernames(t, r18B902Download(t, h, ""))
	if !latestNames["first-user"] {
		t.Fatalf("R18-B9-02：最新快照应为第二单导入前状态（含 first-user），got %v", latestNames)
	}
	if latestNames["second-user"] {
		t.Fatalf("R18-B9-02：最新快照不得含第二单导入的 second-user")
	}

	// 最旧一份 = 第一单导入前的状态（两个用户都不含）。
	// status 清单按 mtime 从新到旧排，末位即最旧份（同秒落盘时
	// Unix 秒相同，不能按 CreatedAt 再比大小）。
	oldest := snaps[len(snaps)-1]
	olderNames := r18B902SnapshotUsernames(t, r18B902Download(t, h, "?snapshot="+oldest.Name))
	if olderNames["first-user"] || olderNames["second-user"] {
		t.Fatalf("R18-B9-02：第一单导入前的快照不得含任一导入用户，got %v", olderNames)
	}
}

// TestR18B902RetentionAndExpiry：保留最近 3 份 + 24h 过期清理。
// 预置 1h/2h/3h 前的三份与一份 25h 前的过期份；一次导入后应只剩
// 3 份（新 + 1h + 2h），过期份与最旧份均被清理。
func TestR18B902RetentionAndExpiry(t *testing.T) {
	h, dir := setupBC01Test(t)
	now := time.Now()
	seed := func(age time.Duration) string {
		name := "import-auto-backup-" + itoaUnix(now.Add(-age)) + ".json"
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(`{"version":"1.0","configs":{}}`), 0o600); err != nil {
			t.Fatalf("seed snapshot: %v", err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatalf("seed mtime: %v", err)
		}
		return name
	}
	name1h := seed(time.Hour)
	name2h := seed(2 * time.Hour)
	name3h := seed(3 * time.Hour)
	nameExpired := seed(25 * time.Hour)

	// status 入口先做懒清理：过期份应已消失，其余三份仍在。
	if names := r18B902SnapshotNames(t, dir); len(names) != 4 {
		t.Fatalf("seed 后快照应为 4 份，got %d（%v）", len(names), names)
	}
	_ = r18B902StatusSnapshots(t, h)
	if _, err := os.Stat(filepath.Join(dir, nameExpired)); !os.IsNotExist(err) {
		t.Fatalf("R18-B9-02：25h 前的过期快照应被懒清理删除")
	}
	if names := r18B902SnapshotNames(t, dir); len(names) != 3 {
		t.Fatalf("R18-B9-02：过期清理后快照应为 3 份，got %d（%v）", len(names), names)
	}

	// 再导入一次：新快照落盘后保留最近 3 份，3h 前的最旧份被轮换掉。
	if rec := callBC01Import(t, h, r18B902ImportBody("imported_probe_key")); rec.Code != http.StatusOK {
		t.Fatalf("import code = %d body = %s", rec.Code, rec.Body.String())
	}
	names := r18B902SnapshotNames(t, dir)
	if len(names) != 3 {
		t.Fatalf("R18-B9-02：保留上限 3 份，导入后 got %d（%v）", len(names), names)
	}
	for _, want := range []string{name1h, name2h} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("R18-B9-02：较新的两份快照 %s 应保留，现有 %v", want, names)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, name3h)); !os.IsNotExist(err) {
		t.Fatalf("R18-B9-02：3h 前的最旧快照应在轮换后被删除")
	}
}

// TestR18B902LegacySingleFileStillReadable：升级前已存在的单份旧
// 快照 import-auto-backup.json 仍被认作一份有效快照，可下载、可列出，
// 不被新一次导入覆盖销毁。
func TestR18B902LegacySingleFileStillReadable(t *testing.T) {
	h, dir := setupBC01Test(t)
	legacy := filepath.Join(dir, importAutoBackupFileName)
	if err := os.WriteFile(legacy, []byte(`{"version":"1.0","configs":{"legacy_marker":"yes"}}`), 0o600); err != nil {
		t.Fatalf("seed legacy snapshot: %v", err)
	}

	if rec := callBC01Import(t, h, r18B902ImportBody("imported_probe_key")); rec.Code != http.StatusOK {
		t.Fatalf("import code = %d body = %s", rec.Code, rec.Body.String())
	}
	names := r18B902SnapshotNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("R18-B9-02：旧单份 + 新快照应共存 2 份，got %d（%v）", len(names), names)
	}
	found := false
	for _, n := range names {
		if n == importAutoBackupFileName {
			found = true
		}
	}
	if !found {
		t.Fatalf("R18-B9-02：旧单份快照 %s 不得被新导入覆盖销毁", importAutoBackupFileName)
	}
	legacyBytes := r18B902Download(t, h, "?snapshot="+importAutoBackupFileName)
	var snap model.BackupData
	if err := json.Unmarshal(legacyBytes, &snap); err != nil {
		t.Fatalf("legacy snapshot is not valid BackupData JSON: %v", err)
	}
	if snap.Configs["legacy_marker"] != "yes" {
		t.Fatalf("R18-B9-02：旧快照内容应原样可下载，got %v", snap.Configs)
	}
}

func itoaUnix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}
