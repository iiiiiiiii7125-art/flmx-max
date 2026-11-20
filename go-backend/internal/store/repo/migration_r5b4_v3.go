package repo

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
)

// R5-B4 版本化迁移 v3（R18-C1-01）—— chain_tunnel 唯一索引改部分索引。
//
// 背景：v1 初版（f1d94f48）建的 idx_chain_tunnel_node_port 是
// (node_id, port) 全量唯一索引；A3-S1（2455ccd7）才改为
// UNIQUE(node_id, port) WHERE port > 0——port NULL/0 是既有「未定
// 端口」语义（R7-H-01），多行同节点 port=0 合法。但 v1 的幂等是
// 按索引名 EXISTS 跳过，永不核对定义，窗口期跑过 v1 的升级库
// 永远留着全量索引：含两条同节点 port=0 链路行的备份在该库上恢
// 复时撞唯一约束、整单回滚（Import 为单事务），而同一份备份在
// 新装库（部分索引）上导入成功。v1 注释称“已建有全量同名索引的
// 库……无需转换”与 R7-H-01 直接冲突，现由本迁移纠正。
//
// 本迁移仿照 v2（node_inbound）：定义已是 partial → 记录版本跳过；
// 定义是全量 → 预检 port>0 无重复后显式 DROP + CREATE 转换；
// port>0 有重复 → 不转换、不记录版本（下次启动重试），并输出清理
// 指引。v1 注释已同步改指 v3。
//
// 执行环境：SQLite 与 PostgreSQL 均为同一套语句（两边都支持
// partial index 与 DROP INDEX 的无表名形式）。
//
// 回滚：rollbackV3ChainTunnelPartialUnique —— DROP 部分索引后按 v1 原
// 定义重建全量唯一索引。注意：存量库若已写入同节点 port=0 的多行，
// 全量索引重建会因重复数据失败，必须先清理多余的 port=0 行。
const migrationV3Version = "r5b4_v3_chain_tunnel_partial_unique"

const chainTunnelIndexName = "idx_chain_tunnel_node_port"

// chainTunnelIndexDefinition 返回 idx_chain_tunnel_node_port 当前的定义
// 文本（SQLite: sqlite_master.sql；PG: pg_indexes.indexdef），
// exists=false 表示索引不存在。
func chainTunnelIndexDefinition(db *gorm.DB) (def string, exists bool, err error) {
	if db.Dialector.Name() == "sqlite" {
		var sqlText interface{}
		row := db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", chainTunnelIndexName).Row()
		if err := row.Scan(&sqlText); err != nil {
			return "", false, nil // 无行 = 不存在
		}
		if s, ok := sqlText.(string); ok {
			return s, true, nil
		}
		return "", true, nil
	}
	var defText string
	if err := db.Raw("SELECT indexdef FROM pg_indexes WHERE indexname=?", chainTunnelIndexName).Scan(&defText).Error; err != nil {
		return "", false, err
	}
	if defText == "" {
		return "", false, nil
	}
	return defText, true, nil
}

// chainTunnelIndexIsPartial 判断索引定义是否已是 port>0 部分索引。
func chainTunnelIndexIsPartial(def string) bool {
	collapsed := strings.Join(strings.Fields(strings.ToLower(def)), " ")
	return strings.Contains(collapsed, "where port > 0")
}

// migrateV3ChainTunnelPartialUnique 执行 v3 迁移（幂等，带预检）。
func migrateV3ChainTunnelPartialUnique(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationV3Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationV3Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	def, exists, err := chainTunnelIndexDefinition(db)
	if err != nil {
		return fmt.Errorf("migration %s: read index definition: %w", migrationV3Version, err)
	}
	if exists && chainTunnelIndexIsPartial(def) {
		// 全新库经 v1 直接建成 partial，或存量库已转换过但版本记录缺失。
		return recordMigrationV3(db)
	}

	// 转换前预检：port>0 行内部 (node_id, port) 不能有重复，
	// 否则新索引建不上。有重复则不转换、不记录版本，下次启动重试。
	var dupGroups int64
	if err := db.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM chain_tunnel WHERE port > 0 GROUP BY node_id, port HAVING COUNT(*) > 1)`).Scan(&dupGroups).Error; err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationV3Version, err)
	}
	if dupGroups > 0 {
		log.Printf("migration %s: PRECHECK FAILED - %d duplicate (node_id, port) groups among port>0 chain tunnels, keeping existing index", migrationV3Version, dupGroups)
		log.Printf("  面板将正常启动，但 chain_tunnel 唯一索引未转换为部分索引。")
		log.Printf("  清理重复的链路端口后重启面板即可自动完成转换。")
		return nil
	}

	start := time.Now()
	if exists {
		if err := db.Exec(fmt.Sprintf("DROP INDEX %s", chainTunnelIndexName)).Error; err != nil {
			return fmt.Errorf("migration %s: DROP INDEX %s failed: %w", migrationV3Version, chainTunnelIndexName, err)
		}
		log.Printf("migration %s: dropped old full unique index %s", migrationV3Version, chainTunnelIndexName)
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON chain_tunnel (node_id, port) WHERE port > 0", chainTunnelIndexName)).Error; err != nil {
		return fmt.Errorf("migration %s: CREATE partial unique index failed: %w", migrationV3Version, err)
	}
	log.Printf("migration %s: created partial unique index %s on chain_tunnel (node_id, port) WHERE port > 0 (%v)",
		migrationV3Version, chainTunnelIndexName, time.Since(start))
	return recordMigrationV3(db)
}

func recordMigrationV3(db *gorm.DB) error {
	if err := db.Create(&schemaMigration{
		Version:   migrationV3Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationV3Version)
	return nil
}

// rollbackV3ChainTunnelPartialUnique 回滚 v3：删除部分唯一索引，按 v1 原
// 定义重建全量唯一索引。存量库若已写入同节点 port=0 的多行，重建会因
// 重复失败，需先删除多余的 port=0 行再执行。
func rollbackV3ChainTunnelPartialUnique(db *gorm.DB) error {
	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", chainTunnelIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: drop index: %w", migrationV3Version, err)
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON chain_tunnel (node_id, port)", chainTunnelIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: recreate full unique index failed (同节点 port=0 的多余行需先清理): %w", migrationV3Version, err)
	}
	log.Printf("rollback %s: restored full unique index %s", migrationV3Version, chainTunnelIndexName)
	return nil
}
