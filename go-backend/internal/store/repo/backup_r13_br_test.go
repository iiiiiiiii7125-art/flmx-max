package repo

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R13 批 R · 先红后绿测试（施工前应全部失败，修复后全部通过）。
// R-b（R13X-004）：inbounds 子段导入计数与 DB 落库数一致。
// R-c（R13X-005）：ExportPartial / Import 对未知类型报错并点名。
// R-d（R13X-006）：导入型入站行以 ProxyProtocol(Extra) 重推导 Protocol。

func TestR13BRInboundSubsectionCounts(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r13br.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status) VALUES(10, 'n1', 's', '203.0.113.1', 0, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, status, created_time, updated_time) VALUES(50, 1, 'admin', 'fwd', 0, '203.0.113.1:443', 'fifo', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	backup := &model.BackupData{Version: "1.0", Inbounds: &model.InboundsBackup{
		Kernels: []model.KernelBackup{
			{ID: 11, NodeID: 10, KernelType: "singbox", Version: "latest", Enabled: 1, CreatedTime: now},
			{ID: 12, NodeID: 10, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
		},
		Inbounds: []model.InboundBackup{
			{ID: 201, KernelID: 11, NodeID: 10, Name: "in-a", Protocol: "vless", Listen: "::", Port: 8443, Enabled: 1, CreatedTime: now},
			{ID: 202, KernelID: 12, NodeID: 10, Name: "in-b", Protocol: "vless", Listen: "::", Port: 8444, Enabled: 1, CreatedTime: now},
		},
		Relays: []model.InboundRelayBackup{
			{ID: 301, InboundID: 201, ForwardID: 50, Name: "relay-a", Managed: 1, CreatedTime: now},
		},
		Counters: []model.InboundCounterBackup{
			{InboundID: 201, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 500},
		},
		Subscriptions: []model.SubscriptionBackup{
			{ID: 101, Name: "sub-a", Token: "tok-a", UUID: "uuid-a", Password: "pw-a", Enabled: 1, CreatedTime: now},
			{ID: 102, Name: "sub-b", Token: "tok-b", UUID: "uuid-b", Password: "pw-b", Enabled: 1, CreatedTime: now},
		},
		SubscriptionCounters: []model.SubscriptionCounterBackup{
			{NodeID: 10, SubscriptionID: 101, Direction: "up", Epoch: "e1", Bytes: 100},
		},
		Sources: []model.RemoteSourceBackup{
			{ID: 401, Name: "src-a", URL: "https://example.invalid/sub", IntervalHours: 24, Enabled: 1, CreatedTime: now},
		},
		Users: []model.SubUserBackup{
			{Name: "legacy-a", Token: "tok-legacy", UUID: "uuid-legacy", Password: "pw-legacy", Enabled: 1, CreatedTime: now},
		},
	}}
	result, err := r.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import inbounds: %v", err)
	}
	type check struct {
		name string
		got  int
		want int
	}
	for _, c := range []check{
		{"inboundsImported", result.InboundsImported, 2},
		{"inboundKernelsImported", result.InboundKernelsImported, 2},
		{"inboundRelaysImported", result.InboundRelaysImported, 1},
		{"inboundCountersImported", result.InboundCountersImported, 1},
		{"inboundSubscriptionsImported", result.InboundSubscriptionsImported, 2},
		{"inboundSubscriptionCountersImported", result.InboundSubscriptionCountersImported, 1},
		{"inboundSourcesImported", result.InboundSourcesImported, 1},
		{"inboundLegacyUsersImported", result.InboundLegacyUsersImported, 1},
	} {
		if c.got != c.want {
			t.Errorf("R13X-004: %s = %d, want %d", c.name, c.got, c.want)
		}
	}
	// 回执计数与 DB 落库数逐段核对。
	dbChecks := []struct {
		name  string
		query string
		want  int
	}{
		{"node_kernel", `SELECT COUNT(*) FROM node_kernel`, 2},
		{"node_inbound", `SELECT COUNT(*) FROM node_inbound WHERE id IN (201, 202)`, 2},
		{"node_inbound_relay", `SELECT COUNT(*) FROM node_inbound_relay`, 1},
		{"node_inbound_counter", `SELECT COUNT(*) FROM node_inbound_counter`, 1},
		// 新库自带 1 条种子订阅（「我的订阅」），故只计本次导入的 token。
		{"subscription", `SELECT COUNT(*) FROM subscription WHERE token IN ('tok-a', 'tok-b', 'tok-legacy')`, 3},
		{"subscription_counter", `SELECT COUNT(*) FROM subscription_counter`, 1},
		{"remote_source", `SELECT COUNT(*) FROM remote_source`, 1},
	}
	for _, c := range dbChecks {
		var n int
		if err := r.DB().Raw(c.query).Scan(&n).Error; err != nil {
			t.Fatalf("count %s: %v", c.name, err)
		}
		if n != c.want {
			t.Errorf("R13X-004: DB %s = %d, want %d", c.name, n, c.want)
		}
	}
}

