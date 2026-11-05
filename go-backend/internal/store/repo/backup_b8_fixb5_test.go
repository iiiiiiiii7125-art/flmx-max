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
