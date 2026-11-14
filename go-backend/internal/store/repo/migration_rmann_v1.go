package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// RM-ANN v1：公告功能整删的存量处置（2.1.13）。
//
// 公告功能已随 1e76851b 从代码面整删（后端 handler/路由/model/
// repo、前端横幅/弹窗/hook、备份导出段），但存量库里的 announcement
// 表及其行仍在（U1 rig 实测：表在、1 行空记录 id=1 内容为空
// enabled=0），违「删除干净，不留残留」口径。本迁移在首次启动
// 时整表删除；新装库（无该表）直接记版本跳过。
//
// 旧备份兼容不受影响：备份导入对未知顶层段本就容忍丢弃＋警告
// 点名（handler.go decodeBackupImportRequest），含 announcements
// 段的旧备份仍 code=0 导入、警告披露；删表迁移只动库表，不碰
// 导入路径（见 backup_import_removed_segment_test.go 的
// announcements 段用例）。
const migrationRMAnnV1Version = "rmann_v1_announcement_removed"

// migrateV1AnnouncementTableRemoved 执行 RM-ANN v1 迁移（幂等）。
func migrateV1AnnouncementTableRemoved(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationRMAnnV1Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationRMAnnV1Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("announcement") {
		// 新装库：无存量表可删，直接记版本（与 v6 同口径）。
		log.Printf("migration %s: no announcement table, nothing to drop", migrationRMAnnV1Version)
	} else {
		var rows int64
		if err := db.Table("announcement").Count(&rows).Error; err != nil {
			return fmt.Errorf("migration %s: count announcement rows: %w", migrationRMAnnV1Version, err)
		}
		if err := db.Migrator().DropTable("announcement"); err != nil {
			return fmt.Errorf("migration %s: drop table announcement: %w", migrationRMAnnV1Version, err)
		}
		log.Printf("migration %s: dropped table announcement (%d row(s) removed)", migrationRMAnnV1Version, rows)
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationRMAnnV1Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationRMAnnV1Version)
	return nil
}
