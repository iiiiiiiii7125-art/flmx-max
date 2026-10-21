package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// FIX132-C 备份族回归（A3-04 备份侧 / A3-05 / D4-01 / D4-02 / D4-03）。
// 形态沿 D4 探针：往返走 JSON 边界，部分段/冲突用手写备份直调 Import。

// D4-01：user_quota 的 PausedForwardIDs 与已用量必须往返保真。
// 旧实现导出不读、导入写 ""/0：配额暂停的转发恢复后永不自动解封
// （解封唯一依据就是该清单），已用量清零等于未告知的配额豁免。
func TestFix132BackupRoundTripsUserQuotaFidelity(t *testing.T) {
	now := time.Now()
	nowMs := now.UnixMilli()
	dayKey, monthKey := userQuotaWindowKeys(now)
	source := openBackupRepo(t, "quota-src.db")
	createRow(t, source, &model.User{ID: 7, User: "quota-user", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: nowMs})
	createRow(t, source, &model.UserQuota{
		UserID: 7, DailyLimitGB: 10, MonthlyLimitGB: 100,
		DailyUsedBytes: 123456, MonthlyUsedBytes: 654321,
		DayKey: dayKey, MonthKey: monthKey,
		DisabledByQuota: 1, DisabledAt: nowMs, PausedForwardIDs: "3,4",
		CreatedTime: nowMs, UpdatedTime: nowMs,
	})

	dest := openBackupRepo(t, "quota-dst.db")
	roundTripBackup(t, source, dest)

	var q model.UserQuota
	if err := dest.DB().Where("user_id = ?", 7).First(&q).Error; err != nil {
		t.Fatalf("quota row must be restored: %v", err)
	}
	if q.PausedForwardIDs != "3,4" {
		t.Fatalf("D4-01: paused_forward_ids = %q, want %q", q.PausedForwardIDs, "3,4")
	}
	if q.DailyUsedBytes != 123456 || q.MonthlyUsedBytes != 654321 {
		t.Fatalf("D4-01: used bytes = %d/%d, want 123456/654321", q.DailyUsedBytes, q.MonthlyUsedBytes)
	}
	if q.DailyLimitGB != 10 || q.MonthlyLimitGB != 100 || q.DisabledByQuota != 1 {
		t.Fatalf("quota limits/disabled must round-trip, got %+v", q)
	}
}

// A3-05：node_ssh_config 与 peer_share 进备份；恢复回执带计数、
// 受影响节点清单与「需重新下发」提示（BC-28）。
func TestFix132BackupRoundTripsNodeSSHConfigAndPeerShare(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "ssh-share-src.db")
	createRow(t, source, &model.Node{ID: 3, Name: "nft-node", Secret: "node-secret", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}, ForwardMode: "nftables"})
	createRow(t, source, &model.NodeSSHConfig{NodeID: 3, Host: "10.0.0.3", Port: 2222, Username: "root",
		AuthType: "password", Password: sql.NullString{String: "ssh-pw", Valid: true}, SudoMode: "sudo",
		CreatedTime: now, UpdatedTime: now})
	createRow(t, source, &model.PeerShare{ID: 9, Name: "share-a", NodeID: 3, Token: "share-token-a",
		MaxBandwidth: 1000, PortRangeStart: 20000, PortRangeEnd: 20010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now, AllowedDomains: "example.com"})

	dest := openBackupRepo(t, "ssh-share-dst.db")
	result := roundTripBackup(t, source, dest)

	var cfg model.NodeSSHConfig
	if err := dest.DB().Where("node_id = ?", 3).First(&cfg).Error; err != nil {
		t.Fatalf("A3-05: ssh config must be restored: %v", err)
	}
	if cfg.Host != "10.0.0.3" || cfg.Port != 2222 || !cfg.Password.Valid || cfg.Password.String != "ssh-pw" || cfg.SudoMode != "sudo" {
		t.Fatalf("A3-05: ssh config mismatch: %+v", cfg)
	}
	var share model.PeerShare
	if err := dest.DB().Where("id = ?", 9).First(&share).Error; err != nil {
		t.Fatalf("A3-05: peer share must be restored: %v", err)
	}
	if share.Token != "share-token-a" || share.PortRangeStart != 20000 || share.PortRangeEnd != 20010 {
		t.Fatalf("A3-05: peer share mismatch: %+v", share)
	}
	if result.NodeSSHConfigsImported != 1 || result.PeerSharesImported != 1 {
		t.Fatalf("receipt counts: ssh=%d shares=%d, want 1/1", result.NodeSSHConfigsImported, result.PeerSharesImported)
	}
	foundNode := false
	for _, id := range result.RestoredSSHNodeIDs {
		if id == 3 {
			foundNode = true
		}
	}
	if !foundNode {
		t.Fatalf("receipt must list restored SSH node ids, got %v", result.RestoredSSHNodeIDs)
	}
	foundWarning := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "SSH") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("receipt must warn that restored SSH nodes need re-dispatch, warnings=%v", result.ImportWarnings)
	}
}

