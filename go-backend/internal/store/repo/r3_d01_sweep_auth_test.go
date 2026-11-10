package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R3-D-01：恢复尾授权 sweep 与运行时 ensureTunnelPermission 判定
// 同源（共用 tunnelUseAuthorizedTx）。管理员（role_id=0）自有转发
// 从不落 user_tunnel 授权行、运行时恒放行——整库恢复时必须同样
// 存续；普通用户无有效授权的转发仍被 sweep（防修过头），停用授
// 权（status=0）按运行时同一口径不算授权。
func TestR3D01ImportSweepSharesRuntimeAuthorization(t *testing.T) {
	now := time.Now().UnixMilli()
	dest := openBackupRepo(t, "r3-d01-sweep-dst.db")

	// 目标库用户：1=管理员（role 0，open 时已自动种子 admin@user，
	// 先断言其角色以防种子口径漂移）、7/8=普通用户（role 1）。
	var seededAdmin model.User
	if err := dest.DB().Where("id = ?", 1).First(&seededAdmin).Error; err != nil {
		t.Fatalf("seeded admin id=1 must exist: %v", err)
	}
	if seededAdmin.RoleID != 0 {
		t.Fatalf("seeded admin role_id = %d, want 0", seededAdmin.RoleID)
	}
	createRow(t, dest, &model.User{ID: 7, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.User{ID: 8, User: "bob", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 5, Name: "tun-a", Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, dest, &model.Tunnel{ID: 6, Name: "tun-b", Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now})
	// alice 对 tun-a 有有效授权；bob 对 tun-b 只有停用授权（运行时
	// 不算授权）；其余组合无授权行——含管理员（恒无行）。
	createRow(t, dest, &model.UserTunnel{UserID: 7, TunnelID: 5, Num: 99999, Flow: 99999, Status: 1, ExpTime: now + 86400000})
	createRow(t, dest, &model.UserTunnel{UserID: 8, TunnelID: 6, Num: 99999, Flow: 99999, Status: 0, ExpTime: now + 86400000})

	mkFwd := func(id, userID int64, userName, name string, tunnelID int64) model.ForwardBackup {
		return model.ForwardBackup{ID: id, UserID: userID, UserName: userName, Name: name,
			TunnelID: tunnelID, RemoteAddr: "203.0.113.5:443", Strategy: "fifo", Status: 1,
			CreatedTime: now, UpdatedTime: now}
	}
	backup := &model.BackupData{
		Version: "1.0",
		Forwards: []model.ForwardBackup{
			mkFwd(101, 1, "admin", "admin-line-a", 5),   // 管理员自有、无授权行 → 必须存续
			mkFwd(102, 1, "admin", "admin-line-b", 6),   // 管理员自有、无授权行 → 必须存续
			mkFwd(103, 7, "alice", "alice-no-grant", 6), // 普通用户、无授权 → 必须被 sweep
			mkFwd(104, 7, "alice", "alice-granted", 5),  // 普通用户、有有效授权 → 必须存续
			mkFwd(105, 8, "bob", "bob-disabled", 6),     // 普通用户、仅停用授权 → 必须被 sweep
		},
	}
	result, err := dest.Import(backup, []string{"forwards"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	exists := func(id int64) bool {
		var count int64
		if err := dest.DB().Model(&model.Forward{}).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count > 0
	}
	for _, id := range []int64{101, 102, 104} {
		if !exists(id) {
			t.Fatalf("R3-D-01: forward %d must survive restore, got swept", id)
		}
	}
	for _, id := range []int64{103, 105} {
		if exists(id) {
			t.Fatalf("R3-D-01: forward %d must be swept (no effective grant), got restored", id)
		}
	}
	if got := result.SkippedRows["forwardsUnauthorized"]; got != 2 {
		t.Fatalf("R3-D-01: forwardsUnauthorized skipped = %d, want 2 (skipped=%v)", got, result.SkippedRows)
	}
	if result.ForwardsImported != 3 {
		t.Fatalf("R3-D-01: ForwardsImported = %d, want 3", result.ForwardsImported)
	}
	// 回执逐项可辨：被删两条各有一条点名 warning（含转发名＋所属
	// 隧道名）；存续三条不得出现在授权类 warning 中。
	named := map[string]bool{}
	for _, w := range result.ImportWarnings {
		if !strings.Contains(w, "无对应用户隧道授权") {
			continue
		}
		for _, want := range []string{"alice-no-grant", "bob-disabled"} {
			if strings.Contains(w, want) && strings.Contains(w, "tun-b") {
				named[want] = true
			}
		}
		for _, banned := range []string{"admin-line-a", "admin-line-b", "alice-granted"} {
			if strings.Contains(w, banned) {
				t.Fatalf("R3-D-01: surviving forward %q must not appear in authorization warnings: %q", banned, w)
			}
		}
	}
	for _, want := range []string{"alice-no-grant", "bob-disabled"} {
		if !named[want] {
			t.Fatalf("R3-D-01: swept forward %q must be named (with tunnel) in warnings, got %v", want, result.ImportWarnings)
		}
	}
}

// 同源谓词的运行时入口直测：UserTunnelUseAuthorized 必须与 sweep
// 在相同事实上给相同结论（管理员恒真、停用授权为假、无行为假、
// 有效授权为真）。
func TestR3D01UserTunnelUseAuthorizedPredicate(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "r3-d01-predicate.db")
	createRow(t, r, &model.User{ID: 7, User: "alice", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 5, Name: "tun-a", Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 6, Name: "tun-b", Type: 1, Protocol: "tls", Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.UserTunnel{UserID: 7, TunnelID: 5, Status: 1, ExpTime: now + 86400000})
	createRow(t, r, &model.UserTunnel{UserID: 7, TunnelID: 6, Status: 0, ExpTime: now + 86400000})

	cases := []struct {
		name     string
		userID   int64
		roleID   int
		tunnelID int64
		want     bool
	}{
		{"admin without grant row", 1, 0, 6, true},
		{"user with enabled grant", 7, 1, 5, true},
		{"user with disabled grant", 7, 1, 6, false},
		{"user without any grant", 7, 1, 999, false},
	}
	for _, c := range cases {
		got, err := r.UserTunnelUseAuthorized(c.userID, c.roleID, c.tunnelID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: UserTunnelUseAuthorized = %v, want %v", c.name, got, c.want)
		}
	}
}
