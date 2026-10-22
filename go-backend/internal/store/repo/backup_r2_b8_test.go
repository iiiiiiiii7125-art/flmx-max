package repo

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R2 批 8 回归：备份导入保真与引用（R2X-014/015/016/037/046/047）。
// 统一口径（verdicts §2.7）：身份引用不存在仍跳行计数；附属引用
// （限速/出站/来源/split 规则）不存在改剥离 + ImportWarnings，不跳整行。

func b8WarningsContain(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func b8PtrInt64(v int64) *int64 { return &v }

// R2X-014：部分恢复（备份含 speedLimits 段但未勾选）时 forwards /
// userTunnels 的悬空限速引用必须剥离为 NULL 并计入 ImportWarnings，
// 行本身照常 imported（红：三处悬空落库且回执全成功）。
func TestR2B8ImportPartialRestoreStripsDanglingSpeedRefs(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b8-speed-dangling.db")
	// 注：Open 会预置 id=1 的默认管理员，测试用户用 id=7 避让。
	createRow(t, dest, &model.User{ID: 7, User: "u7", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 10, UserID: 7, UserName: "u7", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1,
				SpeedID: b8PtrInt64(999), IPSpeedID: b8PtrInt64(888)},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 20, UserID: 7, TunnelID: 1, SpeedID: 777, Status: 1},
		},
		// 段在备份里、但本次未勾选（部分恢复）——引用仍须剥离。
		SpeedLimits: []model.SpeedLimitBackup{
			{ID: 999, Name: "ghost", Speed: 100, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 1 || result.UserTunnelsImported != 1 {
		t.Fatalf("rows themselves must be imported (strip, not skip): forwards=%d userTunnels=%d",
			result.ForwardsImported, result.UserTunnelsImported)
	}
	var fwd model.Forward
	if err := dest.DB().Where("id = ?", 10).First(&fwd).Error; err != nil {
		t.Fatalf("forward row: %v", err)
	}
	if fwd.SpeedID.Valid || fwd.IPSpeedID.Valid {
		t.Fatalf("R2X-014: dangling speed refs must be stripped to NULL, got speed_id=%v ip_speed_id=%v", fwd.SpeedID, fwd.IPSpeedID)
	}
	var ut model.UserTunnel
	if err := dest.DB().Where("id = ?", 20).First(&ut).Error; err != nil {
		t.Fatalf("user tunnel row: %v", err)
	}
	if ut.SpeedID.Valid {
		t.Fatalf("R2X-014: dangling user_tunnel speed ref must be stripped to NULL, got %v", ut.SpeedID)
	}
	if !b8WarningsContain(result.ImportWarnings, "限速") {
		t.Fatalf("R2X-014: stripping must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R2X-014 对照（防段序误杀）：全量恢复（含 speedLimits 段）时合法
// 限速绑定必须保留并指向恢复出的限速行——清扫在全部段之后做，
// 不受 forwards→speedLimits 段序影响。
func TestR2B8ImportFullRestoreKeepsSpeedRefs(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b8-speed-full.db")
	backup := &model.BackupData{
		Version: "1.0",
		Users: []model.UserBackup{
			{ID: 1, User: "u1", Pwd: "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012", RoleID: 1, Status: 1, CreatedTime: now},
		},
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now},
		},
		SpeedLimits: []model.SpeedLimitBackup{
			{ID: 5, Name: "lim", Speed: 100, Status: 1, CreatedTime: now},
		},
		Forwards: []model.ForwardBackup{
			{ID: 10, UserID: 1, UserName: "u1", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1,
				SpeedID: b8PtrInt64(5), IPSpeedID: b8PtrInt64(5)},
		},
		UserTunnels: []model.UserTunnelBackup{
			{ID: 20, UserID: 1, TunnelID: 1, SpeedID: 5, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"users", "tunnels", "forwards", "userTunnels", "speedLimits"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.SpeedLimitsImported != 1 {
		t.Fatalf("speed limit must be restored, got %d", result.SpeedLimitsImported)
	}
	var fwd model.Forward
	if err := dest.DB().Where("id = ?", 10).First(&fwd).Error; err != nil {
		t.Fatalf("forward row: %v", err)
	}
	if !fwd.SpeedID.Valid || fwd.SpeedID.Int64 != 5 || !fwd.IPSpeedID.Valid || fwd.IPSpeedID.Int64 != 5 {
		t.Fatalf("full restore must keep valid speed refs pointing at restored row 5, got %v / %v", fwd.SpeedID, fwd.IPSpeedID)
	}
	var ut model.UserTunnel
	if err := dest.DB().Where("id = ?", 20).First(&ut).Error; err != nil {
		t.Fatalf("user tunnel row: %v", err)
	}
	if !ut.SpeedID.Valid || ut.SpeedID.Int64 != 5 {
		t.Fatalf("full restore must keep user_tunnel speed ref 5, got %v", ut.SpeedID)
	}
}

// R2X-015：入站导入后置校验——source_id/outbound_id 悬空置 0 并计
// warning；splitRules 只剔除指向无效 outbound 的规则；解析失败的
// splitRules 原样保留；同批后序入站引用与同批 source 引用保留。
func TestR2B8ImportInboundStripsDanglingRefs(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b8-inbound-refs.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})

	split := `[{"action":"proxy","outboundId":30},{"action":"proxy","outboundId":888},{"action":"direct"}]`
	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Sources: []model.RemoteSourceBackup{
				{ID: 7, Name: "src", URL: "https://example.com/sub", Kind: "url", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 10, KernelID: 1, NodeID: 1, Name: "dangling", Protocol: "vless", Port: 443, Enabled: 1,
					CreatedTime: now, SourceID: 999, OutboundID: 888, SplitRules: split},
				{ID: 11, KernelID: 1, NodeID: 1, Name: "validrefs", Protocol: "vless", Port: 8443, Enabled: 1,
					CreatedTime: now, SourceID: 7, OutboundID: 30},
				{ID: 12, KernelID: 1, NodeID: 1, Name: "badsplit", Protocol: "vless", Port: 9443, Enabled: 1,
					CreatedTime: now, SplitRules: "not-json{{{"},
				{ID: 30, KernelID: 1, NodeID: 1, Name: "target", Protocol: "trojan", Port: 10443, Enabled: 1,
					CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var in10 model.NodeInbound
	if err := dest.DB().Where("id = ?", 10).First(&in10).Error; err != nil {
		t.Fatalf("inbound 10: %v", err)
	}
	if in10.SourceID != 0 || in10.OutboundID != 0 {
		t.Fatalf("R2X-015: dangling source/outbound must be zeroed, got source=%d outbound=%d", in10.SourceID, in10.OutboundID)
	}
	var rules []struct {
		Action     string `json:"action"`
		OutboundID int64  `json:"outboundId"`
	}
	if err := json.Unmarshal([]byte(in10.SplitRules), &rules); err != nil {
		t.Fatalf("R2X-015: splitRules must stay valid JSON after pruning, got %q: %v", in10.SplitRules, err)
	}
	if len(rules) != 2 || rules[0].OutboundID != 30 || rules[1].Action != "direct" {
		t.Fatalf("R2X-015: splitRules must keep only the valid-outbound rule and the direct rule, got %+v (raw %q)", rules, in10.SplitRules)
	}
	var in11 model.NodeInbound
	if err := dest.DB().Where("id = ?", 11).First(&in11).Error; err != nil {
		t.Fatalf("inbound 11: %v", err)
	}
	if in11.SourceID != 7 || in11.OutboundID != 30 {
		t.Fatalf("same-batch references (source 7, later inbound 30) must be preserved, got source=%d outbound=%d", in11.SourceID, in11.OutboundID)
	}
	var in12 model.NodeInbound
	if err := dest.DB().Where("id = ?", 12).First(&in12).Error; err != nil {
		t.Fatalf("inbound 12: %v", err)
	}
	if in12.SplitRules != "not-json{{{" {
		t.Fatalf("unparseable splitRules must be preserved as-is, got %q", in12.SplitRules)
	}
	if !b8WarningsContain(result.ImportWarnings, "入站") {
		t.Fatalf("R2X-015: stripping must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R2X-016：TrafficRatio=0 恢复保真——插入分支与更新分支都必须落 0
// （红：两分支均被 default:1.0 吞为 1）；对照 2.5 原样往返。
func TestR2B8ImportTunnelTrafficRatioZero(t *testing.T) {
	now := time.Now().UnixMilli()
	zeroBackup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "free", TrafficRatio: 0, Status: 1, CreatedTime: now},
		},
	}

	// 插入分支（空库）。
	destInsert := openBackupRepo(t, "b8-ratio-insert.db")
	if _, err := destInsert.Import(zeroBackup, []string{"tunnels"}); err != nil {
		t.Fatalf("import insert: %v", err)
	}
	var tun model.Tunnel
	if err := destInsert.DB().Where("id = ?", 1).First(&tun).Error; err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	if tun.TrafficRatio != 0 {
		t.Fatalf("R2X-016 insert branch: TrafficRatio = %v, want 0", tun.TrafficRatio)
	}

	// 更新分支（目标库原值 2）。
	destUpdate := openBackupRepo(t, "b8-ratio-update.db")
	createRow(t, destUpdate, &model.Tunnel{ID: 1, Name: "paid", TrafficRatio: 2, Status: 1, CreatedTime: now, UpdatedTime: now})
	if _, err := destUpdate.Import(zeroBackup, []string{"tunnels"}); err != nil {
		t.Fatalf("import update: %v", err)
	}
	if err := destUpdate.DB().Where("id = ?", 1).First(&tun).Error; err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	if tun.TrafficRatio != 0 {
		t.Fatalf("R2X-016 update branch: TrafficRatio = %v, want 0", tun.TrafficRatio)
	}

	// 对照：2.5 原样往返。
	destCtl := openBackupRepo(t, "b8-ratio-ctl.db")
	ctlBackup := &model.BackupData{
		Version: "1.0",
		Tunnels: []model.TunnelBackup{
			{ID: 1, Name: "half", TrafficRatio: 2.5, Status: 1, CreatedTime: now},
		},
	}
	if _, err := destCtl.Import(ctlBackup, []string{"tunnels"}); err != nil {
		t.Fatalf("import control: %v", err)
	}
	if err := destCtl.DB().Where("id = ?", 1).First(&tun).Error; err != nil {
		t.Fatalf("tunnel: %v", err)
	}
	if tun.TrafficRatio != 2.5 {
		t.Fatalf("control: TrafficRatio = %v, want 2.5", tun.TrafficRatio)
	}
}

// R2X-037：grant 组键强制取父 permission 组键——bogus 自带值不得
// 落库（红：999/888 原样落库）；同 id 正确组重导入须能纠正存量
// 错误行（红：DoUpdates 不含组键，永不更新）；不一致计 warning。
func TestR2B8ImportPermissionGrantGroupKeysForcedToParent(t *testing.T) {
	now := time.Now().UnixMilli()
	seedPermDeps := func(t *testing.T, dest *Repository) {
		t.Helper()
		createRow(t, dest, &model.UserGroup{ID: 4, Name: "ug", Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, dest, &model.TunnelGroup{ID: 3, Name: "tg", Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, dest, &model.UserTunnel{ID: 20, UserID: 1, TunnelID: 1, Status: 1})
	}

	// bogus 组 grant → 落库组键 = 父组键 (4,3)，零 999/888 行。
	dest := openBackupRepo(t, "b8-grant-bogus.db")
	seedPermDeps(t, dest)
	backup := &model.BackupData{
		Version: "1.0",
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 4, TunnelGroupID: 3, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 999, TunnelGroupID: 888, UserTunnelID: 20, CreatedTime: now, CreatedByGroup: 1},
			}},
		},
	}
	result, err := dest.Import(backup, []string{"permissions"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var grant model.GroupPermissionGrant
	if err := dest.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant row must land: %v", err)
	}
	if grant.UserGroupID != 4 || grant.TunnelGroupID != 3 {
		t.Fatalf("R2X-037: grant group keys must be forced to parent (4,3), got (%d,%d)", grant.UserGroupID, grant.TunnelGroupID)
	}
	if n := countWhere(t, dest, "group_permission_grant", "user_group_id = ? OR tunnel_group_id = ?", 999, 888); n != 0 {
		t.Fatalf("R2X-037: bogus group rows must not exist, got %d", n)
	}
	if len(result.ImportWarnings) == 0 {
		t.Fatalf("R2X-037: mismatched grant group keys must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}

	// 存量错误行（同 id、bogus 组）+ 正确组重导入 → 被纠正。
	dest2 := openBackupRepo(t, "b8-grant-correct.db")
	seedPermDeps(t, dest2)
	createRow(t, dest2, &model.GroupPermission{ID: 50, UserGroupID: 4, TunnelGroupID: 3, CreatedTime: now})
	createRow(t, dest2, &model.GroupPermissionGrant{ID: 60, UserGroupID: 999, TunnelGroupID: 888, UserTunnelID: 20, CreatedTime: now, CreatedByGroup: 1})
	goodBackup := &model.BackupData{
		Version: "1.0",
		Permissions: []model.PermissionBackup{
			{ID: 50, UserGroupID: 4, TunnelGroupID: 3, CreatedTime: now, Grants: []model.PermissionGrantBackup{
				{ID: 60, UserGroupID: 4, TunnelGroupID: 3, UserTunnelID: 20, CreatedTime: now, CreatedByGroup: 1},
			}},
		},
	}
	if _, err := dest2.Import(goodBackup, []string{"permissions"}); err != nil {
		t.Fatalf("reimport: %v", err)
	}
	if err := dest2.DB().Where("id = ?", 60).First(&grant).Error; err != nil {
		t.Fatalf("grant row: %v", err)
	}
	if grant.UserGroupID != 4 || grant.TunnelGroupID != 3 {
		t.Fatalf("R2X-037: re-import with correct groups must correct the stored row, got (%d,%d)", grant.UserGroupID, grant.TunnelGroupID)
	}
}

// R2X-046：内核行替换（同节点同类型、新 id）前须把备份外存量入站
// 的 kernel_id 重指向新行（红：旧行裸删、入站悬空、列表类型为空）；
// 备份内入站照常导入到新内核。
func TestR2B8ImportKernelReplacementRepointsInbounds(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "b8-kernel-repoint.db")
	createRow(t, dest, &model.Node{ID: 1, Name: "n1", Secret: "s", ServerIP: "10.0.0.1", Port: "10000-10100",
		CreatedTime: now, UpdatedTime: sql.NullInt64{Int64: now, Valid: true}})
	createRow(t, dest, &model.NodeKernel{ID: 1, NodeID: 1, KernelType: "xray", Version: "latest", Status: "ready",
		Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 10, KernelID: 1, NodeID: 1, Name: "outside", Protocol: "vless",
		Listen: "::", Port: 443, Enabled: 1, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Kernels: []model.KernelBackup{
				{ID: 2, NodeID: 1, KernelType: "xray", Version: "latest", Enabled: 1, CreatedTime: now},
			},
			Inbounds: []model.InboundBackup{
				{ID: 11, KernelID: 2, NodeID: 1, Name: "inside", Protocol: "vless", Port: 8443, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var outside model.NodeInbound
	if err := dest.DB().Where("id = ?", 10).First(&outside).Error; err != nil {
		t.Fatalf("outside inbound must survive kernel replacement: %v", err)
	}
	if outside.KernelID != 2 {
		t.Fatalf("R2X-046: outside inbound kernel_id = %d, want repointed to new kernel 2", outside.KernelID)
	}
	// 列表 kernelType 口径：kernel_id 必须解析到真实内核行且类型非空。
	var kernel model.NodeKernel
	if err := dest.DB().Where("id = ?", outside.KernelID).First(&kernel).Error; err != nil {
		t.Fatalf("R2X-046: repointed kernel must resolve: %v", err)
	}
	if kernel.KernelType != "xray" {
		t.Fatalf("R2X-046: resolved kernelType = %q, want xray", kernel.KernelType)
	}
	var inside model.NodeInbound
	if err := dest.DB().Where("id = ?", 11).First(&inside).Error; err != nil {
		t.Fatalf("backup inbound must import as usual: %v", err)
	}
	if inside.KernelID != 2 {
		t.Fatalf("backup inbound kernel_id = %d, want 2", inside.KernelID)
	}
	if !b8WarningsContain(result.ImportWarnings, "内核") {
		t.Fatalf("R2X-046: repoint count must be counted in ImportWarnings, got %v", result.ImportWarnings)
	}
}

// R2X-047（单元）：FilterBackupConfigs 精确排除日报运行时两键；
// pending_realm_settle:* 与 notify_daily_hour 必须豁免（禁止前缀匹配）。
func TestR2B8FilterBackupConfigsExcludesDailyRuntimeKeys(t *testing.T) {
	filtered := FilterBackupConfigs(map[string]string{
		"notify_daily_last":          "2026-10-21",
		"notify_daily_inbound_total": "12345",
		"notify_daily_hour":          "9",
		"pending_realm_settle:7":     `{"nodeId":7}`,
		"app_name":                   "FLMX",
	})
	if _, ok := filtered["notify_daily_last"]; ok {
		t.Fatalf("R2X-047: notify_daily_last must be excluded from backups, got %v", filtered)
	}
	if _, ok := filtered["notify_daily_inbound_total"]; ok {
		t.Fatalf("R2X-047: notify_daily_inbound_total must be excluded from backups, got %v", filtered)
	}
	if filtered["notify_daily_hour"] != "9" {
		t.Fatalf("R2X-047: notify_daily_hour is a config key and must stay, got %v", filtered)
	}
	if filtered["pending_realm_settle:7"] != `{"nodeId":7}` {
		t.Fatalf("R2X-047: pending_realm_settle:* must be exempted, got %v", filtered)
	}
	if filtered["app_name"] != "FLMX" {
		t.Fatalf("ordinary keys must stay, got %v", filtered)
	}
}

// R2X-047（往返）：导出不带日报两键；旧备份带日报键导入不得回退
// 目标库现值；pending 键照常恢复。
func TestR2B8BackupConfigsDailyKeysRoundTrip(t *testing.T) {
	now := time.Now().UnixMilli()
	source := openBackupRepo(t, "b8-configs-src.db")
	for name, value := range map[string]string{
		"notify_daily_last":          "2026-10-21",
		"notify_daily_inbound_total": "12345",
		"notify_daily_hour":          "9",
		"pending_realm_settle:7":     `{"nodeId":7,"direction":"realm->gost"}`,
		"app_name":                   "FLMX",
	} {
		if err := source.UpsertConfig(name, value, now); err != nil {
			t.Fatalf("seed config %s: %v", name, err)
		}
	}
	exported, err := source.ExportAll()
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, ok := exported.Configs["notify_daily_last"]; ok {
		t.Fatalf("R2X-047: export must not carry notify_daily_last")
	}
	if _, ok := exported.Configs["notify_daily_inbound_total"]; ok {
		t.Fatalf("R2X-047: export must not carry notify_daily_inbound_total")
	}
	if exported.Configs["pending_realm_settle:7"] == "" || exported.Configs["notify_daily_hour"] != "9" {
		t.Fatalf("R2X-047: export must keep pending_realm_settle:7 and notify_daily_hour, got %v", exported.Configs)
	}

	dest := openBackupRepo(t, "b8-configs-dst.db")
	if err := dest.UpsertConfig("notify_daily_last", "2026-10-22", now); err != nil {
		t.Fatalf("seed dest daily_last: %v", err)
	}
	if err := dest.UpsertConfig("notify_daily_inbound_total", "99999", now); err != nil {
		t.Fatalf("seed dest daily total: %v", err)
	}
	oldBackup := &model.BackupData{
		Version: "1.0",
		Configs: map[string]string{
			"notify_daily_last":          "2020-01-01",
			"notify_daily_inbound_total": "1",
			"notify_daily_hour":          "8",
			"pending_realm_settle:7":     `{"nodeId":7,"direction":"realm->gost"}`,
		},
	}
	if _, err := dest.Import(oldBackup, []string{"configs"}); err != nil {
		t.Fatalf("import old backup: %v", err)
	}
	getCfg := func(name string) string {
		t.Helper()
		cfg, err := dest.GetConfigByName(name)
		if err != nil || cfg == nil {
			return ""
		}
		return cfg.Value
	}
	if got := getCfg("notify_daily_last"); got != "2026-10-22" {
		t.Fatalf("R2X-047: old backup must not roll back notify_daily_last, got %q", got)
	}
	if got := getCfg("notify_daily_inbound_total"); got != "99999" {
		t.Fatalf("R2X-047: old backup must not roll back notify_daily_inbound_total, got %q", got)
	}
	if got := getCfg("pending_realm_settle:7"); got == "" {
		t.Fatalf("R2X-047: pending_realm_settle:7 must be restored from old backup")
	}
	if got := getCfg("notify_daily_hour"); got != "8" {
		t.Fatalf("R2X-047: notify_daily_hour must import normally, got %q", got)
	}
}
