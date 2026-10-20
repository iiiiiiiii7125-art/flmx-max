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

// precheckResult 预检结果。
type precheckResult struct {
	ForwardPortDups int64
	ChainTunnelDups int64
	NodeInboundDups int64
	UsernameDups    int64
}

// runV1Precheck 运行只读预检（与 scripts/phase_a_dryrun.sql 逻辑一致）。
func runV1Precheck(db *gorm.DB) (*precheckResult, error) {
	r := &precheckResult{}
	queries := []struct {
		dest *int64
		sql  string
	}{
		{&r.ForwardPortDups, `SELECT COUNT(*) FROM (SELECT 1 FROM forward_port GROUP BY node_id, port HAVING COUNT(*) > 1)`},
		{&r.ChainTunnelDups, `SELECT COUNT(*) FROM (SELECT 1 FROM chain_tunnel WHERE port IS NOT NULL AND port > 0 GROUP BY node_id, port HAVING COUNT(*) > 1)`},
		// B4/F-003: node_inbound 的唯一索引是部分索引（WHERE kernel_id > 0，
		// 见 migrateV2NodeInboundPartialUnique），预检口径必须一致——导入行
		// （kernel_id=0）同端口重复是合法数据，不能让它挡住全部四个索引。
		{&r.NodeInboundDups, `SELECT COUNT(*) FROM (SELECT 1 FROM node_inbound WHERE kernel_id > 0 GROUP BY node_id, port HAVING COUNT(*) > 1)`},
		{&r.UsernameDups, `SELECT COUNT(*) FROM (SELECT 1 FROM "user" GROUP BY "user" HAVING COUNT(*) > 1)`},
	}
	for _, q := range queries {
		if err := db.Raw(q.sql).Scan(q.dest).Error; err != nil {
			return nil, fmt.Errorf("precheck query failed: %w", err)
		}
	}
	return r, nil
}

// hasDups 是否有重复。
func (r *precheckResult) hasDups() bool {
	return r.ForwardPortDups > 0 || r.ChainTunnelDups > 0 || r.NodeInboundDups > 0 || r.UsernameDups > 0
}

// migrateV1PortUserUnique 执行 v1 迁移（幂等，带预检）。
//
// D23: 预检发现重复时不建索引，但面板正常启动；日志输出明确指引。
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

	// D23: 预检。
	precheck, err := runV1Precheck(db)
	if err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationV1Version, err)
	}
	if precheck.hasDups() {
		// 发现重复：不建索引，面板正常启动，输出明确指引。
		log.Printf("migration %s: PRECHECK FAILED - duplicate data found, skipping index creation",
			migrationV1Version)
		log.Printf("  forward_port dups: %d, chain_tunnel dups: %d, node_inbound dups: %d, username dups: %d",
			precheck.ForwardPortDups, precheck.ChainTunnelDups, precheck.NodeInboundDups, precheck.UsernameDups)
		log.Printf("  面板将正常启动，但唯一索引未创建。")
		log.Printf("  清理步骤：运行 scripts/phase_a_dryrun.sql 定位重复，按手册清理后重启面板。")
		log.Printf("  详见 doc/guide/upgrade.md「从旧版本升级」。")
		// 记录预检失败（不标记为已应用，下次启动会重试）。
		return nil
	}

	indexes := []struct {
		table string
		name  string
		cols  string
		where string
	}{
		{"forward_port", "idx_forward_port_node_port", "(node_id, port)", ""},
		{"chain_tunnel", "idx_chain_tunnel_node_port", "(node_id, port)", ""},
		// B4/F-003: node_inbound 直接建部分唯一索引——唯一性只对内核托管
		// 入站（kernel_id > 0，真实本地监听）有意义；导入行 kernel_id=0，
		// 同端口不同主机合法共存。存量库里已建的全量同名索引由 v2 迁移
		// 显式 DROP + CREATE 转换（本函数的"同名跳过"不会重建它）。
		{"node_inbound", "idx_node_inbound_node_port", "(node_id, port)", "WHERE kernel_id > 0"},
		{"user", "idx_user_username_unique", "(user)", ""},
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
		sql := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s %s %s", idx.name, idx.table, idx.cols, idx.where)
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
