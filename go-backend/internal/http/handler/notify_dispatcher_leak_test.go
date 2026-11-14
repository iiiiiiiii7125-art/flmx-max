package handler

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R5-B-03：notify 派发 worker 泄漏回归。
//
// 机制：Handler.New() 总是装配非空 notifier，首次 notify 触发
// ensureNotifyDispatcher 懒启动 4 个 runNotifyWorker；未调
// StartBackgroundJobs 的 handler 派发 ctx 是 Background，而
// StopBackgroundJobs 旧逻辑在 !jobsStarted 时直接 return——
// 懒启动的 worker 永远停不掉（旧 20m 超时 dump 里的大量
// runNotifyWorker 即此物；DB connectionOpener 随 repo.Close
// 退出，不是泄漏，见对照用例）。
//
// 本用例以进程内 goroutine 栈的增量断言锁定机制：notify 后
// +4，StopBackgroundJobs 后归零；计数用增量（同进程其余测试
// 也可能留有 worker，绝对值无意义）。
func countGoroutinesContaining(t *testing.T, substr string) int {
	t.Helper()
	buf := make([]byte, 4<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), substr)
}

func waitForGoroutineCount(t *testing.T, substr string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := countGoroutinesContaining(t, substr); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines containing %q did not reach %d within %s (last=%d)", substr, want, timeout, countGoroutinesContaining(t, substr))
}

func TestNotifyDispatcherStopsWithoutBackgroundJobs(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/notify-leak.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	t.Cleanup(func() { h.StopBackgroundJobs() })

	base := countGoroutinesContaining(t, "runNotifyWorker")
	h.notify("test_event", "leak probe", "probe")
	waitForGoroutineCount(t, "runNotifyWorker", base+notifyWorkerCount, 5*time.Second)

	// 未调 StartBackgroundJobs：StopBackgroundJobs 仍须停掉
	// 懒启动的派发器（旧实现此处直接 return，worker 泄漏）。
	h.StopBackgroundJobs()
	waitForGoroutineCount(t, "runNotifyWorker", base, 5*time.Second)

	// 二次停止幂等、不再起 worker。
	h.StopBackgroundJobs()
	time.Sleep(100 * time.Millisecond)
	if got := countGoroutinesContaining(t, "runNotifyWorker"); got != base {
		t.Fatalf("second StopBackgroundJobs changed worker count: got %d want %d", got, base)
	}
}

// 对照：DB connectionOpener 随 repo.Close 退出——旧 dump 里的
// connectionOpener 是当时在跑的库，不是泄漏；若此处失败，
// 说明泄漏口径有误，须重定性 R5-B-03。
func TestRepositoryCloseStopsConnectionOpener(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/opener.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// 用一次查询迫使连接池真正建连接（否则 opener 可能尚未起）。
	if _, err := r.GetNodeByID(1); err != nil {
		t.Fatalf("probe query: %v", err)
	}
	base := countGoroutinesContaining(t, "connectionOpener")
	if base < 1 {
		t.Fatalf("expected at least one connectionOpener while repo open, got %d", base)
	}

	r2, err := repo.Open(t.TempDir() + "/opener2.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.GetNodeByID(1); err != nil {
		t.Fatalf("probe query 2: %v", err)
	}
	waitForGoroutineCount(t, "connectionOpener", base+1, 5*time.Second)
	if err := r2.Close(); err != nil {
		t.Fatalf("close second repo: %v", err)
	}
	waitForGoroutineCount(t, "connectionOpener", base, 5*time.Second)
}
