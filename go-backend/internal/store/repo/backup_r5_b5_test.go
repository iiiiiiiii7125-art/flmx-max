package repo

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R5 批 5（备份导入族）回归：R5X-009 / R5X-016 / R5X-017 / R5X-024。
// 全部用例先红后绿：修复前非法 status 原样落库、入站回执按备份行数
// 虚报、嵌套集合预删+跳过静默、坏入站整对象逐字落库。

const r5b5Bcrypt = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012"

func r5b5WarningsContain(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// R5X-009：users/userGroups/tunnelGroups 三段 status {0,1} 枚举门——
// status=2/-1 逐行跳过 + skipped/warning，合法 0/1 逐字落库；被跳过
// 的分组不得触发成员替换等任何后续处置。
func TestR5B5ImportStatusEnumGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r5b5-status.db")
	backup := &model.BackupData{
		Version: "1.0",
		Users: []model.UserBackup{
			{ID: 2, User: "ok-enabled", Pwd: r5b5Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
			{ID: 3, User: "ok-disabled", Pwd: r5b5Bcrypt, RoleID: 1, Status: 0, CreatedTime: now},
			{ID: 4, User: "bad-two", Pwd: r5b5Bcrypt, RoleID: 1, Status: 2, CreatedTime: now},
			{ID: 5, User: "bad-neg", Pwd: r5b5Bcrypt, RoleID: 1, Status: -1, CreatedTime: now},
		},
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "ug-ok", Status: 1, CreatedTime: now},
			{ID: 2, Name: "ug-off", Status: 0, CreatedTime: now},
			{ID: 3, Name: "ug-bad", Status: 2, CreatedTime: now, Users: []int64{2}},
			{ID: 4, Name: "ug-neg", Status: -1, CreatedTime: now, Users: []int64{2}},
		},
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "tg-ok", Status: 1, CreatedTime: now},
			{ID: 2, Name: "tg-off", Status: 0, CreatedTime: now},
			{ID: 3, Name: "tg-bad", Status: 2, CreatedTime: now, Tunnels: []int64{1}},
			{ID: 4, Name: "tg-neg", Status: -1, CreatedTime: now, Tunnels: []int64{1}},
		},
	}
	result, err := dest.Import(backup, []string{"users", "userGroups", "tunnelGroups"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UsersImported != 2 || result.UserGroupsImported != 2 || result.TunnelGroupsImported != 2 {
		t.Fatalf("R5X-009: imported counts = users %d userGroups %d tunnelGroups %d, want 2/2/2",
			result.UsersImported, result.UserGroupsImported, result.TunnelGroupsImported)
	}
	if result.SkippedRows["users"] != 2 || result.SkippedRows["userGroups"] != 2 || result.SkippedRows["tunnelGroups"] != 2 {
		t.Fatalf("R5X-009: skipped = %v, want users/userGroups/tunnelGroups each 2", result.SkippedRows)
	}
	if !r5b5WarningsContain(result.ImportWarnings, "status") {
		t.Fatalf("R5X-009: illegal status skips must be disclosed in warnings, got %v", result.ImportWarnings)
	}
	// 合法 0/1 逐字落库。
	var st int
	for id, want := range map[int64]int{2: 1, 3: 0} {
		if err := dest.DB().Raw(`SELECT status FROM user WHERE id = ?`, id).Scan(&st).Error; err != nil {
			t.Fatalf("user %d: %v", id, err)
		}
		if st != want {
			t.Fatalf("R5X-009: legal user %d status = %d, want %d verbatim", id, st, want)
		}
	}
	for id, want := range map[int64]int{1: 1, 2: 0} {
		if err := dest.DB().Raw(`SELECT status FROM user_group WHERE id = ?`, id).Scan(&st).Error; err != nil {
			t.Fatalf("user_group %d: %v", id, err)
		}
		if st != want {
			t.Fatalf("R5X-009: legal user_group %d status = %d, want %d verbatim", id, st, want)
		}
		if err := dest.DB().Raw(`SELECT status FROM tunnel_group WHERE id = ?`, id).Scan(&st).Error; err != nil {
			t.Fatalf("tunnel_group %d: %v", id, err)
		}
		if st != want {
			t.Fatalf("R5X-009: legal tunnel_group %d status = %d, want %d verbatim", id, st, want)
		}
	}
	// 非法行不得以任何形态落库，库内无 status∉{0,1} 行。
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user WHERE status NOT IN (0,1)`); n != 0 {
		t.Fatalf("R5X-009: %d user rows with illegal status landed", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user_group WHERE status NOT IN (0,1)`); n != 0 {
		t.Fatalf("R5X-009: %d user_group rows with illegal status landed", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM tunnel_group WHERE status NOT IN (0,1)`); n != 0 {
		t.Fatalf("R5X-009: %d tunnel_group rows with illegal status landed", n)
	}
	if n := countRaw(t, dest, `SELECT COUNT(1) FROM user WHERE id IN (4,5)`); n != 0 {
		t.Fatalf("R5X-009: illegal-status users must not land, got %d rows", n)
	}
	// 被跳过的分组不得处理成员（成员替换在跳过之后不得发生）。
	if n := countWhere(t, dest, "user_group_user", "user_group_id IN (3,4)"); n != 0 {
		t.Fatalf("R5X-009: skipped user groups must not write members, got %d", n)
	}
	if n := countWhere(t, dest, "tunnel_group_tunnel", "tunnel_group_id IN (3,4)"); n != 0 {
		t.Fatalf("R5X-009: skipped tunnel groups must not write members, got %d", n)
	}
}

// R5X-016：InboundsImported 为实落计数——被存在性校验跳过的入站不
// 计入，与 SkippedRows 不重复计数；全跳过时为 0。
func TestR5B5InboundsImportedActualCount(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r5b5-inbcount.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 10, KernelID: 1, NodeID: 1, Name: "ok", Protocol: "vless", Port: 443, Enabled: 1, CreatedTime: now},
				{ID: 11, KernelID: 1, NodeID: 999, Name: "dangling", Protocol: "vless", Port: 8443, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 1 {
		t.Fatalf("R5X-016: InboundsImported = %d, want 1 (actual landed rows)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 1 {
		t.Fatalf("R5X-016: SkippedRows[inbounds] = %d, want 1", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (10,11)"); n != 1 {
		t.Fatalf("R5X-016: landed inbound rows = %d, want 1", n)
	}

	// 全跳过备份：InboundsImported 必须为 0。
	dest2 := openBackupRepo(t, "r5b5-inbcount-allskip.db")
	backup2 := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Inbounds: []model.InboundBackup{
				{ID: 10, KernelID: 0, NodeID: 999, Name: "dangling", Protocol: "vless", Port: 443, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result2, err := dest2.Import(backup2, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import all-skip: %v", err)
	}
	if result2.InboundsImported != 0 {
		t.Fatalf("R5X-016: all-skipped InboundsImported = %d, want 0", result2.InboundsImported)
	}
}

// R5X-017：四段嵌套集合「预删>0 且跳过>0」补 warning（不动删除
// 时序）；干净重建（跳过=0）不得新增 warning（防过修对照）。
func TestR5B5NestedPreDeleteSkipWarnings(t *testing.T) {
	now := time.Now().UnixMilli()

	// (a) chain：存量 chain 被预删、备份 chain 引用缺失节点被跳过。
	dest := openBackupRepo(t, "r5b5-nested-chain.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	port5001 := 5001
	createRow(t, dest, &model.ChainTunnel{ID: 1, TunnelID: 1, NodeID: 1, Port: sql.NullInt64{Int64: 5001, Valid: true}})
	backup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
				{ID: 2, TunnelID: 1, NodeID: 999, Port: &port5001},
			}},
		},
	}
	result, err := dest.Import(backup, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import tunnels: %v", err)
	}
	if n := countWhere(t, dest, "chain_tunnel", "tunnel_id = ?", 1); n != 0 {
		t.Fatalf("setup expectation: pre-deleted chain rows must be gone, got %d", n)
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("R5X-017: pre-delete + skip on chain tunnels must warn, got no warnings (skipped=%v)", result.SkippedRows)
	}

	// (b) forwardPorts：存量端口被预删、备份端口引用缺失节点被跳过。
	destB := openBackupRepo(t, "r5b5-nested-ports.db")
	createRow(t, destB, &model.User{ID: 2, User: "u2", Pwd: r5b5Bcrypt, RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, destB, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, destB, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destB, &model.Forward{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destB, &model.ForwardPort{ForwardID: 20, NodeID: 1, Port: 6001})
	ports := []model.ForwardPortBackup{{NodeID: 999, Port: 6002}}
	backupB := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1, CreatedTime: now, ForwardPorts: &ports},
		},
	}
	resultB, err := destB.Import(backupB, []string{"forwards"})
	if err != nil {
		t.Fatalf("import forwards: %v", err)
	}
	if n := countWhere(t, destB, "forward_port", "forward_id = ?", 20); n != 0 {
		t.Fatalf("setup expectation: pre-deleted forward ports must be gone, got %d", n)
	}
	if len(resultB.ImportWarnings) == 0 {
		t.Fatalf("R5X-017: pre-delete + skip on forward ports must warn, got no warnings (skipped=%v)", resultB.SkippedRows)
	}

	// (c) tunnelGroup 成员：存量成员被预删、备份成员引用缺失隧道被跳过。
	destC := openBackupRepo(t, "r5b5-nested-tgm.db")
	createRow(t, destC, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destC, &model.TunnelGroup{ID: 1, Name: "tg", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destC, &model.TunnelGroupTunnel{TunnelGroupID: 1, TunnelID: 1, CreatedTime: now})
	backupC := &model.BackupData{
		Version: "1.0",
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "tg", Status: 1, CreatedTime: now, Tunnels: []int64{999}},
		},
	}
	resultC, err := destC.Import(backupC, []string{"tunnelGroups"})
	if err != nil {
		t.Fatalf("import tunnelGroups: %v", err)
	}
	if n := countWhere(t, destC, "tunnel_group_tunnel", "tunnel_group_id = ?", 1); n != 0 {
		t.Fatalf("setup expectation: pre-deleted tunnel group members must be gone, got %d", n)
	}
	if len(resultC.ImportWarnings) == 0 {
		t.Fatalf("R5X-017: pre-delete + skip on tunnel group members must warn, got no warnings (skipped=%v)", resultC.SkippedRows)
	}

	// (d) userGroup 成员：存量成员被预删、备份成员引用缺失用户被跳过。
	destD := openBackupRepo(t, "r5b5-nested-ugm.db")
	createRow(t, destD, &model.User{ID: 2, User: "u2", Pwd: r5b5Bcrypt, RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, destD, &model.UserGroup{ID: 1, Name: "ug", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destD, &model.UserGroupUser{UserGroupID: 1, UserID: 2, CreatedTime: now})
	backupD := &model.BackupData{
		Version: "1.0",
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "ug", Status: 1, CreatedTime: now, Users: []int64{999}},
		},
	}
	resultD, err := destD.Import(backupD, []string{"userGroups"})
	if err != nil {
		t.Fatalf("import userGroups: %v", err)
	}
	if n := countWhere(t, destD, "user_group_user", "user_group_id = ?", 1); n != 0 {
		t.Fatalf("setup expectation: pre-deleted user group members must be gone, got %d", n)
	}
	if len(resultD.ImportWarnings) == 0 {
		t.Fatalf("R5X-017: pre-delete + skip on user group members must warn, got no warnings (skipped=%v)", resultD.SkippedRows)
	}

	// 防过修对照：存量 chain 被备份 chain 完整重建（跳过=0）时不得
	// 新增 warning。
	destE := openBackupRepo(t, "r5b5-nested-clean.db")
	createRow(t, destE, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, destE, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, destE, &model.ChainTunnel{ID: 1, TunnelID: 1, NodeID: 1, Port: sql.NullInt64{Int64: 5001, Valid: true}})
	backupE := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
				{ID: 1, TunnelID: 1, NodeID: 1, Port: &port5001},
			}},
		},
	}
	resultE, err := destE.Import(backupE, []string{"tunnels"})
	if err != nil {
		t.Fatalf("import clean tunnels: %v", err)
	}
	if n := countWhere(t, destE, "chain_tunnel", "tunnel_id = ?", 1); n != 1 {
		t.Fatalf("clean chain rebuild must land 1 row, got %d", n)
	}
	if len(resultE.ImportWarnings) != 0 {
		t.Fatalf("R5X-017 防过修: clean nested rebuild must not warn, got %v", resultE.ImportWarnings)
	}
}

// R5X-024：入站四整对象（settings/transport/security/extra）落库前
// 跑 Normalize 等价校验——坏 SS2022 密钥、坏 Reality 私钥、损坏
// JSON 整行跳过 + 计数 + warning；合法行逐字落库（校验只读副本，
// 不得以归一化结果覆写备份原值）。
func TestR5B5ImportInboundNormalizeGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r5b5-inbnormalize.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	validSSSettings := `{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg=="}`
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 10, KernelID: 1, NodeID: 1, Name: "bad-ss", Protocol: "shadowsocks", Port: 8388, Enabled: 1, CreatedTime: now,
					Settings:  `{"method":"2022-blake3-aes-128-gcm","password":"plainpassword"}`,
					Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`},
				{ID: 11, KernelID: 1, NodeID: 1, Name: "bad-reality", Protocol: "vless", Port: 443, Enabled: 1, CreatedTime: now,
					Settings:  `{"uuid":"11111111-1111-1111-1111-111111111111"}`,
					Transport: `{"type":"tcp"}`,
					Security:  `{"type":"reality","realityPrivateKey":"GARBAGE!!","realityPublicKey":"GARBAGE!!"}`},
				{ID: 12, KernelID: 1, NodeID: 1, Name: "good-ss", Protocol: "shadowsocks", Port: 8389, Enabled: 1, CreatedTime: now,
					Settings: validSSSettings, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`},
				{ID: 13, KernelID: 1, NodeID: 1, Name: "bad-json", Protocol: "vless", Port: 8443, Enabled: 1, CreatedTime: now,
					Settings: `not-json{{{`, Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 1 {
		t.Fatalf("R5X-024: InboundsImported = %d, want 1 (only the valid row)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 3 {
		t.Fatalf("R5X-024: SkippedRows[inbounds] = %d, want 3 (bad SS key, bad Reality key, corrupt JSON)", result.SkippedRows["inbounds"])
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("R5X-024: Normalize-gate skips must be disclosed in warnings, got none")
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (10,11,13)"); n != 0 {
		t.Fatalf("R5X-024: invalid inbound rows must not land, got %d", n)
	}
	var good model.NodeInbound
	if err := dest.DB().Where("id = ?", 12).First(&good).Error; err != nil {
		t.Fatalf("valid inbound must land: %v", err)
	}
	if good.Settings != validSSSettings {
		t.Fatalf("R5X-024 防过修: valid settings must restore verbatim, got %q want %q", good.Settings, validSSSettings)
	}
}
