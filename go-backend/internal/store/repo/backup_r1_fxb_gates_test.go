package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R1-03-M1/M2 + R1-07-F3 回归钉：恢复面镜像门集——
//   - 链路策略按段合法集（kernel.ChainStrategyLegal 单点）：入口/
//     跳行集外值与跳段 best 逐行跳过 + warning，出口 best 落库；
//   - 隧道名长门、链路 ConnectIP 格式门；
//   - 转发名长门、远程地址段数帽（100）、目标端口格式门；
//   - forward_port 恢复行四源占用复检 + 节点端口范围复检：冲突/
//     越界行点名跳过 + 回执计数，不静默落库、不整单失败。
func strPtr(s string) *string { return &s }

func fpSlice(rows ...model.ForwardPortBackup) *[]model.ForwardPortBackup { return &rows }

func TestImportChainStrategyGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 51, Name: "n51", Secret: "s", ServerIP: "192.0.2.51", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 21, Name: "chain-t", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
				// 入口行集外策略 → 跳过。
				{ID: 1, TunnelID: 21, ChainType: "1", NodeID: 51, Strategy: strPtr("banana")},
				// 出口行 best → 合法落库。
				{ID: 2, TunnelID: 21, ChainType: "3", NodeID: 51, Strategy: strPtr("best")},
				// 跳行 best → 跳过（best 仅出口/入口段，入口行此处
				// 用 chainType 2 验证跳段口径）。
				{ID: 3, TunnelID: 21, ChainType: "2", NodeID: 51, Strategy: strPtr("best")},
				// 跳行 rand → 落库。
				{ID: 4, TunnelID: 21, ChainType: "2", NodeID: 51, Strategy: strPtr("rand")},
				// ConnectIP 格式非法 → 跳过。
				{ID: 5, TunnelID: 21, ChainType: "1", NodeID: 51, Strategy: strPtr("fifo"), ConnectIP: strPtr("bad host!")},
			}},
			// 隧道名超长 → 整隧道跳过。
			{ID: 22, Name: strings.Repeat("隧", 101), TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TunnelsImported != 1 {
		t.Fatalf("TunnelsImported = %d, want 1, warnings=%v", result.TunnelsImported, result.ImportWarnings)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"banana", "best", "连接地址格式非法"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name skipped value %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	var chains []model.ChainTunnel
	if err := dst.db.Where("tunnel_id = ?", 21).Find(&chains).Error; err != nil {
		t.Fatal(err)
	}
	got := make(map[int64]string)
	for _, c := range chains {
		if c.Strategy.Valid {
			got[c.ID] = c.Strategy.String
		}
	}
	if len(chains) != 2 || got[2] != "best" || got[4] != "rand" {
		t.Fatalf("restored chains = %v, want exactly {2:best, 4:rand}", got)
	}
}

func TestImportForwardRowGates(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	manySegments := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		manySegments = append(manySegments, "203.0.113.10:80")
	}
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 31, Name: "n31", Secret: "s", ServerIP: "192.0.2.31", Port: "3000-3010", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 71, Name: "t71", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			// 名超长 → 跳过。
			{ID: 61, UserID: 1, TunnelID: 71, Name: strings.Repeat("名", 101), RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now},
			// 远程地址 101 段 → 跳过（段数帽 100）。
			{ID: 62, UserID: 1, TunnelID: 71, Name: "too-many-segments", RemoteAddr: strings.Join(manySegments, ","), Strategy: "fifo", Status: 1, CreatedTime: now},
			// 目标端口格式非法 → 跳过。
			{ID: 63, UserID: 1, TunnelID: 71, Name: "bad-target-port", RemoteAddr: "203.0.113.10:99999", Strategy: "fifo", Status: 1, CreatedTime: now},
			// 合法转发：范围内端口行落库、越界端口行跳过。
			{ID: 64, UserID: 1, TunnelID: 71, Name: "ok-forward", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now, ForwardPorts: fpSlice(
				model.ForwardPortBackup{NodeID: 31, Port: 3005},
				model.ForwardPortBackup{NodeID: 31, Port: 2000},
			)},
		},
	}
	backup.UserTunnels = []model.UserTunnelBackup{
		{ID: 1, UserID: 1, TunnelID: 71, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards", "userTunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForwardsImported != 1 {
		t.Fatalf("ForwardsImported = %d, want 1, warnings=%v", result.ForwardsImported, result.ImportWarnings)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"too-many-segments", "bad-target-port", "2000", "不在节点允许端口范围"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	var ports []model.ForwardPort
	if err := dst.db.Where("forward_id = ?", 64).Find(&ports).Error; err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 || ports[0].Port != 3005 {
		t.Fatalf("forward 64 ports = %v, want exactly [3005]", ports)
	}
}

// TestImportForwardPortOccupancyHoleShape 钉住预检排除集的形状洞：
// 备份内转发 A 在恢复时被策略门跳过（其 ForwardPorts 为 nil、既有
// DB 端口行存活），转发 B 声明同一 (节点, 端口)——预检因排除集按
// 备份 ID 整单组装而放行，行内复检必须把 B 的端口行点名跳过，
// 不得落出重复 (node_id, port) 行。
func TestImportForwardPortOccupancyHoleShape(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	// 存量：转发 9101 及其端口行（节点 41，端口 2000）。
	if err := dst.db.Create(&model.Forward{ID: 9101, UserID: 1, UserName: "admin@user", Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.ForwardPort{ForwardID: 9101, NodeID: 41, Port: 2000}).Error; err != nil {
		t.Fatal(err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 41, Name: "n41", Secret: "s", ServerIP: "192.0.2.41", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 72, Name: "t72", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			// A：策略非法被跳过，ForwardPorts 为 nil——其存量端口
			// 行在导入后仍存活。
			{ID: 9101, UserID: 1, TunnelID: 72, Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "banana", Status: 1, CreatedTime: now},
			// B：合法转发，声明同一 (节点, 端口)。
			{ID: 9202, UserID: 1, TunnelID: 72, Name: "claimer-b", RemoteAddr: "203.0.113.11:80", Strategy: "fifo", Status: 1, CreatedTime: now, ForwardPorts: fpSlice(
				model.ForwardPortBackup{NodeID: 41, Port: 2000},
			)},
		},
	}
	backup.UserTunnels = []model.UserTunnelBackup{
		{ID: 1, UserID: 1, TunnelID: 72, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards", "userTunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForwardsImported != 1 {
		t.Fatalf("ForwardsImported = %d, want 1 (only B), warnings=%v", result.ForwardsImported, result.ImportWarnings)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "claimer-b") || !strings.Contains(warnText, "已被占用") {
		t.Fatalf("receipt must name B's occupied port row skip, warnings=%v", result.ImportWarnings)
	}
	var rows []model.ForwardPort
	if err := dst.db.Where("node_id = ? AND port = ?", 41, 2000).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ForwardID != 9101 {
		t.Fatalf("(node 41, port 2000) rows = %v, want exactly the surviving stale row of forward 9101", rows)
	}
}
