package repo

import (
	"encoding/json"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Lane G2（1.3.1）：BUG-A2-3 / BUG-A2-4 回归。
//
// BUG-A2-3：chain_tunnel.port 线上为 NULL 时，旧导出把 NULL 与零值
// 一并丢弃、旧导入一律以 Valid:true 写回（NULL -> 0），同节点两条
// NULL 端口链路在恢复时撞 (node_id, port) 唯一索引，整单事务回滚
// ——含 ≥2 条同节点链式隧道的备份必恢复失败（W10 A2 真机实证）。
//
// BUG-A2-4：订阅在恢复时按 token 重建、被删过的订阅换新 id，
// user_subscription 仍按备份的旧 id 落库，分配悬空且回执假报成功。
//
// 同批同类（备份往返丢可空字段）：SpeedLimit 的 tunnel 绑定、Node 的
// server_ip_v4/v6 等可空列，一并覆盖。

func seedChainTunnelFixtures(t *testing.T, r *Repository, now int64) {
	t.Helper()
	// FIX132-C 订正：链路引用 node 1，夹具补齐该节点行。原夹具只有
	// 悬空引用（真实库中 chain_tunnel.node_id 必有对应节点，经 API
	// 创建时校验）；D4-03 起导入对不存在的引用整行跳过，悬空夹具
	// 会把「跳过」误读成 A2-3 修复失效。
	if err := r.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status)
		VALUES(1, 'g2-node', 'nsecret', '10.0.0.1', '1000-1010', ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(11, 'g2-t1', 1.0, 2, 'tls', 1, ?, ?, 1, 0),
		      (12, 'g2-t2', 1.0, 2, 'tls', 1, ?, ?, 1, 1)
	`, now, now, now, now).Error; err != nil {
		t.Fatalf("seed tunnels: %v", err)
	}
	// 两条链路同挂 node 1、port 均为 NULL —— A2-3 的精确触发形态。
	// 101 全可空列为 NULL；102 仅 port 为 NULL，其余带真实值验保真。
	if err := r.DB().Exec(`
		INSERT INTO chain_tunnel(id, tunnel_id, chain_type, node_id, port, strategy, inx, protocol, connect_ip)
		VALUES(101, 11, '1', 1, NULL, NULL, NULL, NULL, NULL),
		      (102, 12, '1', 1, NULL, 'round', 0, 'tcp', '10.0.0.1')
	`).Error; err != nil {
		t.Fatalf("seed chain_tunnel: %v", err)
	}
}

func countRaw(t *testing.T, r *Repository, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := r.DB().Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

// TestBackupImportTwoChainTunnelsNullPortSameNode 是 BUG-A2-3 的主回归：
// 修复前 Import 直接报 UNIQUE constraint failed 整单失败（roundTripBackup
// 内部 Fatalf）；修复后两条 NULL 端口链路完整恢复且 NULL 语义保真。
func TestBackupImportTwoChainTunnelsNullPortSameNode(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-a23-src.db")
	seedChainTunnelFixtures(t, source, now)

	dest := openBackupRepo(t, "g2-a23-dst.db")
	roundTripBackup(t, source, dest)

	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id IN (101, 102)`); n != 2 {
		t.Fatalf("both chain rows must be restored, got %d", n)
	}
	// 101：全部可空列必须仍是 NULL（不得写成 0/''）。
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id = 101
		AND port IS NULL AND strategy IS NULL AND inx IS NULL AND protocol IS NULL AND connect_ip IS NULL`); n != 1 {
		t.Fatalf("chain 101 nullable columns must stay NULL after restore, got %d matching rows", n)
	}
	// 102：port 仍 NULL；其余列逐值保真（inx=0 是有效值，不得变 NULL）。
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id = 102
		AND port IS NULL AND strategy = 'round' AND inx = 0 AND protocol = 'tcp' AND connect_ip = '10.0.0.1'`); n != 1 {
		t.Fatalf("chain 102 values must round-trip exactly, got %d matching rows", n)
	}
}

