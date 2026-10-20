package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// R5-B6 E-001/D1-010: agent 侧 seq 与先发送后清零。
//
// E-001: 每次上报携带持久化单调递增 seq（重启不回退，落盘）。
// D1-010: 上报成功后再清零本地计数器；失败则保留并重试。

var (
	seqMu sync.Mutex
	// 改名过渡：seq 落盘改用 flmx 文件名；新文件不存在时回退读旧文件，
	// 避免 seq 归零导致面板去重失效、流量重复计账。
	seqFile       = filepath.Join(os.TempDir(), "flmx_agent_seq.json")
	legacySeqFile = filepath.Join(os.TempDir(), "flvx_agent_seq.json")
	// 内存中的 seq（启动时从文件加载）。
	currentSeq int64
	seqLoaded  bool
)

// loadSeq 从文件加载 seq。
func loadSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	if seqLoaded {
		return currentSeq
	}
	data, err := os.ReadFile(seqFile)
	if err != nil {
		data, err = os.ReadFile(legacySeqFile)
	}
	if err == nil {
		var s struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(data, &s) == nil {
			currentSeq = s.Seq
		}
	}
	seqLoaded = true
	return currentSeq
}

// nextSeq 获取下一个 seq（持久化）。
func nextSeq() int64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	if !seqLoaded {
		seqMu.Unlock()
		loadSeq()
		seqMu.Lock()
	}
	currentSeq++
	// 落盘。
	data, _ := json.Marshal(map[string]int64{"seq": currentSeq})
	os.WriteFile(seqFile, data, 0600)
	return currentSeq
}

// TrafficReportBatch 带 seq 的上报批次。
type TrafficReportBatch struct {
	Seq   int64               `json:"seq"`
	Items []TrafficReportItem `json:"items"`
}
