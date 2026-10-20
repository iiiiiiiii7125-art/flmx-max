package service

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// F-005 回归：seq 必须落在 agent 配置目录（持久），整机重启 /tmp 被清空
// （tmpfs）后不得归零——归零的 seq 会被面板按重放（seq <= last_seq）
// 丢弃且面板仍回 ok，agent 清零本地计数，造成流量静默漏计。

func resetSeqStateForTest(t *testing.T) {
	t.Helper()
	seqMu.Lock()
	savedPersist, savedSeq, savedLoaded := seqPersistFile, currentSeq, seqLoaded
	seqMu.Unlock()
	t.Cleanup(func() {
		seqMu.Lock()
		seqPersistFile, currentSeq, seqLoaded = savedPersist, savedSeq, savedLoaded
		seqMu.Unlock()
	})
	seqMu.Lock()
	seqPersistFile, currentSeq, seqLoaded = "", 0, false
	seqMu.Unlock()
}

func simulateAgentRestartForTest() {
	seqMu.Lock()
	currentSeq, seqLoaded = 0, false
	seqMu.Unlock()
}

func TestSeqSurvivesTempDirWipe(t *testing.T) {
	resetSeqStateForTest(t)
	persistDir := t.TempDir()
	SetSeqFilePath(filepath.Join(persistDir, "flmx_agent_seq.json"))
	t.Setenv("TMPDIR", t.TempDir())

	var last int64
	for i := 0; i < 3; i++ {
		last = nextSeq()
	}
	if last != 3 {
		t.Fatalf("nextSeq = %d, want 3", last)
	}

	// 整机重启：进程内存清零，且 /tmp 是全新空目录（tmpfs 语义）。
	simulateAgentRestartForTest()
	t.Setenv("TMPDIR", t.TempDir())

	if got := loadSeq(); got != 3 {
		t.Fatalf("重启后 seq 回退为 %d，want 3；面板会把后续上报按重放静默丢弃", got)
	}
	if got := nextSeq(); got != 4 {
		t.Fatalf("重启后 nextSeq = %d, want 4", got)
	}
}

func TestSeqMigratesLegacyTempDirValue(t *testing.T) {
	resetSeqStateForTest(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// 旧版本只在 TempDir 留下 seq=41。
	if err := os.WriteFile(filepath.Join(tmp, "flmx_agent_seq.json"), []byte(`{"seq":41}`), 0o600); err != nil {
		t.Fatal(err)
	}
	persistPath := filepath.Join(t.TempDir(), "flmx_agent_seq.json")
	SetSeqFilePath(persistPath)

	if got := loadSeq(); got != 41 {
		t.Fatalf("双读迁移失败: loadSeq = %d, want 41", got)
	}
	if got := nextSeq(); got != 42 {
		t.Fatalf("nextSeq = %d, want 42", got)
	}
	data, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("seq 未落盘到持久路径: %v", err)
	}
	if string(data) != `{"seq":42}` {
		t.Fatalf("持久文件内容 = %s, want {\"seq\":42}", data)
	}
}

func TestSeqNeverGoesBackwardsAcrossFiles(t *testing.T) {
	resetSeqStateForTest(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// 改名前的旧文件（flvx）里有更大的 seq 时，必须取最大值，不能回退。
	if err := os.WriteFile(filepath.Join(tmp, "flvx_agent_seq.json"), []byte(`{"seq":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	persistPath := filepath.Join(t.TempDir(), "flmx_agent_seq.json")
	if err := os.WriteFile(persistPath, []byte(`{"seq":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	SetSeqFilePath(persistPath)

	if got := loadSeq(); got != 9 {
		t.Fatalf("loadSeq = %d, want 9（多文件取最大值，seq 绝不回退）", got)
	}
}

func TestNextSeqConcurrentUnique(t *testing.T) {
	resetSeqStateForTest(t)
	SetSeqFilePath(filepath.Join(t.TempDir(), "flmx_agent_seq.json"))
	t.Setenv("TMPDIR", t.TempDir())

	const n = 50
	seen := make(map[int64]bool, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := nextSeq()
			mu.Lock()
			seen[s] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("并发 nextSeq 产生 %d 个不同值, want %d（不得重复）", len(seen), n)
	}
}