// TestBackupRestoreChainTunnelsDeletesStaleRows 验「先删后插」：目标库中
// 同隧道已漂移出的残留 chain 行不得在恢复后残留。修复前残留行 555
// 与备份行并存，此断言失败。
func TestBackupRestoreChainTunnelsDeletesStaleRows(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-stale-src.db")
	seedChainTunnelFixtures(t, source, now)

	backup, err := source.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded model.BackupData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	dest := openBackupRepo(t, "g2-stale-dst.db")
	// FIX132-C 订正：本用例只导入 tunnels 段，链路引用的 node 1
	// 须在目标库先行存在（同 seedChainTunnelFixtures 的理由）。
	if err := dest.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status)
		VALUES(1, 'g2-node', 'nsecret', '10.0.0.1', '1000-1010', ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed dest node: %v", err)
	}
	if err := dest.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(11, 'g2-t1-drifted', 1.0, 2, 'tls', 1, ?, ?, 1, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("seed dest tunnel: %v", err)
	}
	if err := dest.DB().Exec(`
		INSERT INTO chain_tunnel(id, tunnel_id, chain_type, node_id, port)
		VALUES(555, 11, '1', 1, 9999)
	`).Error; err != nil {
		t.Fatalf("seed stale chain row: %v", err)
	}

	if _, err := dest.Import(&decoded, []string{"tunnels"}); err != nil {
		t.Fatalf("import tunnels: %v", err)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE tunnel_id = 11`); n != 1 {
		t.Fatalf("restore must leave exactly the backup's chain rows for tunnel 11, got %d", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id = 555`); n != 0 {
		t.Fatalf("stale chain row 555 must be removed by restore, got %d", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM chain_tunnel WHERE id = 101 AND port IS NULL`); n != 1 {
		t.Fatalf("backup chain row 101 must be restored with NULL port, got %d", n)
	}
}

// TestBackupRestoreSubscriptionAssignmentsRemapped 是 BUG-A2-4 的主回归：
// 目标库中 id 19 已被另一订阅占用，备份订阅（token tok-g2）只能换新 id
// 落库，分配必须被重写到新 id。修复前分配原样指向 19（错指到无关订阅），
// 此断言失败。
func TestBackupRestoreSubscriptionAssignmentsRemapped(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-a24-src.db")
	if err := source.DB().Exec(`
		INSERT INTO subscription(id, name, kind, token, uuid, password, source_all, inbound_ids, source_ids, options, expire_time, enabled, remark, inx, created_time, updated_time)
		VALUES(19, 'g2-sub', 'share', 'tok-g2', 'uuid-g2', 'pw', 0, '', '', '{}', 0, 1, '', 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := source.DB().Exec(`
		INSERT INTO user_subscription(id, user_id, subscription_id, created_time)
		VALUES(5, 1, 19, ?)
	`, now).Error; err != nil {
		t.Fatalf("seed user_subscription: %v", err)
	}

	dest := openBackupRepo(t, "g2-a24-dst.db")
	if err := dest.DB().Exec(`
		INSERT INTO subscription(id, name, kind, token, uuid, password, source_all, inbound_ids, source_ids, options, expire_time, enabled, remark, inx, created_time, updated_time)
		VALUES(19, 'other', 'share', 'tok-other', 'uuid-other', 'pw', 0, '', '', '{}', 0, 1, '', 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("seed occupying subscription: %v", err)
	}

	result := roundTripBackup(t, source, dest)
	if result.UserSubscriptionsImported != 1 || result.UserSubscriptionsSkipped != 0 {
		t.Fatalf("expected 1 imported / 0 skipped assignments, got imported=%d skipped=%d",
			result.UserSubscriptionsImported, result.UserSubscriptionsSkipped)
	}
	var newID int64
	if err := dest.DB().Raw(`SELECT id FROM subscription WHERE token = 'tok-g2'`).Scan(&newID).Error; err != nil {
		t.Fatalf("read restored subscription: %v", err)
	}
	if newID == 0 || newID == 19 {
		t.Fatalf("restored subscription must take a fresh id (19 is occupied), got %d", newID)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user_subscription WHERE id = 5 AND user_id = 1 AND subscription_id = ?`, newID); n != 1 {
		t.Fatalf("assignment must be rewritten to restored subscription id %d, got %d matching rows", newID, n)
	}
}

// TestBackupImportUserSubscriptionsSkipsUnresolvable 验无法落位的分配：
// 目标订阅既不在备份的订阅段、当前库也不存在 —— 必须跳过并在回执
// 如实计数，不得写悬空行、不得计入 imported 假报成功。
// （UserSubscriptionsSkipped 为本次新增回执字段。）
func TestBackupImportUserSubscriptionsSkipsUnresolvable(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "g2-skip-dst.db")
	backup := &model.BackupData{
		Version: "1.0",
		UserSubscriptions: []model.UserSubscriptionBackup{
			{ID: 9, UserID: 1, SubscriptionID: 999, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"userSubscriptions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserSubscriptionsImported != 0 || result.UserSubscriptionsSkipped != 1 {
		t.Fatalf("expected 0 imported / 1 skipped, got imported=%d skipped=%d",
			result.UserSubscriptionsImported, result.UserSubscriptionsSkipped)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user_subscription`); n != 0 {
		t.Fatalf("unresolvable assignment must not be written, got %d rows", n)
	}
}

// TestBackupRoundTripsSpeedLimitTunnelBinding 覆盖同类问题：限速规则的
// 隧道绑定（可空列）在旧导出/导入中被整体丢弃，恢复后静默变全局规则。
func TestBackupRoundTripsSpeedLimitTunnelBinding(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-sl-src.db")
	// FIX132-C 订正：补齐被绑定的隧道行（原夹具引用不存在的隧道
	// 11；D4-03 起导入对悬空绑定整行跳过，保真断言无从成立）。
	if err := source.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(11, 'g2-t1', 1.0, 2, 'tls', 1, ?, ?, 1, 0)
	`, now, now).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := source.DB().Exec(`
		INSERT INTO speed_limit(id, name, speed, tunnel_id, tunnel_name, created_time, updated_time, status)
		VALUES(7, 'g2-limit', 100, 11, 'g2-t1', ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed speed_limit: %v", err)
	}

	dest := openBackupRepo(t, "g2-sl-dst.db")
	roundTripBackup(t, source, dest)
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM speed_limit WHERE id = 7 AND tunnel_id = 11 AND tunnel_name = 'g2-t1'`); n != 1 {
		t.Fatalf("speed limit tunnel binding must survive round-trip, got %d matching rows", n)
	}
}

// TestBackupRoundTripsNodeNullableFieldsStayNull 覆盖同类问题：Node 的
// 可空列（server_ip_v4 等）在旧导入中被无条件写成 ” 有效值。
func TestBackupRoundTripsNodeNullableFieldsStayNull(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-node-src.db")
	if err := source.DB().Exec(`
		INSERT INTO node(id, name, secret, server_ip, port, created_time, updated_time, status)
		VALUES(3, 'g2-node', 's3cret', '10.0.0.9', '1000-1010', ?, ?, 1)
	`, now, now).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	dest := openBackupRepo(t, "g2-node-dst.db")
	roundTripBackup(t, source, dest)
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM node WHERE id = 3
		AND server_ip_v4 IS NULL AND server_ip_v6 IS NULL AND interface_name IS NULL
		AND version IS NULL AND remote_url IS NULL AND remote_token IS NULL AND remote_config IS NULL`); n != 1 {
		t.Fatalf("node nullable columns must stay NULL after restore, got %d matching rows", n)
	}
}

// TestBackupRestoreKeepsZeroValuedFlags 覆盖与 BUG-A4-1 同根的备份侧变体：
// Enabled/Managed 带 gorm `default:1` 的行（内核/入站/公告/监控/中转
// 关联），备份意图为 0 时旧导入在 INSERT 与 OnConflict 更新两个分支都
// 被默认值吞成 1 —— 停用入站恢复后被静默启用正是 A4-1 的恢复版。
// 基线实测（探针）：两分支、五类行全部 0 -> 1。修复以 restoreZeroValuedInt
// 在 Create 后按意图显式写回 0，两分支一并覆盖。
func TestBackupRestoreKeepsZeroValuedFlags(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "g2-flags-src.db")
	seeds := []struct {
		q    string
		args []interface{}
	}{
		{`INSERT INTO node_kernel(id, node_id, kernel_type, version, enabled, created_time, updated_time)
		  VALUES(31, 1, 'singbox', '1.14.3', 0, ?, ?)`, []interface{}{now, now}},
		{`INSERT INTO node_inbound(id, kernel_id, node_id, name, protocol, listen, port, enabled, created_time, updated_time)
		  VALUES(41, 31, 1, 'g2-disabled', 'vless', '::', 24441, 0, ?, ?)`, []interface{}{now, now}},
		{`INSERT INTO node_inbound_relay(id, inbound_id, forward_id, name, managed, created_time)
		  VALUES(51, 41, 777, 'g2-relay', 0, ?)`, []interface{}{now}},
		{`INSERT INTO announcement(id, content, enabled, created_time, updated_time)
		  VALUES(61, 'g2-ann', 0, ?, ?)`, []interface{}{now, now}},
		{`INSERT INTO service_monitor(id, name, type, target, interval_sec, timeout_sec, node_id, enabled, created_time, updated_time)
		  VALUES(71, 'g2-mon', 'http', 'https://example.com', 30, 5, 0, 0, ?, ?)`, []interface{}{now, now}},
	}
	for _, s := range seeds {
		if err := source.DB().Exec(s.q, s.args...).Error; err != nil {
			t.Fatalf("seed (%s): %v", s.q[:40], err)
		}
	}

	dest := openBackupRepo(t, "g2-flags-dst.db")
	readFlags := func(stage string) (ie, ke, rm, ae, me int64) {
		queries := []struct {
			q string
			p *int64
		}{
			{`SELECT enabled FROM node_inbound WHERE id = 41`, &ie},
			{`SELECT enabled FROM node_kernel WHERE id = 31`, &ke},
			{`SELECT managed FROM node_inbound_relay WHERE id = 51`, &rm},
			{`SELECT enabled FROM announcement WHERE id = 61`, &ae},
			{`SELECT enabled FROM service_monitor WHERE id = 71`, &me},
		}
		for _, qq := range queries {
			if err := dest.DB().Raw(qq.q).Scan(qq.p).Error; err != nil {
				t.Fatalf("%s: %s: %v", stage, qq.q, err)
			}
		}
		return
	}
	assertZero := func(stage string) {
		ie, ke, rm, ae, me := readFlags(stage)
		if ie != 0 || ke != 0 || rm != 0 || ae != 0 || me != 0 {
			t.Fatalf("%s: zero-valued flags must survive restore, got inbound=%d kernel=%d relay=%d announcement=%d monitor=%d",
				stage, ie, ke, rm, ae, me)
		}
	}

	// INSERT 分支：空库恢复。
	roundTripBackup(t, source, dest)
	assertZero("insert branch")

	// 更新分支：目标行先被置 1，再恢复同一备份，必须回到 0。
	for _, q := range []string{
		`UPDATE node_inbound SET enabled = 1 WHERE id = 41`,
		`UPDATE node_kernel SET enabled = 1 WHERE id = 31`,
		`UPDATE node_inbound_relay SET managed = 1 WHERE id = 51`,
		`UPDATE announcement SET enabled = 1 WHERE id = 61`,
		`UPDATE service_monitor SET enabled = 1 WHERE id = 71`,
	} {
		if err := dest.DB().Exec(q).Error; err != nil {
			t.Fatalf("force 1 (%s): %v", q, err)
		}
	}
	roundTripBackup(t, source, dest)
	assertZero("update branch")
}
