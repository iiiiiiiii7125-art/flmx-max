package repo

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// C-01（流量审计）：存量隧道计费口径从单向（Flow=1）迁为双向（Flow=2）。
//
// Flow=1 从来只是静默缺省（新建表单初值 1、后端创建缺省 1），UI 从
// 未披露「单向只计上传、下载不计入配额」——不是管理员的知情选择；
// 而配额的语义是总量上限，缺省单向使限额对下载用户形同虚设（实测
// 1.49GB 下载，日配额仅涨 2697B）。新建隧道缺省已改为双向（2，
// handler/mutations.go tunnelCreate 与 tunnel/form.ts 同改）；本
// 迁移把存量 Flow=1 行一次性翻为 2。Flow=2 行不动；Flow<=0/>=3
// 的脏行不动（读时兜底仍归一为 1，repository_flow.go:95-96）。
// 有意仅计上传的管理员可在升级后把个别隧道改回 1。
const migrationTunnelFlowBidiV1Version = "tunnel_flow_bidi_v1"

// migrateV1TunnelFlowBidiDefault 执行 C-01 存量迁移（幂等）。
func migrateV1TunnelFlowBidiDefault(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationTunnelFlowBidiV1Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationTunnelFlowBidiV1Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("tunnel") {
		// 新装库：无存量表可迁，直接记版本（与 v6 同口径）。
		log.Printf("migration %s: no tunnel table, nothing to migrate", migrationTunnelFlowBidiV1Version)
	} else {
		res := db.Exec("UPDATE tunnel SET flow = 2 WHERE flow = 1")
		if res.Error != nil {
			return fmt.Errorf("migration %s: update tunnel flow: %w", migrationTunnelFlowBidiV1Version, res.Error)
		}
		log.Printf("migration %s: flipped %d tunnel row(s) from flow=1 to flow=2", migrationTunnelFlowBidiV1Version, res.RowsAffected)
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationTunnelFlowBidiV1Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationTunnelFlowBidiV1Version)
	return nil
}
