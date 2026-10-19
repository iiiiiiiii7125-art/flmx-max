package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// R5-B4 阶段 B: 版本化迁移 v1 —— 端口/用户名唯一索引。
//
// 本次执行于测试库（2026-10-19）。生产库必须重新跑阶段 A dry-run 并重新批准，
// 不得直接套用本次结论。
//
// 4 个唯一索引（全部含 node_id，未混入 in_ip/protocol）：
//   - forward_port(node_id, port)
//   - chain_tunnel(node_id, port)
//   - node_inbound(node_id, port)
//   - user(user)
//
// 幂等：已存在的索引跳过。失败时输出明确错误，不静默。
const migrationV1Version = "r5b4_v1_port_user_unique"

type schemaMigration struct {
	Version   string `gorm:"primaryKey;type:varchar(64)"`
	AppliedAt int64  `gorm:"not null"`
}

func (schemaMigration) TableName() string { return "schema_migration" }

// migrateV1PortUserUnique 执行 v1 迁移（幂等）。
func migrateV1PortUserUnique(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationV1Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationV1Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	indexes := []struct {
		table string
		name  string
		cols  string
	}{
		{"forward_port", "idx_forward_port_node_port", "(node_id, port)"},
		{"chain_tunnel", "idx_chain_tunnel_node_port", "(node_id, port)"},
		{"node_inbound", "idx_node_inbound_node_port", "(node_id, port)"},
		{"user", "idx_user_username_unique", "(user)"},
	}

	for _, idx := range indexes {
		// 幂等：已存在则跳过。
		var count int64
		if db.Dialector.Name() == "sqlite" {
			db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", idx.name).Scan(&count)
		} else {
			db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname=?", idx.name).Scan(&count)
		}
		if count > 0 {
			log.Printf("migration %s: index %s exists, skipping", migrationV1Version, idx.name)
			continue
		}

		start := time.Now()
		// 注意：chain_tunnel.port 可为空，NULL 不参与唯一约束（SQL 标准）。
		sql := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s %s", idx.name, idx.table, idx.cols)
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf(
				"migration %s: CREATE UNIQUE INDEX %s ON %s %s failed: %w\n"+
					"可能原因：表中有重复数据。请先运行 scripts/phase_a_dryrun.sql 定位重复，"+
					"清理后重新启动。",
				migrationV1Version, idx.name, idx.table, idx.cols, err)
		}
		log.Printf("migration %s: created index %s on %s %s (%v)",
			migrationV1Version, idx.name, idx.table, idx.cols, time.Since(start))
	}

	// 记录版本。
	if err := db.Create(&schemaMigration{
		Version:   migrationV1Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationV1Version)
	return nil
}

// rollbackV1PortUserUnique 回滚 v1 迁移（删除索引，不删版本记录以便追溯）。
func rollbackV1PortUserUnique(db *gorm.DB) error {
	indexes := []string{
		"idx_forward_port_node_port",
		"idx_chain_tunnel_node_port",
		"idx_node_inbound_node_port",
		"idx_user_username_unique",
	}
	for _, name := range indexes {
		sql := fmt.Sprintf("DROP INDEX IF EXISTS %s", name)
		if err := db.Exec(sql).Error; err != nil {
			return fmt.Errorf("drop index %s: %w", name, err)
		}
		log.Printf("rollback: dropped index %s", name)
	}
	return nil
}
