package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// C12 版本化迁移 v4（X04-001/X04-002）—— 转发查询面补普通索引。
//
// 背景：forward_port 此前只有 v1 的唯一索引 (node_id, port)，按
// forward_id 的查询（转发列表入口解析、备份导出端口行）谓词落在
// 索引左端之外，只能全表扫描；逐条取数与之叠加成二次型耗时。
// 列表/导出已改批量 IN 查询，本迁移再给批量查询补上可用的索引：
//   - forward_port(forward_id)：批量端口预取、单条入口解析、导出。
//   - forward(user_id)：GetUserPackageForwards 按用户过滤。
//   - forward(status, inx)：ListForwards 的 status 过滤 + inx 排序。
//
// 全部为普通（非唯一）索引：无重复数据风险、无需预检，
// CREATE INDEX IF NOT EXISTS 在 SQLite 与 PostgreSQL 下同形可用。
// 不走 model tag 的 AutoMigrate 建索引——存量库靠版本化迁移显式
// 落地（与 R5-B4 v1 的既定规矩一致，X04 优化项的明确要求）。
//
// 回滚：rollbackC12V4ForwardLookupIndexes —— DROP 三个索引。
const migrationC12V4Version = "c12_v4_forward_lookup_indexes"

var c12V4Indexes = []struct {
	table string
	name  string
	cols  string
}{
	{"forward_port", "idx_forward_port_forward_id", "(forward_id)"},
	// PG-1 同 v1：forward 在 PG 是非保留词，但与 "user" 的处理保持
	// 一致的双引号写法，两方言同形且无歧义。
	{`"forward"`, "idx_forward_user_id", "(user_id)"},
	{`"forward"`, "idx_forward_status_inx", "(status, inx)"},
}

// c12IndexExists 判断指定索引是否存在（SQLite: sqlite_master；
// PG: pg_indexes）。
func c12IndexExists(db *gorm.DB, name string) (bool, error) {
	var count int
	source := "pg_indexes WHERE indexname=?"
	if db.Dialector.Name() == "sqlite" {
		source = "sqlite_master WHERE type='index' AND name=?"
	}
	if err := db.Raw("SELECT COUNT(*) FROM "+source, name).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// migrateC12V4ForwardLookupIndexes 执行 v4 迁移（幂等）。
func migrateC12V4ForwardLookupIndexes(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationC12V4Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationC12V4Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("forward_port") || !db.Migrator().HasTable("forward") {
		// 表尚不存在（AutoMigrate 异常路径）：无存量可补，下一次
		// 正常启动（表已建好）再执行——不记录版本。
		log.Printf("migration %s: forward tables missing, deferring to next startup", migrationC12V4Version)
		return nil
	}

	for _, idx := range c12V4Indexes {
		exists, err := c12IndexExists(db, idx.name)
		if err != nil {
			return fmt.Errorf("migration %s: read index %s: %w", migrationC12V4Version, idx.name, err)
		}
		if exists {
			log.Printf("migration %s: index %s exists, skipping", migrationC12V4Version, idx.name)
			continue
		}
		start := time.Now()
		if err := db.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s %s", idx.name, idx.table, idx.cols)).Error; err != nil {
			return fmt.Errorf("migration %s: CREATE INDEX %s ON %s %s failed: %w",
				migrationC12V4Version, idx.name, idx.table, idx.cols, err)
		}
		log.Printf("migration %s: created index %s on %s %s (%v)",
			migrationC12V4Version, idx.name, idx.table, idx.cols, time.Since(start))
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationC12V4Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationC12V4Version)
	return nil
}

// rollbackC12V4ForwardLookupIndexes 回滚 v4 迁移（删除三个索引，
// 并删版本记录以便追溯后重跑）。
func rollbackC12V4ForwardLookupIndexes(db *gorm.DB) error {
	for _, idx := range c12V4Indexes {
		if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", idx.name)).Error; err != nil {
			return fmt.Errorf("rollback %s: drop index %s: %w", migrationC12V4Version, idx.name, err)
		}
		log.Printf("rollback %s: dropped index %s", migrationC12V4Version, idx.name)
	}
	if db.Migrator().HasTable(&schemaMigration{}) {
		_ = db.Where("version = ?", migrationC12V4Version).Delete(&schemaMigration{}).Error
	}
	return nil
}
