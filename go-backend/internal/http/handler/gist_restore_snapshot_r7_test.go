package handler

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R7-06：gist 恢复与本地 backupImport 同口径——ImportContext 改写
// DB 之前先生成恢复前服务端快照（saveImportAutoBackup，只落服务
// 端文件），失败按 O7-010 警告续行并在回执 ImportWarnings 披露。
// 快照失败披露格由 TestGistRestoreAutoBackupFailureContinuesImport
//（gist_restore_test.go）覆盖；本文件覆盖成功格与调用方全集守卫。

// 成功格：恢复完成后服务端留存了恢复前快照（0600、与 DB 同级），
// 快照内容是恢复前状态（含备份定格之后才创建的用户），回执不含
// AutoBackup 本体且 ImportWarnings 为空。
func TestGistRestoreTakesPreRestoreServerSnapshot(t *testing.T) {
	h := setupGistTest(t)
	r := gistTestRepo(t, h)
	seedGistRestoreConfigs(t, r)
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("restore-payload-user", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create payload user: %v", err)
	}
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	// 备份定格之后才出现的现行状态：只有恢复前（而非备份内）快照
	// 才能包含它，这是本格的判别点。
	if _, err := r.CreateUser("pre-restore-marker", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create marker user: %v", err)
	}
	gistRestoreMockServer(t, raw)
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, result := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("gist restore code = %d msg = %q body = %s", code, msg, rec.Body.String())
	}
	if result.AutoBackup != nil {
		t.Fatal("SEC-01: AutoBackup must be absent from the restore receipt")
	}
	if len(result.ImportWarnings) != 0 {
		t.Fatalf("snapshot succeeded, ImportWarnings must be empty, got %v", result.ImportWarnings)
	}
	path := filepath.Join(filepath.Dir(r.DBPath()), importAutoBackupFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("pre-restore snapshot file missing after restore: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file perm = %o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("snapshot tmp file must not linger, stat err = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap model.BackupData
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot is not valid BackupData JSON: %v", err)
	}
	users := map[string]bool{}
	for _, u := range snap.Users {
		users[u.User] = true
	}
	if !users["pre-restore-marker"] {
		t.Fatal("snapshot must capture the pre-restore state (marker user created after the backup was taken)")
	}
	if !users["restore-payload-user"] {
		t.Fatal("snapshot must capture the pre-restore state (payload user)")
	}
}

// 快照相位（写入前）中止：以 importAutoBackupMu 把 handler 确定
// 性地挡在快照相位入口（临界区内首步 purge 须拿该锁），此时
// cancel 属「写入前取消」——整单不启动、0 落库、回执点名「恢复
// 已取消」（与本地导入 CB14 的写入前取消格同口径）。定相位不用
// sleep 猜：轮询 backupImportMu 被 handler 占用即其已入临界区、
// 而快照相位被测试持锁挡住不可能越过。
func TestGistRestoreAbortDuringSnapshotCancels(t *testing.T) {
	h := setupGistTest(t)
	seedGistRestoreConfigs(t, gistTestRepo(t, h))
	served := make(chan struct{})
	w2GistMockServer(t, w2GistBackupContent(t, w2GistUsers(10, 700000)), served)

	h.importAutoBackupMu.Lock()
	req, cancel := w2GistRestoreRequest(t, "ver-1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.gistBackupRestore(rec, req)
	}()

	select {
	case <-served:
	case <-time.After(2 * time.Minute):
		t.Fatal("gist download never happened")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if h.backupImportMu.TryLock() {
			h.backupImportMu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("gist restore never entered its critical section")
			}
			time.Sleep(2 * time.Millisecond)
			continue
		}
		break
	}
	// handler 已在临界区内、且快照相位被 importAutoBackupMu 挡住：
	// 此刻取消必落在写入之前。
	cancel()
	h.importAutoBackupMu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("gist restore handler did not return after snapshot-phase abort")
	}

	code, msg, _ := decodeGistRestore(t, rec)
	if code != -2 || !strings.Contains(msg, "恢复已取消") {
		t.Fatalf("snapshot-phase abort: expected -2 恢复已取消, got code=%d msg=%q body=%s", code, msg, rec.Body.String())
	}
	if got := w2GistCountImportedUsers(t, h); got != 0 {
		t.Fatalf("snapshot-phase abort: %d users landed, want 0", got)
	}
}

// 调用方全集守卫：handler 包生产代码里 ImportContext 的全部调用
// 点，其所属函数在调用之前必须先出现 saveImportAutoBackup( 调用
// （快照先于改写）。新增加的 ImportContext 调用方若不先生成快照，
// 本测试直接报红点名。调用点计数钉死为 2（backupImport 与
// gistBackupRestore）；新增调用方时须同步更新计数并满足前置条件。
func TestImportContextCallersTakePreImportSnapshot(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read handler dir: %v", err)
	}
	sites := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(src)
		for idx := 0; ; {
			j := strings.Index(text[idx:], ".ImportContext(")
			if j < 0 {
				break
			}
			callAt := idx + j
			fnStart := strings.LastIndex(text[:callAt], "\nfunc ")
			if fnStart < 0 {
				t.Fatalf("%s: ImportContext call at offset %d has no enclosing func", name, callAt)
			}
			if !strings.Contains(text[fnStart:callAt], "saveImportAutoBackup(") {
				t.Errorf("%s: ImportContext call (offset %d) is not preceded by saveImportAutoBackup in its enclosing function", name, callAt)
			}
			sites++
			idx = callAt + 1
		}
	}
	if sites != 2 {
		t.Fatalf("ImportContext production call sites in handler package = %d, want 2 (backupImport, gistBackupRestore); a new caller must take the pre-import snapshot first and update this count", sites)
	}
}
