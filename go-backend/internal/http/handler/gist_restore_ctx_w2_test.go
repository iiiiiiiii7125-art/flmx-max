package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Round 1 波 2 / GIST-CTX 回归：Gist 恢复按 B14-001 裁决④的机制
// 扩展，与本地 backupImport 同口径两相位 ctx 语义。
//
//   - 准备相位（下载/解密/解析）仍绑请求 ctx：下载中途中止 ⇒ 整单
//     不启动、0 落库（TestW2GistRestoreAbortDuringDownloadCancels）。
//   - 写入相位（ImportContext）改用解绑 ctx：写入中途断开 ⇒ 整单
//     仍跑完、全量落库（TestW2GistRestoreWritePhaseSurvivesClientAbort）。
//     修复前此用例必红：ImportContext 绑 r.Context()，cancel 后整单
//     回滚、计数为 0。
//   - 写入相位与本地导入共用 backupImportMu：锁被占用时恢复直接
//     被拒、不落库（TestW2GistRestoreRejectedWhileImportMutexHeld）。
//
// repo 层 ImportContext 尊重所传入 ctx 的契约不在此重测，仍由
// TestR1B8_XP321 锁定。

const w2GistUserPrefix = "w2gistuser"

func w2GistUsers(n int, idBase int64) []model.UserBackup {
	users := make([]model.UserBackup, 0, n)
	for i := 0; i < n; i++ {
		users = append(users, model.UserBackup{
			ID:     idBase + int64(i),
			User:   fmt.Sprintf("%s%06d", w2GistUserPrefix, i),
			Pwd:    "$2a$10$w2gistw2gistw2gistw2gistw2gistw2gistw2gistw2gistw2g",
			Status: 1,
		})
	}
	return users
}

func w2GistBackupContent(t *testing.T, users []model.UserBackup) []byte {
	t.Helper()
	raw, err := json.Marshal(model.BackupData{Version: "1.0", Users: users})
	if err != nil {
		t.Fatalf("marshal gist backup content: %v", err)
	}
	return raw
}

func w2GistCountImportedUsers(t *testing.T, h *Handler) int {
	t.Helper()
	data, err := h.repo.ExportPartial([]string{"users"})
	if err != nil {
		t.Fatalf("export users for counting: %v", err)
	}
	count := 0
	for _, u := range data.Users {
		if strings.HasPrefix(u.User, w2GistUserPrefix) {
			count++
		}
	}
	return count
}

// w2GistRestoreRequest 构造带管理员 claims 且 ctx 可取消的恢复请求。
func w2GistRestoreRequest(t *testing.T, version string) (*http.Request, context.CancelFunc) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"version": version, "password": ""})
	if err != nil {
		t.Fatalf("marshal restore body: %v", err)
	}
	req := adminRequest(t, http.MethodPost, "/api/v1/backup/gist/restore", body)
	ctx, cancel := context.WithCancel(req.Context())
	return req.WithContext(ctx), cancel
}

// w2GistMockServer 是 gistRestoreMockServer 的可观测变体：served 在
// 内容被取走时关闭，供测试确定性地把 cancel 落在写入相位。
func w2GistMockServer(t *testing.T, content []byte, served chan<- struct{}) {
	t.Helper()
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/gists/gist-restore-id/ver-1" {
			once.Do(func() { close(served) })
			writeJSON(w, 200, map[string]interface{}{"files": map[string]interface{}{gistFileName: map[string]interface{}{"content": string(content)}}})
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FLMX_GIST_API_BASE", srv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")
}

