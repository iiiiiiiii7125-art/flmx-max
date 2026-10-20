package repo

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
)

// R5-B4 版本化迁移 v2（B4/F-003）—— node_inbound 唯一索引改部分索引。
//
// 背景：v1 建的 idx_node_inbound_node_port 是 (node_id, port) 全量唯一索引。
// 导入节点（手动导入 / 订阅源同步）在 node_inbound 里是 kernel_id=0、
// node_id=0 的行——它们是远端端点记录，不是本地监听，多个导入节点同端口
// 不同主机是合法且常见的数据（443/8388），全量唯一索引会把第二个导入
// 直接以 UNIQUE constraint 失败拒掉（F-003，新装必现）。唯一性真正要保的
// 不变量只有"同一节点上内核托管入站（kernel_id > 0）的监听端口唯一"，
// 故索引改为部分唯一索引：UNIQUE(node_id, port) WHERE kernel_id > 0。
// 导入行的去重继续由应用层 externalKey(host, port) 负责，口径不变。
//
// 存量升级坑（FINAL-REVIEW 修正 3）：v1 的幂等是按索引名 EXISTS 跳过，
// 永远不会重建同名索引——已建过全量索引的库仅改 v1 定义不会生效。
// 本迁移显式处理：定义已是 partial → 记录版本跳过；定义是全量 →
// 显式 DROP INDEX + 按新定义 CREATE；kernel 行内有重复 → 不转换、
// 不记录版本（下次启动重试），并输出清理指引。
//
// 执行环境：SQLite 与 PostgreSQL 均为同一套语句（两边都支持
// partial index 与 DROP INDEX 的无表名形式）。
//
// 回滚：rollbackV2NodeInboundPartialUnique —— DROP 部分索引后按 v1 原
// 定义重建全量唯一索引。注意：存量库若已写入同端口导入行，全量索引
// 重建会因重复数据失败，必须先清理 (node_id=0, port) 重复的导入行。
const migrationV2Version = "r5b4_v2_node_inbound_partial_unique"

const nodeInboundIndexName = "idx_node_inbound_node_port"

// nodeInboundIndexDefinition 返回 idx_node_inbound_node_port 当前的定义
// 文本（SQLite: sqlite_master.sql；PG: pg_indexes.indexdef），
// exists=false 表示索引不存在。
func nodeInboundIndexDefinition(db *gorm.DB) (def string, exists bool, err error) {
	if db.Dialector.Name() == "sqlite" {
		var sqlText interface{}
		row := db.Raw("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", nodeInboundIndexName).Row()
		if err := row.Scan(&sqlText); err != nil {
			return "", false, nil // 无行 = 不存在
		}
		if s, ok := sqlText.(string); ok {
			return s, true, nil
		}
		return "", true, nil
	}
	var defText string
	if err := db.Raw("SELECT indexdef FROM pg_indexes WHERE indexname=?", nodeInboundIndexName).Scan(&defText).Error; err != nil {
		return "", false, err
	}
	if defText == "" {
		return "", false, nil
	}
	return defText, true, nil
}

// nodeInboundIndexIsPartial 判断索引定义是否已是 kernel_id 部分索引。
func nodeInboundIndexIsPartial(def string) bool {
	lower := strings.ToLower(def)
	return strings.Contains(lower, "where") && strings.Contains(lower, "kernel_id")
}

// migrateV2NodeInboundPartialUnique 执行 v2 迁移（幂等，带预检）。
func migrateV2NodeInboundPartialUnique(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationV2Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationV2Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	def, exists, err := nodeInboundIndexDefinition(db)
	if err != nil {
		return fmt.Errorf("migration %s: read index definition: %w", migrationV2Version, err)
	}
	if exists && nodeInboundIndexIsPartial(def) {
		// 全新库经 v1 直接建成 partial，或存量库已转换过但版本记录缺失。
		return recordMigrationV2(db)
	}

	// 转换前预检：kernel 托管行内部 (node_id, port) 不能有重复，
	// 否则新索引建不上。有重复则不转换、不记录版本，下次启动重试。
	var dupGroups int64
	if err := db.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1)`).Scan(&dupGroups).Error; err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationV2Version, err)
	}
	if dupGroups > 0 {
		log.Printf("migration %s: PRECHECK FAILED - %d duplicate (node_id, port) groups among kernel-managed inbounds, keeping existing index", migrationV2Version, dupGroups)
		log.Printf("  面板将正常启动，但 node_inbound 唯一索引未转换为部分索引。")
		log.Printf("  清理重复的内核入站后重启面板即可自动完成转换。")
		return nil
	}

	start := time.Now()
	if exists {
		if err := db.Exec(fmt.Sprintf("DROP INDEX %s", nodeInboundIndexName)).Error; err != nil {
			return fmt.Errorf("migration %s: DROP INDEX %s failed: %w", migrationV2Version, nodeInboundIndexName, err)
		}
		log.Printf("migration %s: dropped old full unique index %s", migrationV2Version, nodeInboundIndexName)
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON node_inbound (node_id, port) WHERE kernel_id > 0", nodeInboundIndexName)).Error; err != nil {
		return fmt.Errorf("migration %s: CREATE partial unique index failed: %w", migrationV2Version, err)
	}
	log.Printf("migration %s: created partial unique index %s on node_inbound (node_id, port) WHERE kernel_id > 0 (%v)",
		migrationV2Version, nodeInboundIndexName, time.Since(start))
	return recordMigrationV2(db)
}

func recordMigrationV2(db *gorm.DB) error {
	if err := db.Create(&schemaMigration{
		Version:   migrationV2Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationV2Version)
	return nil
}

// rollbackV2NodeInboundPartialUnique 回滚 v2：删除部分唯一索引，按 v1 原
// 定义重建全量唯一索引。存量库若已写入同端口导入行，重建会因重复失败，
// 需先删除 (node_id = 0) 且同 port 的多余导入行再执行。
func rollbackV2NodeInboundPartialUnique(db *gorm.DB) error {
	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", nodeInboundIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: drop index: %w", migrationV2Version, err)
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON node_inbound (node_id, port)", nodeInboundIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: recreate full unique index failed (同端口导入行需先清理): %w", migrationV2Version, err)
	}
	log.Printf("rollback %s: restored full unique index %s", migrationV2Version, nodeInboundIndexName)
	return nil
}
