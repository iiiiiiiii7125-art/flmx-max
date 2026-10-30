package repo

import (
	"testing"

	"go-backend/internal/store/model"

	gsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// R2 B14-001 存量迁移：写入口归一上线前已落库的大小写影子行须合并——
// 组内有规范（小写）行时删全部变体（变体值从未被读路径命中）；只有
// 变体行时保留最近一行改名为规范键。
func TestR2K1MigrateConfigKeyCaseShadows(t *testing.T) {
	db, err := gorm.Open(gsqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.ViteConfig{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	seed := []model.ViteConfig{
		{Name: "notify_webhook_url", Value: "hook-good", Time: 100},
		{Name: "NOTIFY_WEBHOOK_URL", Value: "hook-shadow", Time: 200},
		{Name: "App_Name", Value: "My Panel", Time: 150},
		{Name: "APP_NAME", Value: "Other", Time: 120},
		{Name: "untouched_key", Value: "v", Time: 1},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].Name, err)
		}
	}

	if err := migrateConfigKeyCaseShadows(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rows []model.ViteConfig
	if err := db.Order("name ASC").Find(&rows).Error; err != nil {
		t.Fatalf("find: %v", err)
	}
	got := make(map[string]string, len(rows))
	for _, row := range rows {
		got[row.Name] = row.Value
	}
	want := map[string]string{
		"notify_webhook_url": "hook-good", // 规范行胜出，影子删除
		"app_name":           "My Panel",  // 仅变体：最近一行改名保留
		"untouched_key":      "v",
	}
	if len(got) != len(want) {
		t.Fatalf("rows after merge = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("row %q = %q, want %q (all rows: %v)", k, got[k], v, got)
		}
	}

	// 幂等：再跑一次无变化。
	if err := migrateConfigKeyCaseShadows(db); err != nil {
		t.Fatalf("migrate second run: %v", err)
	}
	var n int64
	if err := db.Model(&model.ViteConfig{}).Count(&n).Error; err != nil || n != 3 {
		t.Fatalf("count after second run = %d err=%v, want 3", n, err)
	}
}

