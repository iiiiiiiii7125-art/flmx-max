package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R6 批 3（备份导入矩阵收口）回归：R6X-007 / R6X-008 / R6X-009 / R6X-011。
// 全部用例先红后绿：修复前导入型入站 Extra 零校验逐字落库、forwards
// status=2 恢复成无侧表僵尸行、非法 status 原样落库、内容校验跳过
// 回执无行身份、伴生入站可经导入分配段落库。

func r6b3WarningsJoined(warnings []string) string {
	return strings.Join(warnings, "\n")
}

// R6X-008：备份恢复导入型入站（kernel_id=0）的 Extra 代理对象必须经
// 与 ParseImport 共用的同一谓词（kernel.ValidateImportedProxy）校验：
// 坏行整行跳过 + 计数 + warning；合法行逐字落库（校验只读不回写）。
// R6X-009：上述跳过的汇总 warning 必须附被跳过行身份 #{id} {name}
// 与错误摘要，且不得点名合法行。
func TestR6B3ImportExternalProxyGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r6b3-extgate.db")

	badVMessExtra := `{"type":"vmess","name":"bad-vmess-cipher","server":"203.0.113.1","port":10001,"uuid":"11111111-1111-1111-1111-111111111111","alterId":0,"cipher":"GARBAGE-CIPHER","udp":true}`
	badSSExtra := `{"type":"ss","name":"bad-ss-key","server":"203.0.113.2","port":10002,"cipher":"2022-blake3-aes-128-gcm","password":"plainpassword","udp":true}`
	// 防过修格：cipher 大写形态——渲染谓词大小写不敏感（ValidVMessCipher
	// 内含 ToLower），校验副本上的大小写愈合不得回写落库原值。
	goodVMessExtra := `{"type":"vmess","name":"good-vmess-uppercase","server":"203.0.113.3","port":10003,"uuid":"22222222-2222-2222-2222-222222222222","alterId":0,"cipher":"AES-128-GCM","udp":true}`
	goodSSExtra := `{"type":"ss","name":"good-ss","server":"203.0.113.4","port":10004,"cipher":"aes-256-gcm","password":"good-password","udp":true}`

	backup := &model.BackupData{
		Version: "1.0",
		Inbounds: &model.InboundsBackup{
			Inbounds: []model.InboundBackup{
				{ID: 20, KernelID: 0, NodeID: 0, Name: "bad-vmess-cipher", Protocol: "vmess", Listen: "203.0.113.1", Port: 10001, Extra: badVMessExtra, Enabled: 1, CreatedTime: now},
				{ID: 21, KernelID: 0, NodeID: 0, Name: "bad-ss-key", Protocol: "shadowsocks", Listen: "203.0.113.2", Port: 10002, Extra: badSSExtra, Enabled: 1, CreatedTime: now},
				{ID: 22, KernelID: 0, NodeID: 0, Name: "good-vmess-uppercase", Protocol: "vmess", Listen: "203.0.113.3", Port: 10003, Extra: goodVMessExtra, Enabled: 1, CreatedTime: now},
				{ID: 23, KernelID: 0, NodeID: 0, Name: "good-ss", Protocol: "shadowsocks", Listen: "203.0.113.4", Port: 10004, Extra: goodSSExtra, Enabled: 1, CreatedTime: now},
			},
		},
	}
	result, err := dest.Import(backup, []string{"inbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.InboundsImported != 2 {
		t.Fatalf("R6X-008: InboundsImported = %d, want 2 (only the two valid rows)", result.InboundsImported)
	}
	if result.SkippedRows["inbounds"] != 2 {
		t.Fatalf("R6X-008: SkippedRows[inbounds] = %d, want 2", result.SkippedRows["inbounds"])
	}
	if n := countWhere(t, dest, "node_inbound", "id IN (20,21)"); n != 0 {
		t.Fatalf("R6X-008: invalid imported-proxy rows must not land, got %d", n)
	}
	// 合法行逐字落库（含 cipher 大写形态，校验不得回写）。
	for id, want := range map[int64]string{22: goodVMessExtra, 23: goodSSExtra} {
		var row model.NodeInbound
		if err := dest.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatalf("valid inbound %d must land: %v", id, err)
		}
		if row.Extra != want {
			t.Fatalf("R6X-008 防过修: inbound %d Extra must restore verbatim, got %q want %q", id, row.Extra, want)
		}
	}
	// R6X-009：warning 附被跳过行身份 + 错误摘要，不点名合法行。
	joined := r6b3WarningsJoined(result.ImportWarnings)
	if !strings.Contains(joined, "#20 bad-vmess-cipher") || !strings.Contains(joined, "#21 bad-ss-key") {
		t.Fatalf("R6X-009: skip warning must carry skipped row identities, got %v", result.ImportWarnings)
	}
	if !strings.Contains(joined, "cipher") || !strings.Contains(joined, "SS2022") {
		t.Fatalf("R6X-009: skip warning must carry per-row error summaries, got %v", result.ImportWarnings)
	}
	if strings.Contains(joined, "good-vmess-uppercase") || strings.Contains(joined, "good-ss") {
		t.Fatalf("R6X-009: skip warning must not name valid rows, got %v", result.ImportWarnings)
	}
}

