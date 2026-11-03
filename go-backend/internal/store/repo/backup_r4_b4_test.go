package repo

import (
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R4 批 4 回归（repo 导入面）：
//   - R4X-006：导入事务尾只保留转发授权收口——forwards 须验 UT
//     授权存否；userTunnels 逐字落行、无任何撤销裁定。
// （分组三件套已于 1.9.6 整套移除：原「停用对覆盖」探针前提与
// G-S02 importPermissions grants 校验两族随功能删除。）

// ─── R4X-006 事务尾收口 ─────────────────────────────────────────────────

// b4SeedRevokedState 造基础形态（用户 + 两隧道、无任何 UT），供
// 探针 C（授权逐字落行）与探针 D（无授权转发）共用。
func b4SeedRevokedState(t *testing.T, r *Repository, now int64) {
	t.Helper()
	createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
	createRow(t, r, &model.Tunnel{ID: 2, Name: "t2", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
}

// 探针 C：部分导入 userTunnels 时授权一律逐字落行——无跳过、
// 无撤销 skipped（导入的即是现行授权）。
func TestR4B4_006_PartialUserTunnelsLandsVerbatim(t *testing.T) {
	now := time.Now().UnixMilli()
	r := openBackupRepo(t, "b4-imp-ut.db")
	b4SeedRevokedState(t, r, now)

	backup := &model.BackupData{
		Version: "1.0",
		UserTunnels: []model.UserTunnelBackup{
			{ID: 10, UserID: 2, TunnelID: 1, Status: 1},
			{ID: 11, UserID: 2, TunnelID: 2, Status: 1},
		},
	}
	result, err := r.Import(backup, []string{"userTunnels"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 10); n != 1 {
		t.Fatalf("user tunnel must land verbatim, got %d rows", n)
	}
	if n := countWhere(t, r, "user_tunnel", "id = ?", 11); n != 1 {
		t.Fatalf("second user tunnel must import as usual, got %d", n)
	}
	if result.UserTunnelsImported != 2 {
		t.Fatalf("both UTs must count as imported, got %d", result.UserTunnelsImported)
	}
	if result.SkippedRows["userTunnelsRevoked"] != 0 {
		t.Fatalf("no revoked skip may be counted, got %v", result.SkippedRows)
	}
}

// 探针 D 转绿：部分导入 forwards 须验对应 UT 授权存否——无 UT 的
// 转发不落行（skipped + warning）；有 UT（存量或同批）时照常落行。
func TestR4B4_006_PartialForwardsRequireUserTunnel(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("no UT in target or batch: forward skipped", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-none.db")
		b4SeedRevokedState(t, r, now)
		backup := &model.BackupData{
			Version:  "1.0",
			Forwards: []model.ForwardBackup{{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1}},
		}
		result, err := r.Import(backup, []string{"forwards"})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 0 {
			t.Fatalf("R4X-006 probe D: forward without a user_tunnel authorization must not land, got %d rows", n)
		}
		if result.ForwardsImported != 0 {
			t.Fatalf("imported count must exclude the tail-skipped forward, got %d", result.ForwardsImported)
		}
		if len(result.ImportWarnings) == 0 {
			t.Fatalf("tail skip must be counted in ImportWarnings, got %v", result.ImportWarnings)
		}
	})

	t.Run("UT in same batch: forward lands (segment order must not misfire)", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-batch.db")
		createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
		createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
		backup := &model.BackupData{
			Version: "1.0",
			Forwards: []model.ForwardBackup{
				{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1},
			},
			UserTunnels: []model.UserTunnelBackup{
				{ID: 10, UserID: 2, TunnelID: 1, Status: 1},
			},
		}
		result, err := r.Import(backup, []string{"forwards", "userTunnels"})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 1 {
			t.Fatalf("forward whose UT is restored in the same batch must land, got %d", n)
		}
		if result.ForwardsImported != 1 || result.UserTunnelsImported != 1 {
			t.Fatalf("same-batch counts must be intact, forwards=%d userTunnels=%d", result.ForwardsImported, result.UserTunnelsImported)
		}
	})

	t.Run("UT pre-existing in target: forward lands", func(t *testing.T) {
		r := openBackupRepo(t, "b4-imp-fwd-pre.db")
		createRow(t, r, &model.User{ID: 2, User: "u2", Pwd: "x", RoleID: 1, Status: 1, CreatedTime: now})
		createRow(t, r, &model.Tunnel{ID: 1, Name: "t1", TrafficRatio: 1, Status: 1, CreatedTime: now, UpdatedTime: now})
		createRow(t, r, &model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Status: 1})
		backup := &model.BackupData{
			Version:  "1.0",
			Forwards: []model.ForwardBackup{{ID: 20, UserID: 2, UserName: "u2", Name: "f1", TunnelID: 1, RemoteAddr: "1.2.3.4:80", Status: 1}},
		}
		if _, err := r.Import(backup, []string{"forwards"}); err != nil {
			t.Fatalf("import: %v", err)
		}
		if n := countWhere(t, r, "forward", "id = ?", 20); n != 1 {
			t.Fatalf("forward with a pre-existing UT authorization must land, got %d", n)
		}
	})
}

// 全量恢复终态不变（逐行对账）：停用组 + 其成员的手工 UT（无
// grant）在整表恢复后必须原样存活；组建 UT + grant + forward
// 同理（R5-FGRP 后导入对授权行一律逐字落行，本例守恒不变）。

// ─── G-S02 顺查 ─────────────────────────────────────────────────────────

// grants 成员对应校验：UT 的 (user,tunnel) 与权限对成员对应关系
// 不成立时计 warning（写回仍按父组键落库，与 R2X-037 口径兼容；
// 红：无任何对应关系提示）。
