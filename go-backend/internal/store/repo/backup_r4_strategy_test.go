package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4-F3-01 / R4-F4-01 / R4-F4-02 回归钉：备份恢复面（本地导入
// repo.Import）的策略双门——
//   - realm 隧道＋多目标非 {round, hash} 转发逐行跳过 + warning
//     点名（写面 BC-26 门的恢复面镜像；修复前原文落库、渲染静默
//     改写轮询且回执无声）；
//   - 策略合法集 {fifo, round, rand, hash} 与写面共用单点判，集外
//     值逐行跳过 + warning；空串按 create 缺省口径补 fifo 落库。
//
// 合法组合（realm+round/hash 多目标、realm+fifo 单目标、gost 任意
// 合法策略）必须照常恢复。
func TestImportForwardStrategyGates(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 7, Name: "realm-t", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, Engine: "realm", CreatedTime: now},
			{ID: 8, Name: "gost-t", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, Engine: "", CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			// realm + 多目标 fifo → 跳过（BC-26 静默改写形态）。
			{ID: 9, UserID: 1, Name: "realm-fifo-multi", TunnelID: 7, RemoteAddr: "203.0.113.10:80,203.0.113.11:80", Strategy: "fifo", Status: 1, CreatedTime: now},
			// realm + 多目标 round → 恢复。
			{ID: 10, UserID: 1, Name: "realm-round-multi", TunnelID: 7, RemoteAddr: "203.0.113.10:80,203.0.113.11:80", Strategy: "round", Status: 1, CreatedTime: now},
			// realm + 单目标 fifo → 恢复（无负载语义，不误伤）。
			{ID: 11, UserID: 1, Name: "realm-fifo-single", TunnelID: 7, RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now},
			// gost + 集外策略 → 跳过（合法集门）。
			{ID: 12, UserID: 1, Name: "gost-banana", TunnelID: 8, RemoteAddr: "203.0.113.10:80,203.0.113.11:80", Strategy: "banana", Status: 1, CreatedTime: now},
			// gost + 空策略 → 按 fifo 补齐恢复。
			{ID: 13, UserID: 1, Name: "gost-empty", TunnelID: 8, RemoteAddr: "203.0.113.10:80", Strategy: "", Status: 1, CreatedTime: now},
			// realm + 多目标 hash → 恢复。
			{ID: 14, UserID: 1, Name: "realm-hash-multi", TunnelID: 7, RemoteAddr: "203.0.113.10:80,203.0.113.11:80", Strategy: "hash", Status: 1, CreatedTime: now},
		},
	}
	backup.UserTunnels = []model.UserTunnelBackup{
		{ID: 1, UserID: 1, TunnelID: 7, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
		{ID: 2, UserID: 1, TunnelID: 8, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
	}
	result, err := dst.Import(backup, []string{"tunnels", "forwards", "userTunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForwardsImported != 4 {
		t.Fatalf("ForwardsImported = %d, want 4 (round/single-fifo/empty/hash), warnings=%v", result.ForwardsImported, result.ImportWarnings)
	}

	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "realm-fifo-multi") || !strings.Contains(warnText, "fifo") {
		t.Fatalf("receipt must name the realm-gated forward and its strategy, warnings=%v", result.ImportWarnings)
	}
	if !strings.Contains(warnText, "gost-banana") || !strings.Contains(warnText, "banana") {
		t.Fatalf("receipt must name the illegal-strategy forward and value, warnings=%v", result.ImportWarnings)
	}

	forwardStrategy := func(id int64) (string, bool) {
		var f model.Forward
		err := dst.db.Where("id = ?", id).First(&f).Error
		if err != nil {
			return "", false
		}
		return f.Strategy, true
	}
	if _, ok := forwardStrategy(9); ok {
		t.Fatal("realm multi-target fifo forward must not be restored")
	}
	if _, ok := forwardStrategy(12); ok {
		t.Fatal("illegal-strategy forward must not be restored")
	}
	if s, ok := forwardStrategy(10); !ok || s != "round" {
		t.Fatalf("realm round forward strategy = %q (ok=%v), want round", s, ok)
	}
	if s, ok := forwardStrategy(11); !ok || s != "fifo" {
		t.Fatalf("realm single-target fifo strategy = %q (ok=%v), want fifo", s, ok)
	}
	if s, ok := forwardStrategy(13); !ok || s != "fifo" {
		t.Fatalf("empty strategy must be restored as fifo, got %q (ok=%v)", s, ok)
	}
	if s, ok := forwardStrategy(14); !ok || s != "hash" {
		t.Fatalf("realm hash forward strategy = %q (ok=%v), want hash", s, ok)
	}
}