// R6X-007：备份 forwards 段 status 分格——∉{0,1,2} 逐行跳过 +
// skipped/warning；status=2 按批 3 裁定 (b) 归一 status=0（暂停、
// 列表可见、可经正常路径终删）+ warning；合法 0/1 逐字落库。
func TestR6B3ImportForwardsStatusGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r6b3-fwdstatus.db")
	createRow(t, dest, &model.User{ID: 7, User: "u1", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 1, Name: "t1", Type: 1, TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})

	ports70 := []model.ForwardPortBackup{{NodeID: 1, Port: 20001}}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			{ID: 70, UserID: 7, UserName: "u1", Name: "fwd-pending", TunnelID: 1, RemoteAddr: "10.0.0.9:80", Status: 2, CreatedTime: now, ForwardPorts: &ports70},
			{ID: 71, UserID: 7, UserName: "u1", Name: "fwd-bad-status", TunnelID: 1, RemoteAddr: "10.0.0.9:81", Status: 7, CreatedTime: now},
			{ID: 72, UserID: 7, UserName: "u1", Name: "fwd-active", TunnelID: 1, RemoteAddr: "10.0.0.9:82", Status: 1, CreatedTime: now},
			{ID: 73, UserID: 7, UserName: "u1", Name: "fwd-paused", TunnelID: 1, RemoteAddr: "10.0.0.9:83", Status: 0, CreatedTime: now},
			{ID: 74, UserID: 7, UserName: "u1", Name: "fwd-neg-status", TunnelID: 1, RemoteAddr: "10.0.0.9:84", Status: -1, CreatedTime: now},
		},
		// 转发落库须有对应 userTunnel 授权（R4X-006 事务尾收口），
		// 与真实备份同形态一并恢复。
		UserTunnels: []model.UserTunnelBackup{
			{ID: 1, UserID: 7, TunnelID: 1, Status: 1},
		},
	}
	result, err := dest.Import(backup, []string{"forwards", "userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.ForwardsImported != 3 {
		t.Fatalf("R6X-007: ForwardsImported = %d, want 3 (pending-normalized + active + paused)", result.ForwardsImported)
	}
	if result.SkippedRows["forwards"] != 2 {
		t.Fatalf("R6X-007: SkippedRows[forwards] = %d, want 2 (status 7 and -1)", result.SkippedRows["forwards"])
	}
	if n := countWhere(t, dest, "forward", "id IN (71,74)"); n != 0 {
		t.Fatalf("R6X-007: illegal-status forwards must not land, got %d", n)
	}
	// status=2 归一为 0：行落库、状态可见可删，不得以 2 落库，
	// 也不得产生任何侧表记账（裁定 (b) 不重建侧表）。
	var fwd model.Forward
	if err := dest.DB().Where("id = ?", 70).First(&fwd).Error; err != nil {
		t.Fatalf("R6X-007: pending-delete forward must land in normalized form: %v", err)
	}
	if fwd.Status != model.ForwardStatusPaused {
		t.Fatalf("R6X-007: status=2 must normalize to paused (0), got %d", fwd.Status)
	}
	if n := countWhere(t, dest, "nft_pending_delete", "forward_id = 70"); n != 0 {
		t.Fatalf("R6X-007: ruling (b) creates no side-table bookkeeping, got %d rows", n)
	}
	if n := countWhere(t, dest, "forward", "status NOT IN (0,1)"); n != 0 {
		t.Fatalf("R6X-007: no forward may land with status outside {0,1} after restore, got %d", n)
	}
	// 归一行必须出现在列表面（可见性下限）。
	list, err := dest.ListForwards()
	if err != nil {
		t.Fatalf("list forwards: %v", err)
	}
	found70 := false
	for _, row := range list {
		if id, _ := row["id"].(int64); id == 70 {
			found70 = true
		}
	}
	if !found70 {
		t.Fatalf("R6X-007: normalized forward 70 must be visible in ListForwards, got %v", list)
	}
	// 合法 0/1 逐字落库。
	for id, want := range map[int64]int{72: 1, 73: 0} {
		var st int
		if err := dest.DB().Raw(`SELECT status FROM forward WHERE id = ?`, id).Scan(&st).Error; err != nil {
			t.Fatalf("forward %d: %v", id, err)
		}
		if st != want {
			t.Fatalf("R6X-007 防过修: legal forward %d status = %d, want %d verbatim", id, st, want)
		}
	}
	joined := r6b3WarningsJoined(result.ImportWarnings)
	if !strings.Contains(joined, "fwd-pending") {
		t.Fatalf("R6X-007: status=2 normalization must be disclosed in warnings, got %v", result.ImportWarnings)
	}
	if !strings.Contains(joined, "status") {
		t.Fatalf("R6X-007: illegal status skips must be disclosed in warnings, got %v", result.ImportWarnings)
	}
}

