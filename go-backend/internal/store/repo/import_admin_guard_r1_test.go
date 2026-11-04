package repo

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R1-06-L1 回归：构造/异源 users 段备份不得经 upsert 把现行管理员
// 降权覆盖或让导入后全库无管理员——Import 须整单拒绝并回滚；正常
// 备份（含自家 id=1 管理员行）不得误报。

func r1GuardRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(t.TempDir() + "/r1-import-admin-guard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	// 以受控状态起步（不依赖 Open 是否播种）：清空后按需插入。
	if err := r.DB().Exec(`DELETE FROM user`).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func r1GuardInsertUser(t *testing.T, r *Repository, id int64, name string, roleID int) {
	t.Helper()
	err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(?, ?, ?, ?, 0, 100, 0, 0, 0, 10, 1000, 1000, 1)`,
		id, name, k4Bcrypt, roleID).Error
	if err != nil {
		t.Fatal(err)
	}
}

func r1GuardRole(t *testing.T, r *Repository, id int64) (int, bool) {
	t.Helper()
	var u model.User
	if err := r.DB().Where("id = ?", id).First(&u).Error; err != nil {
		return 0, false
	}
	return u.RoleID, true
}

func TestImportAdminGuardRejectsID1Demotion(t *testing.T) {
	r := r1GuardRepo(t)
	r1GuardInsertUser(t, r, 1, "admin", 0)
	now := time.Now().UnixMilli()
	backup := &model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 1, User: "boss", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
		// 即便备份另带一名其他 id 的管理员，id=1 降权覆盖仍须拒绝。
		{ID: 8, User: "otheradmin", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
	}}
	if _, err := r.Import(backup, []string{"users"}); err == nil || !strings.Contains(err.Error(), "id=1") {
		t.Fatalf("import demoting id=1 admin: err=%v, want named rejection mentioning id=1", err)
	}
	// 整单回滚：id=1 仍是管理员、id=8 未落库。
	if role, ok := r1GuardRole(t, r, 1); !ok || role != 0 {
		t.Fatalf("id=1 role after rejected import = %d (ok=%v), want 0", role, ok)
	}
	if _, ok := r1GuardRole(t, r, 8); ok {
		t.Fatalf("id=8 must not persist after whole-import rejection")
	}
}

func TestImportAdminGuardRejectsNoAdminOutcome(t *testing.T) {
	r := r1GuardRepo(t) // 目标库无任何管理员
	now := time.Now().UnixMilli()
	backup := &model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 5, User: "plain", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
	}}
	if _, err := r.Import(backup, []string{"users"}); err == nil || !strings.Contains(err.Error(), "管理员") {
		t.Fatalf("import leaving zero admins: err=%v, want named rejection", err)
	}
	if _, ok := r1GuardRole(t, r, 5); ok {
		t.Fatalf("id=5 must not persist after whole-import rejection")
	}
}

func TestImportAdminGuardAcceptsNormalBackups(t *testing.T) {
	r := r1GuardRepo(t)
	r1GuardInsertUser(t, r, 1, "admin", 0)
	now := time.Now().UnixMilli()

	// 自家备份形态：含 id=1 管理员行 + 普通用户行。
	res, err := r.Import(&model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 1, User: "admin", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
		{ID: 9, User: "member", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
	}}, []string{"users"})
	if err != nil {
		t.Fatalf("normal backup import: %v", err)
	}
	if res == nil || res.UsersImported != 2 {
		t.Fatalf("UsersImported = %v, want 2", res)
	}

	// 不触碰 id=1、只新增另一名管理员与普通用户：同样放行。
	res, err = r.Import(&model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 10, User: "admin2", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
		{ID: 11, User: "member2", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
	}}, []string{"users"})
	if err != nil {
		t.Fatalf("additive backup import: %v", err)
	}
	if res == nil || res.UsersImported != 2 {
		t.Fatalf("UsersImported = %v, want 2", res)
	}
	if role, ok := r1GuardRole(t, r, 1); !ok || role != 0 {
		t.Fatalf("id=1 role = %d (ok=%v), want 0", role, ok)
	}
}
