package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// R5-B6 E-001/D1-010: agent 侧 seq 与先发送后清零。
//
// E-001: 每次上报携带持久化单调递增 seq（重启不回退，落盘）。
// D1-010: 上报成功后再清零本地计数器；失败则保留并重试。
//
// F-005: seq 文件必须落在 agent 配置目录（与 config.json 同级的持久
// 目录），不能放 os.TempDir()——节点 /tmp 常为 tmpfs 或被 tmpfiles
// 清理，整机重启后 seq 归零，面板按 seq<=last_seq 判重放丢弃却仍回
// ok，agent 收到成功后清零计数，流量被静默漏计（最坏数周）。
// 主程序启动时经 SetSeqFilePath 注入持久路径；未注入时回退 TempDir
// 并打警告。读取走多文件双读（持久文件 → TempDir 新名 → TempDir
// 改名前旧名）取最大值：存量节点平滑迁移，且 seq 绝不回退。

var (
	seqMu sync.Mutex
	// seqPersistFile 是持久目录中的 seq 文件路径（SetSeqFilePath 注入）。
	seqPersistFile string
	// 内存中的 seq（启动时从文件加载）。
	currentSeq    int64
	seqLoaded     bool
	seqTempWarned bool
)

// SetSeqFilePath 设置 seq 的持久落盘路径（agent 配置目录下）。
// 由主程序在加载 config.json 后调用；传空串恢复 TempDir 回退。
func SetSeqFilePath(path string) {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqPersistFile = path
}

// seqPrimaryFile 返回写入目标：优先持久路径，否则回退 TempDir。
func seqPrimaryFile() string {
	if seqPersistFile != "" {
		return seqPersistFile
	}
	return filepath.Join(os.TempDir(), "flmx_agent_seq.json")
}

// seqReadCandidates 返回按优先级排列的候选 seq 文件（去重）。
func seqReadCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(seqPersistFile)
	add(filepath.Join(os.TempDir(), "flmx_agent_seq.json"))
	add(filepath.Join(os.TempDir(), "flvx_agent_seq.json")) // 改名前的旧文件
	return out
}

func readSeqFile(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	var s struct {
		Seq int64 `json:"seq"`
	}
	if json.Unmarshal(data, &s) != nil {
		return 0, false
	}
	return s.Seq, true
}

// loadSeqLocked 在持有 seqMu 时加载 seq：多候选双读取最大值。
func loadSeqLocked() int64 {
	if seqLoaded {
		return currentSeq
	}
	for _, path := range seqReadCandidates() {
		if v, ok := readSeqFile(path); ok && v > currentSeq {
			currentSeq = v
		}
	}
	seqLoaded = true
	return currentSeq
}

// loadSeq 从文件加载 seq。
func loadSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	return loadSeqLocked()
}

// nextSeq 获取下一个 seq（持久化）。
func nextSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	loadSeqLocked()
	currentSeq++
	seq := currentSeq

	target := seqPrimaryFile()
	if seqPersistFile == "" && !seqTempWarned {
		seqTempWarned = true
		fmt.Printf("⚠️ seq 未配置持久路径，回退临时文件 %s；整机重启可能丢失 seq（F-005）\n", target)
	}
	if dir := filepath.Dir(target); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	data, _ := json.Marshal(map[string]int64{"seq": seq})
	if err := os.WriteFile(target, data, 0600); err != nil {
		// 写盘失败必须可见：进程内 seq 继续递增，但重启回退会被面板
		// 判重放——不能再像旧实现那样静默吞掉错误。
		fmt.Printf("❌ seq 落盘失败 (%s): %v\n", target, err)
	}
	return seq
}

// TrafficReportBatch 带 seq 的上报批次。
type TrafficReportBatch struct {
	Seq   int64               `json:"seq"`
	Items []TrafficReportItem `json:"items"`
}
