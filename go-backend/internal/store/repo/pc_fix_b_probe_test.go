package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// PORT-CLOSURE 封口修复批 Lane B（基线 main @ c6e90c7）：备份恢复
// intake（repository.go）先红后绿探针。
//
//   - PC-02（P2，F-02）：importNodes TCP/UDPListenAddr 原值落库。
//   - PC-07(ii)（P3，F-07）：importNodes ServerIP/V4/V6 单次剥离后
//     无格式校验，双端口值穿透。
//   - PC-07(iii)（P3，F-07）：importTunnels InIP 双端口项穿透
//     （R32 F-02 在本基线尚未合入，此处按单次剥离 + 校验修）。
//   - PC-08（P3，F-08）：importServiceMonitors Target 原值落库。
//   - B-NEW-01（观察级）：importNodes ExtraIPs 原值落库，顺手剥离。
//
// 凭据 hygiene：本探针只断言地址/监听/目标字段值与回执披露，绝不
// 打印备份载荷（含节点 secret）。

func readStoredNodeField(t *testing.T, dest *Repository, id int64, column string) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := dest.DB().Raw("SELECT "+column+" FROM node WHERE id = ?", id).Scan(&v).Error; err != nil {
		t.Fatalf("read node #%d %s: %v", id, column, err)
	}
	return v
}

func warningsContain(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// PC-02：监听地址含单端口须剥离后入库；"[::]" 默认原样保留；
// ledger RED 实例双端口 "203.0.113.7:9999:8888" 剥离为 no-op，
// 须回落默认 "[::]" 并回执披露（单次剥离本身闭合不了该实例）。
func TestPortClosureBPC02RestoreListenAddrStripped(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "pc-b-pc02.db")
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 1, Name: "pc02-n1", Secret: "s1", ServerIP: "203.0.113.7:6366", Status: 1, CreatedTime: now, TCPListenAddr: "203.0.113.7:9999", UDPListenAddr: "203.0.113.7:8888"},
			{ID: 2, Name: "pc02-n2", Secret: "s2", ServerIP: "203.0.113.7", Status: 1, CreatedTime: now, TCPListenAddr: "[::]", UDPListenAddr: "[::]"},
			{ID: 3, Name: "pc02-n3", Secret: "s3", ServerIP: "203.0.113.7", Status: 1, CreatedTime: now, TCPListenAddr: "203.0.113.7:9999:8888", UDPListenAddr: "198.51.100.1:7777"},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.NodesImported != 3 {
		t.Fatalf("NodesImported = %d, want 3", result.NodesImported)
	}

	// CELL-N1：单端口监听地址剥离；同备份 serverIp 剥离为对照。
	if got := readStoredNodeField(t, dest, 1, "tcp_listen_addr"); got.String != "203.0.113.7" {
		t.Errorf("CELL-N1 tcp_listen_addr = %q, want %q", got.String, "203.0.113.7")
	}
	if got := readStoredNodeField(t, dest, 1, "udp_listen_addr"); got.String != "203.0.113.7" {
		t.Errorf("CELL-N1 udp_listen_addr = %q, want %q", got.String, "203.0.113.7")
	}
	if got := readStoredNodeField(t, dest, 1, "server_ip"); got.String != "203.0.113.7" {
		t.Errorf("CELL-N1 server_ip (control) = %q, want %q", got.String, "203.0.113.7")
	}
	// CELL-N2："[::]" 默认原样保留（stripIngressHostPort 会改写为 "::"，此处不许）。
	if got := readStoredNodeField(t, dest, 2, "tcp_listen_addr"); got.String != "[::]" {
		t.Errorf("CELL-N2 tcp_listen_addr = %q, want %q ([::] default must pass through)", got.String, "[::]")
	}
	if got := readStoredNodeField(t, dest, 2, "udp_listen_addr"); got.String != "[::]" {
		t.Errorf("CELL-N2 udp_listen_addr = %q, want %q", got.String, "[::]")
	}
	// CELL-N3：双端口监听地址（ledger RED 实例）回落默认 + 回执披露。
	if got := readStoredNodeField(t, dest, 3, "tcp_listen_addr"); got.String != "[::]" {
		t.Errorf("CELL-N3 tcp_listen_addr = %q, want %q (double-port must fall back to default)", got.String, "[::]")
	}
	if got := readStoredNodeField(t, dest, 3, "udp_listen_addr"); got.String != "198.51.100.1" {
		t.Errorf("CELL-N3 udp_listen_addr = %q, want %q", got.String, "198.51.100.1")
	}
	if !warningsContain(result.ImportWarnings, "节点 #3") {
		t.Errorf("CELL-N3 no receipt disclosure for node #3 listen addr, warnings = %v", result.ImportWarnings)
	}
}