func TestR13BRUnknownBackupTypeRejected(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r13br-types.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	if _, err := r.ExportPartial([]string{"users", "bogus_type", "inbound"}); err == nil || !strings.Contains(err.Error(), "bogus_type") {
		t.Fatalf("R13X-005: ExportPartial mixed unknown type must error naming bogus_type, got err=%v", err)
	}
	if _, err := r.Import(&model.BackupData{Version: "1.0"}, []string{"bogus_only"}); err == nil || !strings.Contains(err.Error(), "bogus_only") {
		t.Fatalf("R13X-005: Import unknown-only type must error naming bogus_only, got err=%v", err)
	}
	// 合法类型清单行为不变：16 值全量导出/导入均成功。
	legal := []string{"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits", "tunnelGroups", "userGroups", "permissions", "configs", "inbounds", "userInbounds", "announcements", "monitorPermissions", "userSubscriptions", "serviceMonitors"}
	if _, err := r.ExportPartial(legal); err != nil {
		t.Fatalf("R13X-005: legal 16-type export must succeed, got %v", err)
	}
	if _, err := r.Import(&model.BackupData{Version: "1.0"}, legal); err != nil {
		t.Fatalf("R13X-005: legal 16-type import of empty backup must succeed, got %v", err)
	}
}

func TestR13BRImportedInboundProtocolRederived(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "r13br-proto.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	now := time.Now().UnixMilli()
	// 对照：自建入站行（kernel_id>0）不受重推导门影响，Protocol 原样落库。
	if err := r.DB().Exec(`INSERT INTO node_kernel(id, node_id, kernel_type, version, status, enabled, created_time, updated_time) VALUES(60, 0, 'singbox', 'latest', 'pending', 1, ?, ?)`, now, now).Error; err != nil {
		t.Fatalf("insert kernel: %v", err)
	}
	ssExtra := `{"type":"ss","server":"203.0.113.5","port":8388,"cipher":"aes-256-gcm","password":"secret123"}`
	backup := &model.BackupData{Version: "1.0", Inbounds: &model.InboundsBackup{
		Inbounds: []model.InboundBackup{
			{ID: 501, KernelID: 0, NodeID: 0, Name: "imported-ss", Protocol: "trojan", Listen: "203.0.113.5", Port: 8388, Extra: ssExtra, Enabled: 1, CreatedTime: now},
			{ID: 502, KernelID: 60, NodeID: 0, Name: "selfbuilt", Protocol: "trojan", Listen: "::", Port: 8443, Enabled: 1, CreatedTime: now},
		},
	}}
	if _, err := r.Import(backup, []string{"inbounds"}); err != nil {
		t.Fatalf("import inbounds: %v", err)
	}
	var protocol, extra string
	if err := r.DB().Raw(`SELECT protocol, extra FROM node_inbound WHERE id = 501`).Row().Scan(&protocol, &extra); err != nil {
		t.Fatalf("read inbound: %v", err)
	}
	// ProxyProtocol(ss Extra) = 面板协议名 "shadowsocks"（importTypes 口径，
	// 与 externalRow 创建路径同值；任务书「为 ss」即此协议）。
	if protocol != "shadowsocks" {
		t.Fatalf("R13X-006: imported row protocol = %q, want %q (ProxyProtocol of ss Extra)", protocol, "shadowsocks")
	}
	if extra != ssExtra {
		t.Fatalf("R13X-006: Extra must stay verbatim (data plane follows Extra), got %q", extra)
	}
	var selfProtocol string
	if err := r.DB().Raw(`SELECT protocol FROM node_inbound WHERE id = 502`).Row().Scan(&selfProtocol); err != nil {
		t.Fatalf("read selfbuilt inbound: %v", err)
	}
	if selfProtocol != "trojan" {
		t.Fatalf("R13X-006: kernel-bound row must keep its Protocol verbatim, got %q", selfProtocol)
	}
}
