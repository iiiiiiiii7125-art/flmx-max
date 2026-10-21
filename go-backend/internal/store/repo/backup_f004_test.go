package repo

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// F-004 (B4): user_subscription（1.0.12 订阅分配）整表未进备份，恢复后
// 全部分配关系丢失。变体同批：F-030 service_monitor 定义未备份、
// F-027 NodeBackup.ExtraIPs 导出/导入双漏。
//
// 这些用例走 JSON 序列化边界（备份文件的真实形态），不依赖新字段的
// Go 类型，保证修复前即可运行并以行为失败呈现。

var allBackupTypes = []string{
	"users", "nodes", "tunnels", "forwards", "userTunnels", "speedLimits",
	"tunnelGroups", "userGroups", "permissions", "configs", "inbounds",
	"userInbounds", "announcements", "monitorPermissions",
	"userSubscriptions", "serviceMonitors",
}

func openBackupRepo(t *testing.T, name string) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// roundTripBackup exports from source, passes the payload through a JSON
// marshal/unmarshal cycle (the on-disk backup form), and imports it into
// dest with the full type list.
func roundTripBackup(t *testing.T, source, dest *Repository) *model.ImportResult {
	t.Helper()
	backup, err := source.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal backup: %v", err)
	}
	var decoded model.BackupData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal backup: %v", err)
	}
	result, err := dest.Import(&decoded, allBackupTypes)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func TestBackupRoundTripsUserSubscriptions(t *testing.T) {
	source := openBackupRepo(t, "src.db")
	now := time.Now().UnixMilli()
	if err := source.DB().Exec(`
		INSERT INTO subscription(id, name, kind, token, uuid, password, source_all, inbound_ids, source_ids, options, expire_time, enabled, remark, inx, created_time, updated_time)
		VALUES(10, '机场A', 'share', 'tok-a', 'uuid-a', 'pw', 0, '', '', '{}', 0, 1, '', 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := source.DB().Exec(`
		INSERT INTO user_subscription(id, user_id, subscription_id, created_time)
		VALUES(5, 1, 10, ?)
	`, now).Error; err != nil {
		t.Fatalf("seed user_subscription: %v", err)
	}

	dest := openBackupRepo(t, "dst.db")
	roundTripBackup(t, source, dest)
	// BUG-A2-4 订正：订阅按 token 重建会换新 id（空库里不是备份的 10），
	// 分配必须被重写到新 id——旧断言把 subscription_id 钉在 10，断言的
	// 正是分配悬空/错指的缺陷结果，故改为按 token 解析实际落库 id 后核对。
	var restoredSubID int64
	if err := dest.DB().Raw(`SELECT id FROM subscription WHERE token = 'tok-a'`).Scan(&restoredSubID).Error; err != nil {
		t.Fatalf("read restored subscription: %v", err)
	}
	if restoredSubID == 0 {
		t.Fatalf("restored subscription tok-a must exist")
	}
	var n int64
	if err := dest.DB().Raw(`SELECT COUNT(1) FROM user_subscription WHERE id = 5 AND user_id = 1 AND subscription_id = ?`, restoredSubID).Scan(&n).Error; err != nil {
		t.Fatalf("count restored user_subscription: %v", err)
	}
	if n != 1 {
		t.Fatalf("user_subscription assignment must survive backup round-trip pointing at the restored subscription (id %d), got %d rows", restoredSubID, n)
	}
}

func TestBackupImportLegacyWithoutUserSubscriptions(t *testing.T) {
	dest := openBackupRepo(t, "legacy.db")
	// 旧备份（无 userSubscriptions 字段）导入不得报错。
	legacy := &model.BackupData{Version: "1.0", ExportedAt: time.Now().UnixMilli()}
	if _, err := dest.Import(legacy, allBackupTypes); err != nil {
		t.Fatalf("legacy backup without userSubscriptions must import cleanly: %v", err)
	}
}

func TestBackupRoundTripsServiceMonitors(t *testing.T) {
	source := openBackupRepo(t, "src.db")
	now := time.Now().UnixMilli()
	if err := source.DB().Exec(`
		INSERT INTO service_monitor(id, name, type, target, interval_sec, timeout_sec, node_id, enabled, created_time, updated_time)
		VALUES(7, '落地健康', 'http', 'https://example.com/health', 30, 5, 0, 1, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("seed service_monitor: %v", err)
	}

	dest := openBackupRepo(t, "dst.db")
	roundTripBackup(t, source, dest)
	var name string
	if err := dest.DB().Raw(`SELECT name FROM service_monitor WHERE id = 7`).Scan(&name).Error; err != nil {
		t.Fatalf("read restored service_monitor: %v", err)
	}
	if name != "落地健康" {
		t.Fatalf("service_monitor definition must survive round-trip, got %q", name)
	}
}

func TestBackupRoundTripsNodeExtraIPs(t *testing.T) {
	source := openBackupRepo(t, "src.db")
	now := time.Now().UnixMilli()
	if err := source.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, port, extra_ips, created_time, updated_time, status)
		VALUES(3, 'multi-ip', 's3cret', '10.0.0.9', '1000-1010', '["198.51.100.7","198.51.100.8"]', ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	backup, err := source.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(backup.Nodes) != 1 || backup.Nodes[0].ExtraIPs == "" {
		t.Fatalf("exported node must carry ExtraIPs (F-027), got %+v", backup.Nodes)
	}

	dest := openBackupRepo(t, "dst.db")
	roundTripBackup(t, source, dest)
	var extra string
	if err := dest.DB().Raw(`SELECT extra_ips FROM node WHERE id = 3`).Scan(&extra).Error; err != nil {
		t.Fatalf("read restored node extra_ips: %v", err)
	}
	if !strings.Contains(extra, "198.51.100.7") {
		t.Fatalf("node ExtraIPs must survive round-trip (F-027), got %q", extra)
	}
}

// TestBackupTableCoverage 是防再漏的清单契约：库中每张业务表必须被分类为
// 「由某个 BackupData 字段覆盖」或「明确排除并写明理由」。新表落库而未
// 分类会直接红——F-004/F-030 这类"新表漏进备份清单"不得第三次发生。
func TestBackupTableCoverage(t *testing.T) {
	r := openBackupRepo(t, "cov.db")

	// BackupData 实际存在的 JSON 字段集合（反射真源，防注册表写了不存在的键）。
	jsonKeys := map[string]bool{}
	bt := reflect.TypeOf(model.BackupData{})
	for i := 0; i < bt.NumField(); i++ {
		tag := bt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		jsonKeys[strings.Split(tag, ",")[0]] = true
	}

	// covered: 表 -> BackupData JSON 键（user_quota 并入 users 的配额字段）。
	covered := map[string]string{
		"user": "users", "user_quota": "users",
		"node": "nodes", "tunnel": "tunnels", "chain_tunnel": "tunnels",
		"forward": "forwards", "forward_port": "forwards",
		"user_tunnel": "userTunnels", "speed_limit": "speedLimits",
		"tunnel_group": "tunnelGroups", "tunnel_group_tunnel": "tunnelGroups",
		"user_group": "userGroups", "user_group_user": "userGroups",
		"group_permission": "permissions", "group_permission_grant": "permissions",
		"vite_config":          "configs",
		"user_inbound":         "userInbounds",
		"user_subscription":    "userSubscriptions",
		"announcement":         "announcements",
		"monitor_permission":   "monitorPermissions",
		"service_monitor":      "serviceMonitors",
		"node_kernel":          "inbounds",
		"node_inbound":         "inbounds",
		"node_inbound_relay":   "inbounds",
		"node_inbound_counter": "inbounds",
		"subscription":         "inbounds",
		"remote_source":        "inbounds",
		"sub_user":             "inbounds",
		// FIX132-C：A3-04 订阅计数基线随 inbounds 段；A3-05 两表随
		// nodes 段（nodeSSHConfigs / peerShares 独立键）。
		"subscription_counter": "inbounds",
		"node_ssh_config":      "nodeSSHConfigs",
		"peer_share":           "peerShares",
	}
	// excluded: 表 -> 排除理由（安全凭据 / 运行时状态 / 遥测时序 / 簿记）。
	excluded := map[string]string{
		"passkey":                    "凭据材料，安全排除（O7 口径：备份不含 passkey）",
		"nft_rule_binding":           "nft 运行时状态，由 reconcile 按期望状态重建",
		"nft_counter_state":          "nft 计数基线，运行时状态",
		"nft_pending_delete":         "pending-delete 重试台账，运行时状态",
		"node_seq":                   "agent 流量序号，运行时状态",
		"statistics_flow":            "流量统计时序，非配置数据",
		"node_metric":                "监控遥测时序",
		"node_latency_sample":        "延迟探测遥测时序，非配置数据（7 天保留，探测自动重建）",
		"tunnel_metric":              "监控遥测时序",
		"tunnel_quality":             "质量探测时序",
		"service_monitor_result":     "监控结果时序（定义在 service_monitor，已备份）",
		"subscription_traffic":       "订阅流量时序遥测，非配置数据（400 天保留，计数样本持续重建）",
		"peer_share_runtime":         "联邦运行时状态",
		"peer_share_resource":        "联邦运行时状态",
		"federation_tunnel_binding":  "联邦运行时绑定，跨实例恢复无语义",
		"federation_pending_release": "联邦运行时状态",
		"schema_version":             "迁移簿记",
		"schema_migration":           "迁移簿记",
	}

	var tables []string
	if err := r.DB().Raw(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found")
	}
	for _, table := range tables {
		if key, ok := covered[table]; ok {
			if !jsonKeys[key] {
				t.Errorf("table %s registered as covered by BackupData key %q, but BackupData has no such json field", table, key)
			}
			continue
		}
		if _, ok := excluded[table]; ok {
			continue
		}
		t.Errorf("table %s is not classified for backup coverage — add it to covered (with export/import) or excluded (with a reason) in TestBackupTableCoverage", table)
	}
}