// PC-07(ii)：备份恢复含双端口 ServerIP/V4 的节点后非法值清空 +
// 回执披露；单端口对照剥离；括号 v6 对照剥离后保留。
func TestPortClosureBPC07RestoreNodeDoublePortCleared(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "pc-b-pc07ii.db")
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 1, Name: "pc07-n1", Secret: "s1", ServerIP: "203.0.113.7:9999:8888", ServerIPv4: "198.51.100.5:9999:8888", ServerIPv6: "[2001:db8::1]:443", Status: 1, CreatedTime: now},
			{ID: 2, Name: "pc07-n2", Secret: "s2", ServerIP: "203.0.113.7:6366", Status: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.NodesImported != 2 {
		t.Fatalf("NodesImported = %d, want 2", result.NodesImported)
	}

	// CELL-S1：双端口 ServerIP 清空。
	if got := readStoredNodeField(t, dest, 1, "server_ip"); got.String != "" {
		t.Errorf("CELL-S1 server_ip = %q, want %q (double-port must be cleared)", got.String, "")
	}
	// CELL-S2：双端口 ServerIPv4 清空（NULL）。
	if got := readStoredNodeField(t, dest, 1, "server_ip_v4"); got.Valid && got.String != "" {
		t.Errorf("CELL-S2 server_ip_v4 = %q (valid=%v), want cleared/NULL", got.String, got.Valid)
	}
	// CELL-S3：括号 v6 单端口剥离后保留。
	if got := readStoredNodeField(t, dest, 1, "server_ip_v6"); got.String != "2001:db8::1" {
		t.Errorf("CELL-S3 server_ip_v6 = %q, want %q", got.String, "2001:db8::1")
	}
	// CELL-S4：单端口 ServerIP 对照剥离（既有行为）。
	if got := readStoredNodeField(t, dest, 2, "server_ip"); got.String != "203.0.113.7" {
		t.Errorf("CELL-S4 server_ip (control) = %q, want %q", got.String, "203.0.113.7")
	}
	if !warningsContain(result.ImportWarnings, "节点 #1") {
		t.Errorf("CELL-S1/S2 no receipt disclosure for node #1, warnings = %v", result.ImportWarnings)
	}
}

// PC-07(iii)：importTunnels 双端口 in_ip 项穿透。单值双端口整
// 项丢弃（置 NULL）；多值列表仅丢非法项；单端口对照剥离。
func TestPortClosureBPC07RestoreTunnelDoublePortCleared(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "pc-b-pc07iii.db")
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "pc07-t1", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, InIP: "203.0.113.7:9999:8888"},
			{ID: 2, Name: "pc07-t2", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, InIP: "198.51.100.77:8443,203.0.113.7:9999:8888,2001:db8::1"},
			{ID: 3, Name: "pc07-t3", Flow: 1, TrafficRatio: 1, Type: 1, Status: 1, CreatedTime: now, InIP: "203.0.113.7:6366"},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.TunnelsImported != 3 {
		t.Fatalf("TunnelsImported = %d, want 3", result.TunnelsImported)
	}
	readInIP := func(id int64) sql.NullString {
		t.Helper()
		var v sql.NullString
		if err := dest.DB().Raw("SELECT in_ip FROM tunnel WHERE id = ?", id).Scan(&v).Error; err != nil {
			t.Fatalf("read tunnel #%d in_ip: %v", id, err)
		}
		return v
	}
	// CELL-T1：单值双端口 in_ip 丢弃（置 NULL）。
	if got := readInIP(1); got.Valid && got.String != "" {
		t.Errorf("CELL-T1 in_ip = %q (valid=%v), want cleared/NULL", got.String, got.Valid)
	}
	// CELL-T2：多值列表仅丢非法项，其余逐项剥离保留。
	if got := readInIP(2); got.String != "198.51.100.77,2001:db8::1" {
		t.Errorf("CELL-T2 in_ip = %q, want %q", got.String, "198.51.100.77,2001:db8::1")
	}
	// CELL-T3：单端口对照剥离（R32 F-02 行为）。
	if got := readInIP(3); got.String != "203.0.113.7" {
		t.Errorf("CELL-T3 in_ip (control) = %q, want %q", got.String, "203.0.113.7")
	}
	if !warningsContain(result.ImportWarnings, "隧道 #1") || !warningsContain(result.ImportWarnings, "隧道 #2") {
		t.Errorf("CELL-T1/T2 no receipt disclosure for tunnels #1/#2, warnings = %v", result.ImportWarnings)
	}
}