// R6X-011：备份导入 userInbounds 段对计量伴生入站（CompanionOf>0）
// 整行跳过 + skipped 计数 + warning，与保存面「伴生不可分配」同
// 口径；合法 origin 分配逐字不变。
func TestR6B3ImportUserInboundsCompanionGate(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r6b3-uicomp.db")
	createRow(t, dest, &model.User{ID: 2, User: "u2", Pwd: r5b5Bcrypt, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 5, KernelID: 0, NodeID: 1, Name: "origin5", Protocol: "shadowsocks", Listen: "::", Port: 8388, Enabled: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.NodeInbound{ID: 6, KernelID: 0, NodeID: 1, Name: "origin5（计量）", Protocol: "shadowsocks", Listen: "::", Port: 8389, Enabled: 1, CompanionOf: 5, CreatedTime: now, UpdatedTime: now})

	backup := &model.BackupData{
		Version: "1.0",
		UserInbounds: []model.UserInboundBackup{
			{ID: 1, UserID: 2, InboundID: 6},
			{ID: 2, UserID: 2, InboundID: 5},
		},
	}
	result, err := dest.Import(backup, []string{"userInbounds"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.UserInboundsImported != 1 {
		t.Fatalf("R6X-011: UserInboundsImported = %d, want 1 (only the origin assignment)", result.UserInboundsImported)
	}
	if result.SkippedRows["userInbounds"] != 1 {
		t.Fatalf("R6X-011: SkippedRows[userInbounds] = %d, want 1", result.SkippedRows["userInbounds"])
	}
	if n := countWhere(t, dest, "user_inbound", "inbound_id = 6"); n != 0 {
		t.Fatalf("R6X-011: companion assignment must not land, got %d rows", n)
	}
	if n := countWhere(t, dest, "user_inbound", "inbound_id = 5 AND user_id = 2"); n != 1 {
		t.Fatalf("R6X-011 防过修: origin assignment must land verbatim, got %d rows", n)
	}
	if !strings.Contains(r6b3WarningsJoined(result.ImportWarnings), "伴生") {
		t.Fatalf("R6X-011: companion skip must be disclosed in warnings, got %v", result.ImportWarnings)
	}
}
