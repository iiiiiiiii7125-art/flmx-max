package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// K10 版本化迁移 v5（B02-003）—— 用户名大小写折叠唯一索引。
//
// 背景：B02-003 裁决用户名判重不区分大小写。应用层判重已统一到
// usernameFoldCount（LOWER 两侧折叠），本迁移再给 DB 层补同口径
// 兜底：函数唯一索引 (LOWER("user"))，拦截任何绕过应用层判重的
// 并发写入（判重面全集见 username_check.go 头注）。v1 的精确唯一
// 索引 idx_user_username_unique 与之并存（精确唯一被折叠唯一蕴含，
// 保留不拆，避免存量库索引形态分叉）。
//
// 存量脏数据（D23 范式，与 v1 一致）：
//   - 预检发现折叠重复组时**不建索引、不记版本**（下次启动重试），
//     面板正常启动；日志逐组点名（折叠键 + 成员 id + 存储形态），
//     不静默合并、不改名、不删行——存量双生子如何处置由用户决定。
//   - 降级期间应用层判重照常执法：新写入的大小写变体仍被拒，
//     只是少了 DB 层对并发竞态的最终兜底（SQLite 串行连接下应用
//     层复检本身成立；PG 靠折叠键咨询锁串行化，见 CreateUserTx）。
//   - 清理口径：scripts/phase_a_dryrun.sql 第 6b 节可定位全部
//     折叠重复组；清理后重启面板即自动补建索引。
//
// 索引表达式与查询表达式必须同形（LOWER("user")），两方言均支持
// 表达式索引（SQLite ≥3.9 / PostgreSQL），写法同形。
const migrationK10V5Version = "k10_v5_username_casefold_unique"

const usernameFoldIndexName = "idx_user_username_fold_unique"

// migrateV5UsernameCasefoldUnique 执行 v5 迁移（幂等，带预检）。
func migrateV5UsernameCasefoldUnique(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationK10V5Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationK10V5Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("user") {
		// 表尚不存在（AutoMigrate 异常路径）：无存量可查，下一次
		// 正常启动再执行——不记录版本（与 v4 同口径）。
		log.Printf("migration %s: user table missing, deferring to next startup", migrationK10V5Version)
		return nil
	}

	// D23 预检：折叠重复存在则点名跳过。
	groups, err := listUsernameFoldDupGroups(db)
	if err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationK10V5Version, err)
	}
	if len(groups) > 0 {
		log.Printf("migration %s: PRECHECK FAILED - %d case-variant duplicate username group(s) found, skipping index creation",
			migrationK10V5Version, len(groups))
		for _, g := range groups {
			log.Printf("  case-variant duplicates: folded=%q ids=[%s] stored forms=[%s]", g.Fold, g.IDs, g.Forms)
		}
		log.Printf("  面板将正常启动，但用户名大小写折叠唯一索引未创建；应用层判重仍按大小写不敏感执法。")
		log.Printf("  上列用户仅大小写不同、按现行口径视为同名；系统不会自动合并/改名，请人工决定保留哪一个后清理。")
		log.Printf("  定位：scripts/phase_a_dryrun.sql 第 6b 节；清理后重启面板将自动补建索引。")
		log.Printf("  详见 doc/guide/upgrade.md「从旧版本升级」。")
		return nil
	}

	exists, err := c12IndexExists(db, usernameFoldIndexName)
	if err != nil {
		return fmt.Errorf("migration %s: read index %s: %w", migrationK10V5Version, usernameFoldIndexName, err)
	}
	if exists {
		log.Printf("migration %s: index %s exists, skipping", migrationK10V5Version, usernameFoldIndexName)
	} else {
		start := time.Now()
		if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON \"user\" (LOWER(\"user\"))", usernameFoldIndexName)).Error; err != nil {
			return fmt.Errorf(
				"migration %s: CREATE UNIQUE INDEX %s failed: %w\n"+
					"若原始错误为唯一约束冲突：预检后仍出现折叠重复（并发写入窗口），请运行 scripts/phase_a_dryrun.sql 第 6b 节定位、清理后重启；"+
					"若为语法/方言类错误，请连同原始错误上报，勿按重复数据排查。",
				migrationK10V5Version, usernameFoldIndexName, err)
		}
		log.Printf("migration %s: created index %s on \"user\" (LOWER(\"user\")) (%v)",
			migrationK10V5Version, usernameFoldIndexName, time.Since(start))
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationK10V5Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationK10V5Version)
	return nil
}

// rollbackV5UsernameCasefoldUnique 回滚 v5 迁移（删除折叠唯一索引，
// 不删版本记录以便追溯，与 v1 回滚同口径）。
func rollbackV5UsernameCasefoldUnique(db *gorm.DB) error {
	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", usernameFoldIndexName)).Error; err != nil {
		return fmt.Errorf("drop index %s: %w", usernameFoldIndexName, err)
	}
	log.Printf("rollback: dropped index %s", usernameFoldIndexName)
	return nil
}