// PC-08：含端口 Target 的监控项导入后拒绝/清空 + 回执披露；
// 合法 tcp/ icmp 对照保留。
func TestPortClosureBPC08RestoreMonitorTargetCleared(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "pc-b-pc08.db")
	backup := &model.BackupData{
		Version: "1.0",
		ServiceMonitors: []model.ServiceMonitorBackup{
			{ID: 1, Name: "pc08-m1", Type: "tcp", Target: "203.0.113.7:80:90", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 2, Name: "pc08-m2", Type: "tcp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 3, Name: "pc08-m3", Type: "icmp", Target: "203.0.113.7:80", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 4, Name: "pc08-m4", Type: "icmp", Target: "203.0.113.7", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 5, Name: "pc08-m5", Type: "icmp", Target: "[2001:db8::1]", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 6, Name: "pc08-m6", Type: "icmp", Target: "2001:db8::1", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
			{ID: 7, Name: "pc08-m7", Type: "tcp", Target: "203.0.113.7", IntervalSec: 60, TimeoutSec: 10, NodeID: 0, Enabled: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"serviceMonitors"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ServiceMonitorsImported != 7 {
		t.Fatalf("ServiceMonitorsImported = %d, want 7", result.ServiceMonitorsImported)
	}
	readTarget := func(id int64) string {
		t.Helper()
		var v string
		if err := dest.DB().Raw("SELECT target FROM service_monitor WHERE id = ?", id).Scan(&v).Error; err != nil {
			t.Fatalf("read monitor #%d target: %v", id, err)
		}
		return v
	}
	// CELL-M1：tcp 双端口 Target 清空。
	if got := readTarget(1); got != "" {
		t.Errorf("CELL-M1 target = %q, want %q (tcp double-port must be cleared)", got, "")
	}
	// CELL-M2：合法 tcp host:port 对照保留。
	if got := readTarget(2); got != "203.0.113.7:80" {
		t.Errorf("CELL-M2 target (control) = %q, want %q", got, "203.0.113.7:80")
	}
	// CELL-M3：icmp 含端口 Target 清空（PC-09 口径）。
	if got := readTarget(3); got != "" {
		t.Errorf("CELL-M3 target = %q, want %q (icmp host:port must be cleared)", got, "")
	}
	// CELL-M4：合法 icmp 裸主机对照保留。
	if got := readTarget(4); got != "203.0.113.7" {
		t.Errorf("CELL-M4 target (control) = %q, want %q", got, "203.0.113.7")
	}
	// CELL-M5：icmp 括号 IPv6 对照保留。
	if got := readTarget(5); got != "[2001:db8::1]" {
		t.Errorf("CELL-M5 target (control) = %q, want %q", got, "[2001:db8::1]")
	}
	// CELL-M6：icmp 裸 IPv6 对照保留（合法 IP 字面量）。
	if got := readTarget(6); got != "2001:db8::1" {
		t.Errorf("CELL-M6 target (control) = %q, want %q", got, "2001:db8::1")
	}
	// CELL-M7：tcp 无端口 Target 清空（tcp 须为 host:port）。
	if got := readTarget(7); got != "" {
		t.Errorf("CELL-M7 target = %q, want %q (tcp without port must be cleared)", got, "")
	}
	if !warningsContain(result.ImportWarnings, "监控 #1") || !warningsContain(result.ImportWarnings, "监控 #3") || !warningsContain(result.ImportWarnings, "监控 #7") {
		t.Errorf("CELL-M1/M3/M7 no receipt disclosure, warnings = %v", result.ImportWarnings)
	}
}

// B-NEW-01（观察级，顺手收口）：importNodes ExtraIPs 逐项剥离。
func TestPortClosureBBNEW01RestoreExtraIPsStripped(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "pc-b-bnew01.db")
	backup := &model.BackupData{
		Version: "1.0",
		Nodes: []model.NodeBackup{
			{ID: 1, Name: "bnew01-n1", Secret: "s1", ServerIP: "203.0.113.7", ExtraIPs: "203.0.113.7:9999,198.51.100.9,2001:db8::1", Status: 1, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.NodesImported != 1 {
		t.Fatalf("NodesImported = %d, want 1", result.NodesImported)
	}
	if got := readStoredNodeField(t, dest, 1, "extra_ips"); got.String != "203.0.113.7,198.51.100.9,2001:db8::1" {
		t.Errorf("extra_ips = %q, want %q", got.String, "203.0.113.7,198.51.100.9,2001:db8::1")
	}
}