// A3-04 备份侧：subscription_counter 基线随 InboundsBackup 往返，
// 恢复后首样本按差分计（与消费者「缺失只建基线」双保险）。
func TestFix132BackupRoundTripsSubscriptionCounters(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "sub-counter-src.db")
	createRow(t, source, &model.Node{ID: 3, Name: "n3", Secret: "s", ServerIP: "10.0.0.3", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, source, &model.Subscription{ID: 21, Name: "sub", Token: "tok-counter", Kind: "share", Enabled: 1,
		CreatedTime: now, UpdatedTime: now, UploadBytes: 1000, DownloadBytes: 2000})
	createRow(t, source, &model.SubscriptionCounter{NodeID: 3, SubscriptionID: 21, Direction: "up", Epoch: "e9", Bytes: 1000, UpdatedTime: now})
	createRow(t, source, &model.SubscriptionCounter{NodeID: 3, SubscriptionID: 21, Direction: "down", Epoch: "e9", Bytes: 2000, UpdatedTime: now})

	dest := openBackupRepo(t, "sub-counter-dst.db")
	roundTripBackup(t, source, dest)

	var counters []model.SubscriptionCounter
	if err := dest.DB().Where("node_id = ?", 3).Find(&counters).Error; err != nil {
		t.Fatalf("list restored counters: %v", err)
	}
	if len(counters) != 2 {
		t.Fatalf("A3-04: subscription counter baselines must be restored, got %d rows", len(counters))
	}
	// 恢复后同 epoch 增量样本只计差分：up 1000→1250 计 +250。
	var sub model.Subscription
	if err := dest.DB().Where("token = ?", "tok-counter").First(&sub).Error; err != nil {
		t.Fatalf("restored subscription: %v", err)
	}
	if err := dest.ApplySubscriptionCounterSamples(3, "e9", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 1250},
	}, now+60000); err != nil {
		t.Fatalf("apply post-restore sample: %v", err)
	}
	if err := dest.DB().First(&sub, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if sub.UploadBytes != 1250 {
		t.Fatalf("post-restore sample must count delta only, upload = %d, want 1250", sub.UploadBytes)
	}
}

// D4-02（探针 B 同构）：monitor_permission 业务键冲突不得整单回滚。
func TestFix132ImportMonitorPermissionConflictDoesNotRollbackAll(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "mp-conflict.db")
	// 用户 id 避开 New() 播种的默认管理员（id=1）。
	createRow(t, dest, &model.User{ID: 101, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.MonitorPermission{ID: 99, UserID: 101, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Users:   []model.UserBackup{{ID: 102, User: "bob", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now}},
		MonitorPermissions: []model.MonitorPermissionBackup{
			{ID: 5, UserID: 101, CreatedTime: now},
			{ID: 6, UserID: 102, CreatedTime: now},
		},
	}
	result, err := dest.Import(backup, []string{"users", "monitorPermissions"})
	if err != nil {
		t.Fatalf("D4-02: import must not fail on monitor_permission business-key conflict: %v", err)
	}
	if result.MonitorPermissionsImported != 2 {
		t.Fatalf("monitor permissions imported = %d, want 2", result.MonitorPermissionsImported)
	}
	if n := countWhere(t, dest, "user", "user = ?", "bob"); n != 1 {
		t.Fatalf("D4-02: unrelated users segment must land, bob rows = %d", n)
	}
	if n := countWhere(t, dest, "monitor_permission", "user_id = ?", 101); n != 1 {
		t.Fatalf("exactly one monitor_permission per user must remain, got %d", n)
	}
}

// D4-02：分组名唯一冲突与权限复合唯一冲突同治。
func TestFix132ImportGroupNameAndPermissionConflicts(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "group-conflict.db")
	// 用户 id 避开 New() 播种的默认管理员（id=1）。
	createRow(t, dest, &model.User{ID: 101, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 5, Name: "t5", Type: 1, CreatedTime: now, UpdatedTime: now})
	// 同名异 id 的既有分组 + 同复合键异 id 的既有权限行
	createRow(t, dest, &model.UserGroup{ID: 9, Name: "vip", CreatedTime: now, UpdatedTime: now, Status: 1})
	createRow(t, dest, &model.TunnelGroup{ID: 8, Name: "tg", CreatedTime: now, UpdatedTime: now, Status: 1})
	createRow(t, dest, &model.GroupPermission{ID: 9, UserGroupID: 5, TunnelGroupID: 8, CreatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 5, Name: "vip", CreatedTime: now, Status: 1, Users: []int64{101}},
		},
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 8, Name: "tg", CreatedTime: now, Status: 1, Tunnels: []int64{5}},
		},
		Permissions: []model.PermissionBackup{
			{ID: 5, UserGroupID: 5, TunnelGroupID: 8, CreatedTime: now},
		},
	}
	if _, err := dest.Import(backup, []string{"userGroups", "tunnelGroups", "permissions"}); err != nil {
		t.Fatalf("D4-02: group/permission business-key conflicts must not fail the import: %v", err)
	}
	if n := countWhere(t, dest, "user_group", "name = ?", "vip"); n != 1 {
		t.Fatalf("exactly one group named vip must remain, got %d", n)
	}
	if n := countWhere(t, dest, "user_group_user", "user_group_id = ? AND user_id = ?", 5, 101); n != 1 {
		t.Fatalf("restored group membership must land, got %d", n)
	}
	if n := countWhere(t, dest, "group_permission", "user_group_id = ? AND tunnel_group_id = ?", 5, 8); n != 1 {
		t.Fatalf("exactly one permission for the pair must remain, got %d", n)
	}
}

