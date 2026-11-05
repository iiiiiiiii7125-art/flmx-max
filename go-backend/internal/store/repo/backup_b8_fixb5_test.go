package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX-B5 回归钉（audit-max B8 簇）：恢复面镜像写面门集。
// 每条一个测试函数，钉住对应条目的门行为与回执披露。

// B8-02：importServiceMonitors 镜像 monitorServiceCreate 全套门：
// type 集外/名空/超长/enabled 域外/负 NodeID/ICMP 无节点逐行跳过
// + warning 点名；type 大小写归一；interval/timeout 按写面同源
// 限值（配置键）默认 + 钳制 + warning。
func TestImportServiceMonitorsMirrorWriteGates(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 41, Name: "n41", Secret: "s", ServerIP: "192.0.2.41", Status: 1, CreatedTime: now},
		},
		ServiceMonitors: []model.ServiceMonitorBackup{
			// type 集外 → 跳过。
			{ID: 1, Name: "bad-type", Type: "http", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, Enabled: 1, CreatedTime: now},
			// 名为空 → 跳过。
			{ID: 2, Name: "  ", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, Enabled: 1, CreatedTime: now},
			// 名超长 → 跳过。
			{ID: 3, Name: strings.Repeat("名", 101), Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, Enabled: 1, CreatedTime: now},
			// enabled 域外 → 跳过。
			{ID: 4, Name: "bad-enabled", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, Enabled: 7, CreatedTime: now},
			// NodeID 负值 → 跳过。
			{ID: 5, Name: "neg-node", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, NodeID: -3, Enabled: 1, CreatedTime: now},
			// ICMP 无节点 → 跳过。
			{ID: 6, Name: "icmp-nolocal", Type: "icmp", Target: "203.0.113.7", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			// 绑定不存在的节点 → 跳过（D4-03 既有门）。
			{ID: 7, Name: "ghost-node", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, NodeID: 999, Enabled: 1, CreatedTime: now},
			// type 大小写归一后落库；名 trim 后落库。
			{ID: 8, Name: "  upper-type  ", Type: "TCP", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, Enabled: 1, CreatedTime: now},
			// interval/timeout 越界 → 默认 + 钳制后落库。
			{ID: 9, Name: "clamp-me", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: -5, TimeoutSec: 99999, Enabled: 1, CreatedTime: now},
			// interval/timeout 为 0 → 取默认后落库。
			{ID: 10, Name: "defaults", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 0, TimeoutSec: 0, Enabled: 0, CreatedTime: now},
			// ICMP 绑节点 → 落库（对照）。
			{ID: 11, Name: "icmp-ok", Type: "icmp", Target: "203.0.113.9", IntervalSec: 60, TimeoutSec: 10, NodeID: 41, Enabled: 1, CreatedTime: now},
		},
	}
	result, err := dst.Import(backup, []string{"nodes", "serviceMonitors"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceMonitorsImported != 4 {
		t.Fatalf("ServiceMonitorsImported = %d, want 4 (ids 8,9,10,11), warnings=%v", result.ServiceMonitorsImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["serviceMonitors"]; got != 7 {
		t.Fatalf("SkippedRows[serviceMonitors] = %d, want 7, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"\"http\"", "enabled=7", "nodeId=-3", "ICMP", "已调整为"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	readMonitor := func(id int64) model.ServiceMonitor {
		t.Helper()
		var m model.ServiceMonitor
		if err := dst.db.Where("id = ?", id).First(&m).Error; err != nil {
			t.Fatalf("read monitor #%d: %v", id, err)
		}
		return m
	}
	if m := readMonitor(8); m.Type != "tcp" || m.Name != "upper-type" {
		t.Fatalf("monitor 8 = %+v, want type tcp / trimmed name", m)
	}
	// 缺省限值：DefaultIntervalSec=1、DefaultTimeoutSec=5、MaxTimeoutSec=60。
	if m := readMonitor(9); m.IntervalSec != 1 || m.TimeoutSec != 60 {
		t.Fatalf("monitor 9 interval/timeout = %d/%d, want 1/60 (default+clamp)", m.IntervalSec, m.TimeoutSec)
	}
	if m := readMonitor(10); m.IntervalSec != 1 || m.TimeoutSec != 5 || m.Enabled != 0 {
		t.Fatalf("monitor 10 = %+v, want interval 1 / timeout 5 / enabled 0", m)
	}
	if m := readMonitor(11); m.Type != "icmp" || m.NodeID != 41 {
		t.Fatalf("monitor 11 = %+v, want icmp on node 41", m)
	}
}

// B8-05：隧道 Type/Status 恢复面枚举门镜像写面 {1,2}/{0,1}，
// 集外逐行跳过 + warning 点名，集内（含 status=0）对照落库。
func TestImportTunnelTypeStatusGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "bad-type", TrafficRatio: 1, Type: 9, Flow: 1, Status: 1, CreatedTime: now},
			{ID: 2, Name: "bad-status", TrafficRatio: 1, Type: 1, Flow: 1, Status: 7, CreatedTime: now},
			{ID: 3, Name: "type-zero", TrafficRatio: 1, Type: 0, Flow: 1, Status: 1, CreatedTime: now},
			{ID: 4, Name: "ok-tunnel", TrafficRatio: 1, Type: 2, Flow: 1, Status: 0, CreatedTime: now},
		},
	}
	result, err := dst.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TunnelsImported != 1 {
		t.Fatalf("TunnelsImported = %d, want 1 (only id 4), warnings=%v", result.TunnelsImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["tunnels"]; got != 3 {
		t.Fatalf("SkippedRows[tunnels] = %d, want 3, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"type=9", "status=7", "type=0"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	var tun model.Tunnel
	if err := dst.db.Where("id = ?", 4).First(&tun).Error; err != nil {
		t.Fatal(err)
	}
	if tun.Type != 2 || tun.Status != 0 {
		t.Fatalf("tunnel 4 type/status = %d/%d, want 2/0", tun.Type, tun.Status)
	}
}

func intPtr(v int) *int { return &v }

// B4-02 残 A：转发面顺序无关——声明行 B 排在被跳行 A **之前**时，
// A 的存量端口行仍须计占用（旧实现按「尚未处理」排除，B 先落库、
// A 后跳过 → 同节点同口双行）。与 backup_r1_fxb_gates_test.go 的
// TestImportForwardPortOccupancyHoleShape（A 在前）成对。
func TestImportForwardPortOccupancyReversedOrder(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

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
			// B 在前：合法转发，声明同一 (节点, 端口)。
			{ID: 9202, UserID: 1, TunnelID: 72, Name: "claimer-b", RemoteAddr: "203.0.113.11:80", Strategy: "fifo", Status: 1, CreatedTime: now, ForwardPorts: fpSlice(
				model.ForwardPortBackup{NodeID: 41, Port: 2000},
			)},
			// A 在后：策略非法被跳过，ForwardPorts 为 nil，存量行存活。
			{ID: 9101, UserID: 1, TunnelID: 72, Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "banana", Status: 1, CreatedTime: now},
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

// B4-02 残 B（链路面）：被跳转发的存量端口行存活时，链路声明同
// 节点同口不得落库——链路面此前无行内复检，预检排除集又按备份
// ID 全集组装而放行。
func TestImportChainPortRecheckAgainstSkippedForward(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	if err := dst.db.Create(&model.Forward{ID: 9101, UserID: 1, UserName: "admin@user", Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.ForwardPort{ForwardID: 9101, NodeID: 41, Port: 2002}).Error; err != nil {
		t.Fatal(err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 41, Name: "n41", Secret: "s", ServerIP: "192.0.2.41", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 21, Name: "chain-t", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
				{ID: 1, TunnelID: 21, ChainType: "1", NodeID: 41, Port: intPtr(2002), Strategy: strPtr("fifo")},
				{ID: 2, TunnelID: 21, ChainType: "3", NodeID: 41, Port: intPtr(2003), Strategy: strPtr("round")},
			}},
		},
		Forwards: []model.ForwardBackup{
			{ID: 9101, UserID: 1, TunnelID: 21, Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "banana", Status: 1, CreatedTime: now},
		},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards"})
	if err != nil {
		t.Fatal(err)
	}
	if result.TunnelsImported != 1 {
		t.Fatalf("TunnelsImported = %d, want 1, warnings=%v", result.TunnelsImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["chainTunnels"]; got != 1 {
		t.Fatalf("SkippedRows[chainTunnels] = %d, want 1, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "2002") || !strings.Contains(warnText, "已被占用") {
		t.Fatalf("receipt must name the occupied chain port skip, warnings=%v", result.ImportWarnings)
	}
	var chains []model.ChainTunnel
	if err := dst.db.Where("tunnel_id = ?", 21).Find(&chains).Error; err != nil {
		t.Fatal(err)
	}
	if len(chains) != 1 || chains[0].ID != 2 {
		t.Fatalf("restored chains = %v, want exactly chain #2 (port 2003)", chains)
	}
}

// B4-02 残 B（入站面）：被跳转发的存量端口行存活时，入站声明同
// 节点同口不得落库——入站面此前无行内复检。
func TestImportInboundPortRecheckAgainstSkippedForward(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	if err := dst.db.Create(&model.Forward{ID: 9101, UserID: 1, UserName: "admin@user", Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := dst.db.Create(&model.ForwardPort{ForwardID: 9101, NodeID: 41, Port: 2004}).Error; err != nil {
		t.Fatal(err)
	}

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 41, Name: "n41", Secret: "s", ServerIP: "192.0.2.41", Status: 1, CreatedTime: now},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 72, Name: "t72", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			{ID: 9101, UserID: 1, TunnelID: 72, Name: "stale-a", RemoteAddr: "203.0.113.10:80", Strategy: "banana", Status: 1, CreatedTime: now},
		},
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 41, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 30, KernelID: 1, NodeID: 41, Name: "collide", Protocol: "vless", Port: 2004, Enabled: 1, CreatedTime: now,
					Settings: `{"uuid":"11111111-2222-3333-4444-555555555555"}`, Security: `{"type":"none"}`},
				{ID: 31, KernelID: 1, NodeID: 41, Name: "free-port", Protocol: "vless", Port: 2005, Enabled: 1, CreatedTime: now,
					Settings: `{"uuid":"11111111-2222-3333-4444-555555555555"}`, Security: `{"type":"none"}`},
			},
		},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards", "inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if result.InboundsImported != 1 {
		t.Fatalf("InboundsImported = %d, want 1 (only the free-port row), warnings=%v", result.InboundsImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["inbounds"]; got != 1 {
		t.Fatalf("SkippedRows[inbounds] = %d, want 1, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "collide") || !strings.Contains(warnText, "已被已有转发占用") {
		t.Fatalf("receipt must name the colliding inbound and its occupant, warnings=%v", result.ImportWarnings)
	}
	if n := countWhere(t, dst, "node_inbound", "node_id = ? AND port = ?", 41, 2004); n != 0 {
		t.Fatalf("colliding inbound must not land, got %d rows on (41, 2004)", n)
	}
}

// B8-06 残面：MaxConn/IPMaxConn 负值恢复时钳 0 + warning，正值
// 对照逐字落库。
func TestImportForwardMaxConnClamp(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 31, Name: "n31", Secret: "s", ServerIP: "192.0.2.31", Status: 1},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 71, Name: "t71", TrafficRatio: 1, Type: 1, Flow: 1, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			{ID: 61, UserID: 1, TunnelID: 71, Name: "neg-conn", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now, MaxConn: -5, IPMaxConn: -2},
			{ID: 62, UserID: 1, TunnelID: 71, Name: "pos-conn", RemoteAddr: "203.0.113.10:80", Strategy: "fifo", Status: 1, CreatedTime: now, MaxConn: 10, IPMaxConn: 3},
		},
	}
	backup.UserTunnels = []model.UserTunnelBackup{
		{ID: 1, UserID: 1, TunnelID: 71, Num: 99999, Flow: 99999, FlowResetTime: 1, ExpTime: 2727251700000, Status: 1},
	}
	result, err := dst.Import(backup, []string{"nodes", "tunnels", "forwards", "userTunnels"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ForwardsImported != 2 {
		t.Fatalf("ForwardsImported = %d, want 2, warnings=%v", result.ForwardsImported, result.ImportWarnings)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	if !strings.Contains(warnText, "neg-conn") || !strings.Contains(warnText, "-5/-2") {
		t.Fatalf("receipt must name the clamped row and values, warnings=%v", result.ImportWarnings)
	}
	readForward := func(id int64) model.Forward {
		t.Helper()
		var f model.Forward
		if err := dst.db.Where("id = ?", id).First(&f).Error; err != nil {
			t.Fatalf("read forward #%d: %v", id, err)
		}
		return f
	}
	if f := readForward(61); f.MaxConn != 0 || f.IPMaxConn != 0 {
		t.Fatalf("forward 61 maxConn/ipMaxConn = %d/%d, want 0/0 (clamped)", f.MaxConn, f.IPMaxConn)
	}
	if f := readForward(62); f.MaxConn != 10 || f.IPMaxConn != 3 {
		t.Fatalf("forward 62 maxConn/ipMaxConn = %d/%d, want 10/3 (verbatim)", f.MaxConn, f.IPMaxConn)
	}
}

// B8-08：订阅源恢复面镜像写面 名/间隔/角色 门——名空/超长跳行、
// 间隔集外跳行、role 集外归一、文件源 URL/间隔强制清零。
func TestImportRemoteSourceMirrorGates(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Sources: []model.RemoteSourceBackup{
				{ID: 1, Name: "  ", URL: "https://a.example/sub", IntervalHours: 24, Enabled: 1, CreatedTime: now},
				{ID: 2, Name: strings.Repeat("长", 101), URL: "https://a.example/sub", IntervalHours: 24, Enabled: 1, CreatedTime: now},
				{ID: 3, Name: "neg-interval", URL: "https://a.example/sub", IntervalHours: -1, Enabled: 1, CreatedTime: now},
				{ID: 4, Name: "huge-interval", URL: "https://a.example/sub", IntervalHours: 999999, Enabled: 1, CreatedTime: now},
				{ID: 5, Name: "bad-role", URL: "https://a.example/sub", IntervalHours: 24, Role: "bogus", Enabled: 1, CreatedTime: now},
				{ID: 6, Name: "relay-role", URL: "https://a.example/sub", IntervalHours: 24, Role: "relay", Enabled: 1, CreatedTime: now},
				{ID: 7, Name: "file-src", Kind: "file", URL: "https://x.example/ignored", IntervalHours: 55, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if result.InboundSourcesImported != 3 {
		t.Fatalf("InboundSourcesImported = %d, want 3 (ids 5, 6, 7), warnings=%v", result.InboundSourcesImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["sources"]; got != 4 {
		t.Fatalf("SkippedRows[sources] = %d, want 4, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"订阅源 #4", "999999 小时", "bogus"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	readSource := func(id int64) model.RemoteSource {
		t.Helper()
		var s model.RemoteSource
		if err := dst.db.Where("id = ?", id).First(&s).Error; err != nil {
			t.Fatalf("read source #%d: %v", id, err)
		}
		return s
	}
	if s := readSource(5); s.Role != "" {
		t.Fatalf("source 5 role = %q, want \"\" (normalized)", s.Role)
	}
	if s := readSource(6); s.Role != "relay" {
		t.Fatalf("source 6 role = %q, want relay (verbatim)", s.Role)
	}
	if s := readSource(7); s.URL != "" || s.IntervalHours != 0 {
		t.Fatalf("file source 7 url/interval = %q/%d, want \"\"/0 (write-face clearing)", s.URL, s.IntervalHours)
	}
}

// B8-09：订阅模板恢复面镜像写面门——名/kind/builder 与网址同设/
// 网址形态/CheckTemplate 逐行跳过 + 点名，builder 行按写面同路
// 生成内容落库。
func TestImportSubscriptionTemplateMirrorGates(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	validClash := "proxy-groups:\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"
	builderJSON := `{"version":1,"preset":"custom","final":"节点选择","groups":[{"name":"美国优选","type":"url-test","filter":"(?i)美国|US"}],"rules":[{"type":"domain_suffix","value":"example.com","target":"DIRECT"}]}`
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Templates: []model.SubscriptionTemplateBackup{
				{ID: 1, Name: " ", Kind: "clash", Content: validClash, CreatedTime: now},
				{ID: 2, Name: "bad-kind", Kind: "quantum", Content: validClash, CreatedTime: now},
				{ID: 3, Name: "bad-content", Kind: "singbox", Content: "{not json", CreatedTime: now},
				{ID: 4, Name: "both-set", Kind: "clash", Content: validClash, BuilderConfig: builderJSON, SourceURL: "https://x.example/tpl", CreatedTime: now},
				{ID: 5, Name: "bad-url", Kind: "clash", Content: validClash, SourceURL: "ftp://x.example/tpl", CreatedTime: now},
				{ID: 6, Name: "builder-ok", Kind: "clash", Content: "stale content to be replaced", BuilderConfig: builderJSON, CreatedTime: now},
				{ID: 7, Name: "plain-ok", Kind: "clash", Content: validClash, CreatedTime: now},
				{ID: 8, Name: "url-ok", Kind: "clash", Content: validClash, SourceURL: "https://x.example/tpl", CreatedTime: now},
			},
		},
	}
	result, err := dst.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatal(err)
	}
	if result.InboundTemplatesImported != 3 {
		t.Fatalf("InboundTemplatesImported = %d, want 3 (ids 6, 7, 8), warnings=%v", result.InboundTemplatesImported, result.ImportWarnings)
	}
	if got := result.SkippedRows["templates"]; got != 5 {
		t.Fatalf("SkippedRows[templates] = %d, want 5, skipped=%v", got, result.SkippedRows)
	}
	warnText := strings.Join(result.ImportWarnings, "\n")
	for _, want := range []string{"quantum", "both-set", "bad-url", "bad-content"} {
		if !strings.Contains(warnText, want) {
			t.Fatalf("receipt must name %q, warnings=%v", want, result.ImportWarnings)
		}
	}
	readTemplate := func(id int64) model.SubscriptionTemplate {
		t.Helper()
		var tp model.SubscriptionTemplate
		if err := dst.db.Where("id = ?", id).First(&tp).Error; err != nil {
			t.Fatalf("read template #%d: %v", id, err)
		}
		return tp
	}
	if tp := readTemplate(6); tp.Content == "stale content to be replaced" || !strings.Contains(tp.Content, "美国优选") {
		t.Fatalf("builder template content must be regenerated by the write-face path, got %q", tp.Content)
	}
	if tp := readTemplate(7); tp.Content != validClash {
		t.Fatalf("plain template content must restore verbatim, got %q", tp.Content)
	}
	if tp := readTemplate(8); tp.SourceURL != "https://x.example/tpl" {
		t.Fatalf("url template sourceUrl = %q, want kept", tp.SourceURL)
	}
}

// B8-02（限值同源）：限值经 service_monitor_* 配置键调整后，恢复面
// 钳制跟随配置口径，与写面 resolveServiceMonitorLimits 同源。
func TestImportServiceMonitorsLimitsFromConfig(t *testing.T) {
	now := time.Now().UnixMilli()
	dst, err := Open(filepath.Join(t.TempDir(), "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dst.Close() }()

	for name, value := range map[string]string{
		"service_monitor_default_interval_sec": "30",
		"service_monitor_max_timeout_sec":      "45",
	} {
		if err := dst.db.Create(&model.ViteConfig{Name: name, Value: value, Time: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	backup := &model.BackupData{
		Version: "1.0",
		ServiceMonitors: []model.ServiceMonitorBackup{
			{ID: 1, Name: "cfg-limits", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 0, TimeoutSec: 99999, Enabled: 1, CreatedTime: now},
		},
	}
	result, err := dst.Import(backup, []string{"serviceMonitors"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceMonitorsImported != 1 {
		t.Fatalf("ServiceMonitorsImported = %d, want 1, warnings=%v", result.ServiceMonitorsImported, result.ImportWarnings)
	}
	var m model.ServiceMonitor
	if err := dst.db.Where("id = ?", 1).First(&m).Error; err != nil {
		t.Fatal(err)
	}
	if m.IntervalSec != 30 || m.TimeoutSec != 45 {
		t.Fatalf("interval/timeout = %d/%d, want 30/45 (config-sourced limits)", m.IntervalSec, m.TimeoutSec)
	}
}
