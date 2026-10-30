package service

import (
	"os"
	"path/filepath"
	"testing"
)

// R2-A02-002 回归：seq 落盘必须原子 + 带备份，撕裂文件不得让 seq
// 回退到 1。
//
// 背景：旧实现单次 os.WriteFile 直接覆写 seq 文件，崩溃/掉电恰落覆写
// 窗口即产生截断 JSON；重启加载时解析失败被静默跳过、无其他候选时
// seq 归 0，nextSeq 从 1 重报——面板按 seq<=last_seq 判重放回 ok 不
// 入账，agent 据 ok 清零本地计数，最坏数周量级流量静默漏计（V5 已
// 实证 5001→1）。修复口径：写入走同目录 tmp+fsync+rename 原子写并
// 同步维护 .bak；加载候选含 .bak，主文件损坏时取备份值；进程内已
// 加载的内存值永不因磁盘损坏回退。

func TestSeqTornPrimaryRecoversFromBackup(t *testing.T) {
	resetSeqStateForTest(t)
	t.Setenv("TMPDIR", t.TempDir())
	persistPath := filepath.Join(t.TempDir(), "flmx_agent_seq.json")
	SetSeqFilePath(persistPath)

	// 存量节点：文件里已有 seq=5000。
	if err := os.WriteFile(persistPath, []byte(`{"seq":5000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	simulateAgentRestartForTest()
	if got := nextSeq(); got != 5001 {
		t.Fatalf("nextSeq = %d, want 5001", got)
	}
	// 主文件与备份都应落到 5001。
	for _, p := range []string{persistPath, persistPath + ".bak"} {
		v, ok := readSeqFile(p)
		if !ok || v != 5001 {
			t.Fatalf("%s = %d, %v; want 5001, true", p, v, ok)
		}
	}

	// 撕裂主文件（模拟旧实现覆写窗口掉电留下的截断 JSON）。
	if err := os.WriteFile(persistPath, []byte(`{"seq":50`), 0o600); err != nil {
		t.Fatal(err)
	}
	simulateAgentRestartForTest()
	if got := loadSeq(); got != 5001 {
		t.Fatalf("主文件撕裂后 loadSeq = %d, want 5001（从 .bak 恢复，禁止回退到 1）", got)
	}
	if got := nextSeq(); got != 5002 {
		t.Fatalf("恢复后 nextSeq = %d, want 5002", got)
	}
}

func TestSeqInMemoryValueNeverRegressesOnDiskCorruption(t *testing.T) {
	resetSeqStateForTest(t)
	t.Setenv("TMPDIR", t.TempDir())
	persistPath := filepath.Join(t.TempDir(), "flmx_agent_seq.json")
	SetSeqFilePath(persistPath)

	if got := nextSeq(); got != 1 {
		t.Fatalf("nextSeq = %d, want 1", got)
	}
	// 运行中主文件与备份同时被外部破坏：进程内已加载值不得回退。
	for _, p := range []string{persistPath, persistPath + ".bak"} {
		if err := os.WriteFile(p, []byte(`garbage`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := nextSeq(); got != 2 {
		t.Fatalf("磁盘损坏后 nextSeq = %d, want 2（保持内存值继续）", got)
	}
	// 本次成功落盘后文件恢复完整。
	if v, ok := readSeqFile(persistPath); !ok || v != 2 {
		t.Fatalf("重写后主文件 = %d, %v; want 2, true", v, ok)
	}
}

func TestSeqAtomicWriteLeavesCompleteFileAndNoTmpResidue(t *testing.T) {
	resetSeqStateForTest(t)
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	persistPath := filepath.Join(dir, "flmx_agent_seq.json")
	SetSeqFilePath(persistPath)

	for i := 0; i < 5; i++ {
		nextSeq()
	}
	if v, ok := readSeqFile(persistPath); !ok || v != 5 {
		t.Fatalf("主文件 = %d, %v; want 5, true（内容必须是完整 JSON）", v, ok)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("原子写残留临时文件: %s", e.Name())
		}
	}
}

func TestSeqAllCopiesTornFallsBackToZeroWithWarning(t *testing.T) {
	// 边界登记：主文件、备份与全部迁移候选同时损坏且进程刚启动
	// （内存无值）时无从恢复，只能从 0 起——此时 loadSeqLocked 会
	// 打出 ❌ 告警留痕，不再像旧实现那样静默归零。
	resetSeqStateForTest(t)
	t.Setenv("TMPDIR", t.TempDir())
	persistPath := filepath.Join(t.TempDir(), "flmx_agent_seq.json")
	SetSeqFilePath(persistPath)
	for _, p := range []string{persistPath, persistPath + ".bak"} {
		if err := os.WriteFile(p, []byte(`{"seq":`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	simulateAgentRestartForTest()
	if got := loadSeq(); got != 0 {
		t.Fatalf("全损坏时 loadSeq = %d, want 0（无可用值可恢复）", got)
	}
}
