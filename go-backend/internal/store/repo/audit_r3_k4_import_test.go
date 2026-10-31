package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// Round 3 K4 回归：B03-001 用户侧分组写面非正 ID 守卫（Tx 层）、
// B02-001 备份导入用户名归一、B09-001 导入限速规则名长校验。

const k4Bcrypt = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ012"

func TestK4UserGroupTxRejectsNonPositiveIDs(t *testing.T) {
	r, err := Open(t.TempDir() + "/k4-groups.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'member', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO user_group(id, name, created_time, updated_time, status) VALUES(1, 'g-one', 1000, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`INSERT INTO user_group_user(user_group_id, user_id, created_time) VALUES(1, 2, 1000)`).Error; err != nil {
		t.Fatal(err)
	}
	membership := func() int64 {
		var n int64
		if err := r.DB().Model(&model.UserGroupUser{}).Where("user_id = ?", 2).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Replace 面：[-5] 与 [1,-5] 均整单拒绝且归属零变化。
	for _, ids := range [][]int64{{-5}, {1, -5}} {
		tx := r.DB().Begin()
		_, _, err := r.ReplaceUserGroupsByUserIDTx(tx, 2, ids, now)
		_ = tx.Rollback()
		if err == nil || !strings.Contains(err.Error(), "成员ID非法") {
			t.Fatalf("ReplaceUserGroupsByUserIDTx(%v): want 成员ID非法 rejection, got %v", ids, err)
		}
		if membership() != 1 {
			t.Fatalf("ReplaceUserGroupsByUserIDTx(%v) changed membership despite rejection", ids)
		}
	}
	// Add 面：[-5] 拒绝；对照 [1] 正常（已存在则 DoNothing）。
	tx := r.DB().Begin()
	if err := r.AddUserToGroupsTx(tx, 2, []int64{-5}, now); err == nil {
		_ = tx.Rollback()
		t.Fatalf("AddUserToGroupsTx([-5]): want rejection, got nil")
	}
	_ = tx.Rollback()
	tx = r.DB().Begin()
	if err := r.AddUserToGroupsTx(tx, 2, []int64{1}, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("AddUserToGroupsTx([1]) control: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if membership() != 1 {
		t.Fatalf("control add changed membership count unexpectedly: %d", membership())
	}
}

func TestK4ImportUsersUsernameNormalization(t *testing.T) {
	r, err := Open(t.TempDir() + "/k4-import-users.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(50, 'bob02', 'pwd', 1, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	run := func(rows []model.UserBackup) (int, *importReport, error) {
		rep := &importReport{skipped: map[string]int{}}
		tx := r.DB().Begin()
		n, err := importUsers(tx, rows, now, rep)
		if err != nil {
			_ = tx.Rollback()
			return n, rep, err
		}
		if err := tx.Commit().Error; err != nil {
			return n, rep, err
		}
		return n, rep, nil
	}
	storedName := func(id int64) (string, bool) {
		var u model.User
		err := r.DB().Where("id = ?", id).First(&u).Error
		if err != nil {
			return "", false
		}
		return u.User, true
	}

	// 带空格名：归一后落库。
	n, _, err := run([]model.UserBackup{{ID: 501, User: " Dave04 ", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now}})
	if err != nil || n != 1 {
		t.Fatalf("import ' Dave04 ': n=%d err=%v", n, err)
	}
	if name, ok := storedName(501); !ok || name != "Dave04" {
		t.Fatalf("stored username = %q (ok=%v), want trimmed 'Dave04'", name, ok)
	}
	// 带空格的大小写变体：归一后与存量 bob02 折叠判重冲突，整行报错不落库。
	if _, _, err := run([]model.UserBackup{{ID: 502, User: " BOB02 ", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now}}); err == nil {
		t.Fatalf("import ' BOB02 ' must be rejected by folded uniqueness after normalization")
	}
	if _, ok := storedName(502); ok {
		t.Fatalf("rejected variant leaked a row")
	}
	// 空名/纯空格名/超长名：逐行 skip+warn，不落库、不整单失败。
	for _, tc := range []struct {
		id   int64
		user string
	}{
		{503, ""},
		{504, "   "},
		{505, strings.Repeat("x", 101)},
	} {
		n, rep, err := run([]model.UserBackup{{ID: tc.id, User: tc.user, Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now}})
		if err != nil {
			t.Fatalf("import user id=%d: unexpected whole-import error: %v", tc.id, err)
		}
		if n != 0 || rep.skipped["users"] != 1 || len(rep.warnings) == 0 {
			t.Fatalf("import user id=%d: n=%d skipped=%d warnings=%d, want 0/1/>0", tc.id, n, rep.skipped["users"], len(rep.warnings))
		}
		if _, ok := storedName(tc.id); ok {
			t.Fatalf("skipped user id=%d leaked a row", tc.id)
		}
	}
}

func TestK4ImportSpeedLimitsNameLength(t *testing.T) {
	r, err := Open(t.TempDir() + "/k4-import-speed.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	now := time.Now().UnixMilli()
	rep := &importReport{skipped: map[string]int{}}
	tx := r.DB().Begin()
	n, err := importSpeedLimits(tx, []model.SpeedLimitBackup{
		{ID: 901, Name: strings.Repeat("L", 101), Speed: 42, Status: 1, CreatedTime: now},
		{ID: 902, Name: "ok-rule", Speed: 42, Status: 1, CreatedTime: now},
	}, now, rep)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("importSpeedLimits: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if n != 1 || rep.skipped["speedLimits"] != 1 {
		t.Fatalf("imported=%d skipped=%d, want 1/1", n, rep.skipped["speedLimits"])
	}
	foundWarn := false
	for _, w := range rep.warnings {
		if strings.Contains(w, "超过 100 字符") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("missing skip warning naming the length violation: %v", rep.warnings)
	}
	var cnt int64
	if err := r.DB().Model(&model.SpeedLimit{}).Where("id = ?", 901).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("overlong speed limit leaked a row")
	}
	if err := r.DB().Model(&model.SpeedLimit{}).Where("id = ?", 902).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("normal speed limit missing after import")
	}
}
