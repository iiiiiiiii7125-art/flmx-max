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
// R2-A02-002: 每个主文件后紧跟其 .bak 备份（nextSeq 每次落盘同步维护），
// 主文件撕裂/损坏时从备份恢复，绝不回退到 1。
func seqReadCandidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	addWithBackup := func(p string) {
		add(p)
		if p != "" {
			add(p + ".bak")
		}
	}
	addWithBackup(seqPersistFile)
	addWithBackup(filepath.Join(os.TempDir(), "flmx_agent_seq.json"))
	addWithBackup(filepath.Join(os.TempDir(), "flvx_agent_seq.json")) // 改名前的旧文件
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
// R2-A02-002: 候选文件存在但解析失败（撕裂/损坏）时告警留痕——旧实现
// 静默跳过、全部候选皆坏时 currentSeq 归 0，重启后 seq 从 1 重报、被
// 面板判重放回 ok、agent 据 ok 清零，流量长期静默漏计且无任何日志。
func loadSeqLocked() int64 {
	if seqLoaded {
		return currentSeq
	}
	corrupt := ""
	for _, path := range seqReadCandidates() {
		if v, ok := readSeqFile(path); ok {
			if v > currentSeq {
				currentSeq = v
			}
			continue
		}
		if _, err := os.Stat(path); err == nil && corrupt == "" {
			corrupt = path
		}
	}
	if corrupt != "" {
		if currentSeq > 0 {
			fmt.Printf("⚠️ seq 文件损坏 (%s)，已从备份/其他候选恢复 seq=%d（R2-A02-002）\n", corrupt, currentSeq)
		} else {
			fmt.Printf("❌ seq 文件损坏 (%s) 且无可用备份，seq 将从头计数；面板可能按重放丢弃在途批次（R2-A02-002）\n", corrupt)
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

// writeSeqFileAtomic 原子落盘：同目录临时文件写满 + fsync 后 rename
// 覆盖目标，再 fsync 目录。崩溃/掉电落在任何一步，目标文件要么是旧的
// 完整内容、要么是新的完整内容，绝不出现写一半的撕裂文件。
func writeSeqFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// nextSeq 获取下一个 seq（持久化）。
// R2-A02-002: 落盘改原子写（tmp+rename+fsync）并同步维护 .bak 备份：
// 旧实现单次 os.WriteFile 直接覆写，崩溃/掉电恰落覆写窗口即撕裂文件，
// 重启后 seq 回退到 1、被面板判重放回 ok、agent 据 ok 清零，数周量级
// 流量静默漏计。现主文件即使损坏，加载侧可从 .bak 恢复（见
// seqReadCandidates/loadSeqLocked）；进程内 currentSeq 永不因落盘
// 失败而回退。
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
	data, _ := json.Marshal(map[string]int64{"seq": seq})
	if err := writeSeqFileAtomic(target, data); err != nil {
		// 写盘失败必须可见：进程内 seq 继续递增，但重启回退会被面板
		// 判重放——不能再像旧实现那样静默吞掉错误。
		fmt.Printf("❌ seq 落盘失败 (%s): %v\n", target, err)
	} else if err := writeSeqFileAtomic(target+".bak", data); err != nil {
		fmt.Printf("⚠️ seq 备份落盘失败 (%s.bak): %v\n", target, err)
	}
	return seq
}

// TrafficReportBatch 带 seq 的上报批次。
type TrafficReportBatch struct {
	Seq   int64               `json:"seq"`
	Items []TrafficReportItem `json:"items"`
}