// D4-03（探针 C 同构）：部分段恢复不得写悬空行，跳过须回执计数。
func TestFix132ImportUserInboundsSkipsDangling(t *testing.T) {
	dest := openBackupRepo(t, "dangling-ui.db")
	backup := &model.BackupData{
		Version:      "1.0",
		UserInbounds: []model.UserInboundBackup{{ID: 9, UserID: 42, InboundID: 77, RelayID: 0}},
	}
	result, err := dest.Import(backup, []string{"userInbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserInboundsImported != 0 {
		t.Fatalf("dangling rows must not be counted as imported, got %d", result.UserInboundsImported)
	}
	if result.SkippedRows["userInbounds"] != 1 {
		t.Fatalf("skip must be receipted, SkippedRows = %v", result.SkippedRows)
	}
	if n := countWhere(t, dest, "user_inbound", "1 = 1"); n != 0 {
		t.Fatalf("D4-03: dangling user_inbound rows written: %d", n)
	}
}

// D4-03：引用缺失的转发整行跳过并计数，不写悬空行。
func TestFix132ImportForwardsSkipsDangling(t *testing.T) {
	dest := openBackupRepo(t, "dangling-fwd.db")
	backup := &model.BackupData{
		Version:  "1.0",
		Forwards: []model.ForwardBackup{{ID: 50, UserID: 42, UserName: "ghost", Name: "f", TunnelID: 99, Status: 1}},
	}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 0 || result.SkippedRows["forwards"] != 1 {
		t.Fatalf("dangling forward must be skipped and receipted: %+v", result)
	}
	if n := countWhere(t, dest, "forward", "1 = 1"); n != 0 {
		t.Fatalf("D4-03: dangling forward rows written: %d", n)
	}
}

// D4-03（探针 D 同构）：同 id 异源节点被顶替必须回执提示。
func TestFix132ImportNodeReplacementWarns(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "node-replace.db")
	createRow(t, dest, &model.Node{ID: 3, Name: "local-node", Secret: "local-secret", ServerIP: "10.0.0.3",
		Port: "10000-10100", CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	backup := &model.BackupData{
		Version: "1.0",
		Nodes:   []model.NodeBackup{{ID: 3, Name: "foreign-node", Secret: "foreign-secret", ServerIP: "10.9.9.9", Port: "10000-10100", CreatedTime: now}},
	}
	result, err := dest.Import(backup, []string{"nodes"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var node model.Node
	if err := dest.DB().Where("id = ?", 3).First(&node).Error; err != nil {
		t.Fatalf("node: %v", err)
	}
	if node.Name != "foreign-node" {
		t.Fatalf("restore keeps backup-wins semantics, node = %+v", node)
	}
	found := false
	for _, w := range result.ImportWarnings {
		if strings.Contains(w, "3") {
			found = true
		}
	}
	if !found {
		t.Fatalf("D4-03: same-id node replacement must be warned in receipt, warnings = %v", result.ImportWarnings)
	}
}
