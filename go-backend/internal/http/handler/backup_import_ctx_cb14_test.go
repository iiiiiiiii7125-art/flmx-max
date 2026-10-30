package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// B14-001 回归（用户 2026-10-30 裁决④折中）：备份导入两相位 ctx 语义。
//
//   - 快照相位（写入前）仍绑请求 ctx：客户端在此相位中止 ⇒ 整单不
//     启动、0 落库（TestCB14...AbortBeforeWriteCancelsWholeImport）。
//   - 写入相位改用解绑 ctx（WithoutCancel + backupImportTimeout）：
//     客户端在写入中途断开 ⇒ 整单仍跑完、全量落库
//     （TestCB14...WritePhaseSurvivesClientAbort）。修复前此用例必红：
//     ImportContext 绑 r.Context()，cancel 后整单回滚、计数为 0。
//   - 解绑后关页不再是取消手段，故进程内互斥：第二单并发导入直接
//     被拒（TestCB14...ConcurrentSecondImportRejected）。
//
// repo 层 ImportContext 尊重所传入 ctx 的契约不在此重测，仍由
// TestR1B8_XP321 锁定（本次改动只改 handler 传参，不动该契约）。

const cb14UserPrefix = "cb14user"
const cb14UserCount = 30000

func cb14Users(n int, idBase int64) []model.UserBackup {
	users := make([]model.UserBackup, 0, n)
	for i := 0; i < n; i++ {
		users = append(users, model.UserBackup{
			ID:     idBase + int64(i),
			User:   fmt.Sprintf("%s%06d", cb14UserPrefix, i),
			Pwd:    "$2a$10$cb14cb14cb14cb14cb14cb14cb14cb14cb14cb14cb14cb14cb1",
			Status: 1,
		})
	}
	return users
}

func cb14ImportBody(t *testing.T, users []model.UserBackup) []byte {
	t.Helper()
	body := struct {
		Types []string `json:"types"`
		*model.BackupData
	}{Types: []string{"users"}, BackupData: &model.BackupData{Version: "1.0", Users: users}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import body: %v", err)
	}
	return raw
}

func cb14CountImportedUsers(t *testing.T, h *Handler) int {
	t.Helper()
	data, err := h.repo.ExportPartial([]string{"users"})
	if err != nil {
		t.Fatalf("export users for counting: %v", err)
	}
	count := 0
	for _, u := range data.Users {
		if strings.HasPrefix(u.User, cb14UserPrefix) {
			count++
		}
	}
	return count
}

func cb14Envelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

// cb14WaitSnapshot polls until the pre-import snapshot file exists,
// i.e. the snapshot phase has finished publishing and the handler is
// at/just past the phase boundary into the write phase.
func cb14WaitSnapshot(t *testing.T, h *Handler) {
	t.Helper()
	path := h.importAutoBackupPath()
	if path == "" {
		t.Fatal("snapshot path unavailable in fixture")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("pre-import snapshot never appeared")
}

// 写入相位断连：整单跑完、全量落库、回执 code=0。
func TestCB14BackupImportWritePhaseSurvivesClientAbort(t *testing.T) {
	h := r8NewHandler(t)
	body := cb14ImportBody(t, cb14Users(cb14UserCount, 100000))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.backupImport(rec, req)
	}()

	// 等快照落盘（快照相位结束）后稍过相位边界，再模拟客户端断开。
	cb14WaitSnapshot(t, h)
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("import handler did not return after client abort")
	}

	if code, msg := cb14Envelope(t, rec); code != 0 {
		t.Fatalf("write-phase abort: import must complete, got code=%d msg=%q", code, msg)
	}
	if got := cb14CountImportedUsers(t, h); got != cb14UserCount {
		t.Fatalf("write-phase abort: %d users landed, want %d (detached ctx must run the import to completion)", got, cb14UserCount)
	}
}

// 快照相位（写入前）中止：整单不启动、0 落库。
// 用 importAutoBackupMu 把 handler 确定性地挡在快照相位之前：
// backupImport 的首个 purge 步骤须拿该锁，测试持锁期间 handler
// 必然尚未进入快照/写入，此时 cancel 即「写入前取消」。
func TestCB14BackupImportAbortBeforeWriteCancelsWholeImport(t *testing.T) {
	h := r8NewHandler(t)
	body := cb14ImportBody(t, cb14Users(100, 200000))

	h.importAutoBackupMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.backupImport(rec, req)
	}()

	// handler 此时必被挡在 purge（快照相位之前）；取消后放行。
	time.Sleep(300 * time.Millisecond)
	cancel()
	h.importAutoBackupMu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("import handler did not return")
	}

	code, msg := cb14Envelope(t, rec)
	if code == 0 {
		t.Fatalf("pre-write abort: import must not run, got code=0 body=%s", rec.Body.String())
	}
	if !strings.Contains(msg, "导入已取消") {
		t.Fatalf("pre-write abort: msg must state the cancellation, got %q", msg)
	}
	if got := cb14CountImportedUsers(t, h); got != 0 {
		t.Fatalf("pre-write abort: %d users landed, want 0", got)
	}
}

// 并发第二单：第一单写入进行中时，第二单直接被拒（不排队、不叠写）。
func TestCB14BackupImportConcurrentSecondImportRejected(t *testing.T) {
	h := r8NewHandler(t)
	body := cb14ImportBody(t, cb14Users(cb14UserCount, 300000))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.backupImport(rec, req)
	}()

	// 第一单已过快照相位、写入进行中（互斥锁全程持有）。
	cb14WaitSnapshot(t, h)
	time.Sleep(100 * time.Millisecond)

	secondBody := cb14ImportBody(t, cb14Users(10, 400000))
	secondReq := httptest.NewRequest(http.MethodPost, "/api/v1/backup/import", bytes.NewReader(secondBody))
	secondRec := httptest.NewRecorder()
	h.backupImport(secondRec, secondReq)
	code, msg := cb14Envelope(t, secondRec)
	if code == 0 {
		t.Fatalf("concurrent import must be rejected, got code=0 body=%s", secondRec.Body.String())
	}
	if !strings.Contains(msg, "已有备份导入正在执行") {
		t.Fatalf("concurrent import: msg must name the in-progress import, got %q", msg)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("first import did not return")
	}
	if code, msg := cb14Envelope(t, rec); code != 0 {
		t.Fatalf("first import must complete, got code=%d msg=%q", code, msg)
	}
	if got := cb14CountImportedUsers(t, h); got != cb14UserCount {
		t.Fatalf("first import: %d users landed, want %d (second import must not have added any)", got, cb14UserCount)
	}
}