// 写入相位断连：整单跑完、全量落库、回执 code=0。
func TestW2GistRestoreWritePhaseSurvivesClientAbort(t *testing.T) {
	h := setupGistTest(t)
	seedGistRestoreConfigs(t, gistTestRepo(t, h))
	const userCount = 30000
	served := make(chan struct{})
	w2GistMockServer(t, w2GistBackupContent(t, w2GistUsers(userCount, 100000)), served)

	req, cancel := w2GistRestoreRequest(t, "ver-1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.gistBackupRestore(rec, req)
	}()

	// 等下载被取走，再轮询共用互斥：handler 抢到 backupImportMu 即
	// 已越过相位边界、进入写入相位（锁在 ImportContext 全程持有）。
	// 以锁状态作判别信号，不用固定 sleep 猜相位——30k 用户的解析
	// 耗时随机器负载变化，sleep 会把 cancel 落进准备相位（解析中），
	// 那属于「写入前取消」的另一格语义，不是本用例要验的形态。
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
				t.Fatal("gist restore never entered its write phase")
			}
			time.Sleep(2 * time.Millisecond)
			continue
		}
		break
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("gist restore handler did not return after client abort")
	}

	code, msg, _ := decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("write-phase abort: restore must complete, got code=%d msg=%q", code, msg)
	}
	if got := w2GistCountImportedUsers(t, h); got != userCount {
		t.Fatalf("write-phase abort: %d users landed, want %d (detached ctx must run the import to completion)", got, userCount)
	}
}

// 准备相位（下载中）中止：整单不启动、0 落库。
func TestW2GistRestoreAbortDuringDownloadCancels(t *testing.T) {
	h := setupGistTest(t)
	seedGistRestoreConfigs(t, gistTestRepo(t, h))

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/gists/") {
			once.Do(func() { close(entered) })
			// 挂起下载，直到客户端 ctx 取消或测试释放。
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		writeJSON(w, 404, map[string]interface{}{"message": "not found"})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	t.Setenv("FLMX_GIST_API_BASE", srv.URL)
	t.Setenv("FLVX_GIST_API_BASE", "")

	req, cancel := w2GistRestoreRequest(t, "ver-1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.gistBackupRestore(rec, req)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Minute):
		t.Fatal("gist download never started")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("gist restore handler did not return after download abort")
	}

	code, msg, _ := decodeGistRestore(t, rec)
	if code == 0 {
		t.Fatalf("download-phase abort: restore must not run, got code=0 body=%s", rec.Body.String())
	}
	if !strings.Contains(msg, "从 Gist 恢复失败") {
		t.Fatalf("download-phase abort: msg must surface the download failure, got %q", msg)
	}
	if got := w2GistCountImportedUsers(t, h); got != 0 {
		t.Fatalf("download-phase abort: %d users landed, want 0", got)
	}
}

// 共用互斥：backupImportMu 被占用（本地导入进行中）时，Gist 恢复在
// 写入相位前直接被拒、0 落库；释放后同一恢复可正常完成。
func TestW2GistRestoreRejectedWhileImportMutexHeld(t *testing.T) {
	h := setupGistTest(t)
	seedGistRestoreConfigs(t, gistTestRepo(t, h))
	gistRestoreMockServer(t, w2GistBackupContent(t, w2GistUsers(10, 500000)))

	h.backupImportMu.Lock()
	rec := callGistRestore(t, h, "ver-1", "")
	code, msg, _ := decodeGistRestore(t, rec)
	h.backupImportMu.Unlock()
	if code != -2 || !strings.Contains(msg, "已有备份导入正在执行") {
		t.Fatalf("mutex held: expected -2 已有备份导入正在执行, got code=%d msg=%q", code, msg)
	}
	if got := w2GistCountImportedUsers(t, h); got != 0 {
		t.Fatalf("mutex held: %d users landed, want 0", got)
	}

	rec = callGistRestore(t, h, "ver-1", "")
	code, msg, _ = decodeGistRestore(t, rec)
	if code != 0 {
		t.Fatalf("mutex released: restore must succeed, got code=%d msg=%q", code, msg)
	}
	if got := w2GistCountImportedUsers(t, h); got != 10 {
		t.Fatalf("mutex released: %d users landed, want 10", got)
	}
}
