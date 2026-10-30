package repo

import (
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// T2-NEW-01 回归：无界 ID 集合塞单个 IN ? 越过 SQL 变量上限
// （SQLite 32766 / PostgreSQL 65535），备份导出在约 3.3 万用户时
// 整体失败（-2 too many SQL variables，真机实证），用户列表与通知
// 汇总共用同一配额查询、同路受累。修复后 40000 用户下导出/列表/
// 配额视图必须全部成功且内容等价（行数、顺序、抽样字段、配额透传）。

func TestEXPChunkInt64IDsShape(t *testing.T) {
	if got := chunkInt64IDs(nil); got != nil {
		t.Fatalf("nil input: got %v, want nil", got)
	}
	for _, tc := range []struct {
		n      int
		chunks []int
	}{
		{1, []int{1}},
		{499, []int{499}},
		{500, []int{500}},
		{501, []int{500, 1}},
		{1000, []int{500, 500}},
		{1234, []int{500, 500, 234}},
	} {
		ids := make([]int64, tc.n)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		chunks := chunkInt64IDs(ids)
		if len(chunks) != len(tc.chunks) {
			t.Fatalf("n=%d: %d chunks, want %d", tc.n, len(chunks), len(tc.chunks))
		}
		pos := 0
		for ci, want := range tc.chunks {
			if len(chunks[ci]) != want {
				t.Fatalf("n=%d chunk %d: len %d, want %d", tc.n, ci, len(chunks[ci]), want)
			}
			for _, v := range chunks[ci] {
				pos++
				if v != int64(pos) {
					t.Fatalf("n=%d: order broken at %d: got %d", tc.n, pos, v)
				}
			}
		}
	}
}

func TestEXPExportAndListsBeyondSQLiteVariableLimit(t *testing.T) {
	r := openBackupRepo(t, "exp.db")

	const bulk = 40000 // > SQLite 变量上限 32766，旧形态必失败
	users := make([]model.User, 0, bulk)
	for i := 0; i < bulk; i++ {
		users = append(users, model.User{
			User:          fmt.Sprintf("expbulk%06d", i),
			Pwd:           "x",
			RoleID:        2,
			ExpTime:       4102444800000,
			FlowResetTime: 0,
			Num:           1,
			CreatedTime:   1700000000000,
			Status:        1,
		})
	}
	if err := r.db.CreateInBatches(users, 500).Error; err != nil {
		t.Fatalf("bulk insert users: %v", err)
	}

	var totalUsers int64
	if err := r.db.Model(&model.User{}).Count(&totalUsers).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if totalUsers < bulk {
		t.Fatalf("seeded users = %d, want >= %d", totalUsers, bulk)
	}

	// 哨兵用户 + 配额行：验证分块合表后配额视图仍精确透传到导出。
	var sentinel model.User
	if err := r.db.Where("user = ?", "expbulk000123").First(&sentinel).Error; err != nil {
		t.Fatalf("load sentinel user: %v", err)
	}
	if err := r.db.Create(&model.UserQuota{
		UserID:         sentinel.ID,
		DailyLimitGB:   7,
		MonthlyLimitGB: 70,
		CreatedTime:    1700000000000,
		UpdatedTime:    1700000000000,
	}).Error; err != nil {
		t.Fatalf("insert sentinel quota: %v", err)
	}

	// 1) 配额视图：全量 ID（>32766）一次调用必须成功且哨兵精确。
	var allIDs []int64
	if err := r.db.Model(&model.User{}).Order("id ASC").Pluck("id", &allIDs).Error; err != nil {
		t.Fatalf("pluck ids: %v", err)
	}
	views, err := r.ListUserQuotaViewsByUserIDs(allIDs, time.Now())
	if err != nil {
		t.Fatalf("ListUserQuotaViewsByUserIDs(%d ids): %v", len(allIDs), err)
	}
	sv := views[sentinel.ID]
	if sv == nil {
		t.Fatalf("sentinel quota view missing among %d views", len(views))
	}
	if sv.DailyLimitGB != 7 || sv.MonthlyLimitGB != 70 {
		t.Fatalf("sentinel quota = %d/%d GB, want 7/70", sv.DailyLimitGB, sv.MonthlyLimitGB)
	}

	// 2) 备份导出（T2 失败本体）：必须成功、行数与库一致、id 升序
	// 跨块不乱、抽样字段与配额透传正确。
	backup, err := r.ExportAll()
	if err != nil {
		t.Fatalf("ExportAll with %d users: %v", totalUsers, err)
	}
	if int64(len(backup.Users)) != totalUsers {
		t.Fatalf("exported users = %d, want %d", len(backup.Users), totalUsers)
	}
	for i := 1; i < len(backup.Users); i++ {
		if backup.Users[i].ID <= backup.Users[i-1].ID {
			t.Fatalf("export order broken at %d: id %d after %d", i, backup.Users[i].ID, backup.Users[i-1].ID)
		}
	}
	var exportedSentinel *model.UserBackup
	for i := range backup.Users {
		if backup.Users[i].ID == sentinel.ID {
			exportedSentinel = &backup.Users[i]
			break
		}
	}
	if exportedSentinel == nil {
		t.Fatalf("sentinel user %d missing from export", sentinel.ID)
	}
	if exportedSentinel.User != "expbulk000123" || exportedSentinel.DailyQuotaGB != 7 || exportedSentinel.MonthlyQuotaGB != 70 {
		t.Fatalf("exported sentinel = {%q %d %d}, want {expbulk000123 7 70}",
			exportedSentinel.User, exportedSentinel.DailyQuotaGB, exportedSentinel.MonthlyQuotaGB)
	}

	// 3) 用户列表（同一配额查询的另一整表调用方）：必须成功且条数
	// 与 role_id != 0 的行数一致。
	items, err := r.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers with %d users: %v", totalUsers, err)
	}
	var nonAdmin int64
	if err := r.db.Model(&model.User{}).Where("role_id != ?", 0).Count(&nonAdmin).Error; err != nil {
		t.Fatalf("count non-admin: %v", err)
	}
	if int64(len(items)) != nonAdmin {
		t.Fatalf("ListUsers rows = %d, want %d", len(items), nonAdmin)
	}
}
