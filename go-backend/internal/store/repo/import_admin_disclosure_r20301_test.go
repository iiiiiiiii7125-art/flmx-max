package repo

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// R2-03-01 回归：备份导入扩大管理员集合时必须在 ImportWarnings 中
// 点名披露——以新 id 新建 role_id=0 行、或把存量非管理员行提升为
// role_id=0，都不得只体现在 UsersImported 计数里；不扩大管理员
// 集合的合法导入不得误报。

func r20301WarningNaming(res *ImportResult, id int64, username string) bool {
	if res == nil {
		return false
	}
	needle := fmt.Sprintf("用户 #%d（%q）", id, username)
	for _, warning := range res.ImportWarnings {
		if strings.Contains(warning, needle) && strings.Contains(warning, "管理员") {
			return true
		}
	}
	return false
}

func TestImportAdminDisclosureWarnsForNewAdmin(t *testing.T) {
	r := r1GuardRepo(t)
	r1GuardInsertUser(t, r, 1, "admin", 0)
	now := time.Now().UnixMilli()

	res, err := r.Import(&model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 1, User: "admin", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
		{ID: 12, User: "second-admin", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
		{ID: 13, User: "member", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
	}}, []string{"users"})
	if err != nil {
		t.Fatalf("import with a new admin: %v", err)
	}
	if !r20301WarningNaming(res, 12, "second-admin") {
		t.Fatalf("ImportWarnings = %v, want one naming new admin #12 (second-admin)", res.ImportWarnings)
	}
	if r20301WarningNaming(res, 1, "admin") || r20301WarningNaming(res, 13, "member") {
		t.Fatalf("ImportWarnings = %v, must not name the pre-existing admin or the ordinary user", res.ImportWarnings)
	}
}

func TestImportAdminDisclosureWarnsForPromotedAdmin(t *testing.T) {
	r := r1GuardRepo(t)
	r1GuardInsertUser(t, r, 1, "admin", 0)
	r1GuardInsertUser(t, r, 20, "promoted", 1)
	now := time.Now().UnixMilli()

	res, err := r.Import(&model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 20, User: "promoted", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
	}}, []string{"users"})
	if err != nil {
		t.Fatalf("import promoting an existing user: %v", err)
	}
	if !r20301WarningNaming(res, 20, "promoted") {
		t.Fatalf("ImportWarnings = %v, want one naming promoted admin #20 (promoted)", res.ImportWarnings)
	}
}

func TestImportAdminDisclosureSilentWhenAdminSetUnchanged(t *testing.T) {
	r := r1GuardRepo(t)
	r1GuardInsertUser(t, r, 1, "admin", 0)
	now := time.Now().UnixMilli()

	res, err := r.Import(&model.BackupData{Version: "1.0", Users: []model.UserBackup{
		{ID: 1, User: "admin", Pwd: k4Bcrypt, RoleID: 0, Status: 1, CreatedTime: now},
		{ID: 21, User: "ordinary", Pwd: k4Bcrypt, RoleID: 1, Status: 1, CreatedTime: now},
	}}, []string{"users"})
	if err != nil {
		t.Fatalf("ordinary import: %v", err)
	}
	for _, warning := range res.ImportWarnings {
		if strings.Contains(warning, "管理员") {
			t.Fatalf("ImportWarnings = %v, must not warn about admins when the admin set is unchanged", res.ImportWarnings)
		}
	}
}
