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

// R5X-009：users 段 status {0,1} 枚举门——status=2/-1 逐行跳过 +
// skipped/warning，合法 0/1 逐字落库（分组两段的同门已随分组
// 三件套于 1.9.6 整套移除）。
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
	}
	result, err := dest.Import(backup, []string{"users"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UsersImported != 2 {
		t.Fatalf("R5X-009: imported counts = users %d, want 2", result.UsersImported)
	}
	if result.SkippedRows["users"] != 2 {
		t.Fatalf("R5X-009: skipped = %v, want users 2", result.SkippedRows)
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
			{ID: 1, Name: "t1", TrafficRatio: 1, Flow: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
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

	// (c) 已移除段披露：旧备份携带的分组三件套三段不落库（表已不
	// 建），但必须在 ImportWarnings 中逐段点名计数，不许静默丢。
	destC := openBackupRepo(t, "r5b5-removed-segments.db")
	backupC := &model.BackupData{
		Version: "1.0",
		TunnelGroups: []model.TunnelGroupBackup{
			{ID: 1, Name: "tg", Status: 1, CreatedTime: now, Tunnels: []int64{1}},
		},
		UserGroups: []model.UserGroupBackup{
			{ID: 1, Name: "ug", Status: 1, CreatedTime: now, Users: []int64{2}},
		},
		Permissions: []model.PermissionBackup{
			{ID: 1, UserGroupID: 1, TunnelGroupID: 1, CreatedTime: now},
		},
	}
	resultC, err := destC.Import(backupC, []string{"users"})
	if err != nil {
		t.Fatalf("import with removed segments: %v", err)
	}
	for _, seg := range []string{"tunnelGroups", "userGroups", "permissions"} {
		if !r5b5WarningsContain(resultC.ImportWarnings, seg) {
			t.Fatalf("removed segment %s must be named in import warnings, got %v", seg, resultC.ImportWarnings)
		}
	}
	// RR-S2-01：三段不落库的条数改以 *Ignored 结构化字段据实回执
	// （各 1 条，与上方 ImportWarnings 的文本披露同数）；旧
	// *Imported 字段名实相反且恒为 0，已整族改名删除。
	if resultC.TunnelGroupsIgnored != 1 || resultC.UserGroupsIgnored != 1 || resultC.PermissionsIgnored != 1 {
		t.Fatalf("removed segments must be receipted as ignored counts 1/1/1, got %d/%d/%d",
			resultC.TunnelGroupsIgnored, resultC.UserGroupsIgnored, resultC.PermissionsIgnored)
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
			{ID: 1, Name: "t1", TrafficRatio: 1, Flow: 1, Status: 1, CreatedTime: now, ChainTunnels: []model.ChainTunnelBackup{
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
