package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// DEV-RM-SNELL 迁移 v1 —— 入站 Snell 功能整删的存量处置（2.1.0）。
//
// 裁定口径（主控建议、合并前待终裁）：存量 Snell 入站不删行、不改
// 凭据，只强制置禁用；Snell 内核行保留，但清掉已装状态并置禁用，
// 使 agent 不再拉起 snell-server / sing-box Snell 监听。理由：非破
// 坏性、流量影响仅限被删功能本身、留审计痕；用户可在面板里看到
// 这些入站已停用并自行删除。备选「整表删除」未采用（如改判，另
// 起迁移 v2 执行）。
//
// 具体动作：
//   - node_inbound：内核托管行（kernel_id > 0）中 protocol='snell' 或
//     所属内核 kernel_type='snell' 且 enabled=1 的行 → enabled=0，
//     逐行点名日志（id + name）。导入行（kernel_id=0）即便 protocol
//     为 snell 也不动——导入 Snell 节点不在本次删除范围（保留面）。
//   - node_kernel：kernel_type='snell' 的行 → enabled=0、
//     installed_version=''、config_hash=''、applied_hash=''、
//     status='pending'、last_error 写明移除原因。
//
// 禁用后：订阅装配天然略过禁用入站；创建/编辑面因 catalog 已无
// Snell 而拒绝；下发面 ComboSupported 复检拒绝（内核行本身已禁用
// 且类型不在 catalog 内）。
const migrationRMSnellV1Version = "rmsnell_v1_snell_inbound_removed"

// migrateV1SnellInboundRemoved 执行 DEV-RM-SNELL v1 迁移（幂等）。
func migrateV1SnellInboundRemoved(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationRMSnellV1Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationRMSnellV1Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("node_inbound") || !db.Migrator().HasTable("node_kernel") {
		// 表尚不存在（AutoMigrate 异常路径）：无存量可处置，下一次
		// 正常启动再执行——不记录版本（与 v5 同口径）。
		log.Printf("migration %s: node_inbound/node_kernel table missing, deferring to next startup", migrationRMSnellV1Version)
		return nil
	}

	now := time.Now().UnixMilli()

	// 先取受影响入站清单用于点名日志（禁用前快照）。
	type snellRow struct {
		ID   int64
		Name string
	}
	var rows []snellRow
	if err := db.Raw(`SELECT id, name FROM node_inbound
		WHERE enabled = 1 AND kernel_id > 0 AND (protocol = 'snell' OR kernel_id IN
			(SELECT id FROM node_kernel WHERE kernel_type = 'snell'))`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationRMSnellV1Version, err)
	}
	for _, r := range rows {
		log.Printf("migration %s: disabling Snell inbound id=%d name=%q (Snell 入站功能已删除)", migrationRMSnellV1Version, r.ID, r.Name)
	}
	if len(rows) > 0 {
		if err := db.Exec(`UPDATE node_inbound SET enabled = 0, updated_time = ?
			WHERE enabled = 1 AND kernel_id > 0 AND (protocol = 'snell' OR kernel_id IN
				(SELECT id FROM node_kernel WHERE kernel_type = 'snell'))`, now).Error; err != nil {
			return fmt.Errorf("migration %s: disable inbounds: %w", migrationRMSnellV1Version, err)
		}
	}

	res := db.Exec(`UPDATE node_kernel SET enabled = 0, installed_version = '',
		config_hash = '', applied_hash = '', status = 'pending',
		last_error = 'Snell 内核已随 2.1.0 移除（入站 Snell 功能删除）', updated_time = ?
		WHERE kernel_type = 'snell'`, now)
	if res.Error != nil {
		return fmt.Errorf("migration %s: clear snell kernels: %w", migrationRMSnellV1Version, res.Error)
	}
	if res.RowsAffected > 0 {
		log.Printf("migration %s: cleared installed state on %d Snell kernel row(s)", migrationRMSnellV1Version, res.RowsAffected)
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationRMSnellV1Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied (disabled %d inbound(s))", migrationRMSnellV1Version, len(rows))
	return nil
}
