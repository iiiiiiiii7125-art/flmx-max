package repo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// relayCredentialToken 是 repo 层生成随机凭据的统一入口
// （crypto/rand；与 repository_subscription 的 randomToken 同强度）。
func relayCredentialToken(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// backfillPeerShareRelayCredentials 为存量 share 补发 D3-04 的 relay
// 凭据（relay_password 为空者）。新列由 AutoMigrate 添加后默认空串，
// 存量行在面板启动时一次性补齐；handler 侧另有懒补发兜底
// （ensurePeerShareRelayCredentials），单行失败不阻塞启动。
func backfillPeerShareRelayCredentials(db *gorm.DB) {
	var shares []struct {
		ID int64
	}
	if err := db.Raw(`SELECT id FROM peer_share WHERE relay_password = '' OR relay_password IS NULL`).Scan(&shares).Error; err != nil {
		log.Printf("fix132: relay credential backfill scan failed: %v", err)
		return
	}
	for _, row := range shares {
		username := "share_" + relayCredentialToken(6)
		password := relayCredentialToken(24)
		if username == "share_" || password == "" {
			log.Printf("fix132: relay credential generation failed for share %d", row.ID)
			continue
		}
		if err := db.Exec(`UPDATE peer_share SET relay_username = ?, relay_password = ? WHERE id = ? AND (relay_password = '' OR relay_password IS NULL)`,
			username, password, row.ID).Error; err != nil {
			log.Printf("fix132: relay credential backfill failed for share %d: %v", row.ID, err)
		}
	}
	if len(shares) > 0 {
		log.Printf("fix132: backfilled relay credentials for %d peer shares", len(shares))
	}
}

// FIX-132 版本化迁移 v3（D3-05）—— peer_share_runtime 唯一索引由单列
// resource_key 改为复合 (share_id, resource_key)。
//
// 背景：消费方的 ResourceKey 由其本地自增 ID 拼成
// （tunnel:<本地隧道ID>:node:<本地节点ID>:type:<t>:hop:<n>，见
// handler.federationRuntimeResourceKey），两个互不相干的消费方对同一
// 提供方建首条多跳隧道时键必然相同。模型上却挂着单列全局 uniqueIndex，
// repository 的查询/幂等语义（GetPeerShareRuntimeByResourceKey(shareID,
// key)）又是按 share 隔离的——第二个消费方 reserve-port 恒撞
// UNIQUE constraint 失败（R5 真机坐实，错误原文直接回吐对端）。
//
// 迁移步骤（SQLite/PG 同语句，两边都支持按名 DROP INDEX 与复合唯一
// 索引创建）：
//  1. 预检：同 (share_id, resource_key) 组内重复 > 0 时不转换、不记录
//     版本（旧单列索引下该数据本不可能出现，出现即库被外部改过），
//     输出指引、下次启动重试——与 v2 预检失败口径一致。
//  2. DROP 旧单列索引 idx_peer_share_runtime_resource_key（存在时）。
//  3. CREATE UNIQUE INDEX idx_peer_share_runtime_share_resource
//     ON peer_share_runtime (share_id, resource_key)（不存在时）。
//
// 注意 AutoMigrate 的执行顺序在本迁移之前：模型 tag 已去掉单列
// uniqueIndex，AutoMigrate 不会重建旧索引，也不会抢建复合索引
// （唯一索引不进 model tag，与 R5-B4 v1 的既定规矩一致）。
//
// 回滚：rollbackV3PeerShareRuntimeCompositeUnique —— 删除复合索引后
// 按旧定义重建单列唯一索引。注意：迁移后若已写入跨 share 同键行，
// 单列索引重建会因重复失败，必须先处理跨 share 的同键 runtime 行。
const migrationV3Version = "fix132_v3_peer_share_runtime_share_resource_unique"

const (
	legacyResourceKeyIndexName = "idx_peer_share_runtime_resource_key"
	compositeResourceIndexName = "idx_peer_share_runtime_share_resource"
	peerShareRuntimeTableName  = "peer_share_runtime"
)

// peerShareRuntimeIndexExists 判断指定索引是否存在
// （SQLite: sqlite_master；PG: pg_indexes）。
func peerShareRuntimeIndexExists(db *gorm.DB, name string) (bool, error) {
	if db.Dialector.Name() == "sqlite" {
		var count int
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&count).Error; err != nil {
			return false, err
		}
		return count > 0, nil
	}
	var count int
	if err := db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname=?", name).Scan(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// migrateV3PeerShareRuntimeCompositeUnique 执行 v3 迁移（幂等，带预检）。
func migrateV3PeerShareRuntimeCompositeUnique(db *gorm.DB) error {
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

	if !db.Migrator().HasTable(peerShareRuntimeTableName) {
		// 表尚不存在（AutoMigrate 异常路径）：无存量可迁，复合索引将由
		// 后续建表后的下一次启动补建——但正常启动顺序里表已由
		// AutoMigrate 建好，此分支仅作防御。
		return recordMigrationV3(db)
	}

	legacyExists, err := peerShareRuntimeIndexExists(db, legacyResourceKeyIndexName)
	if err != nil {
		return fmt.Errorf("migration %s: read legacy index: %w", migrationV3Version, err)
	}
	compositeExists, err := peerShareRuntimeIndexExists(db, compositeResourceIndexName)
	if err != nil {
		return fmt.Errorf("migration %s: read composite index: %w", migrationV3Version, err)
	}
	if !legacyExists && compositeExists {
		// 全新库经本迁移直接建成复合索引，或存量库已转换但版本记录缺失。
		return recordMigrationV3(db)
	}

	// 转换前预检：同 share 内 (share_id, resource_key) 重复会让复合
	// 索引建不上。有重复则不转换、不记录版本，下次启动重试。
	var dupGroups int64
	if err := db.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM peer_share_runtime GROUP BY share_id, resource_key HAVING COUNT(*) > 1)`).Scan(&dupGroups).Error; err != nil {
		return fmt.Errorf("migration %s precheck failed: %w", migrationV3Version, err)
	}
	if dupGroups > 0 {
		log.Printf("migration %s: PRECHECK FAILED - %d duplicate (share_id, resource_key) groups in peer_share_runtime, keeping existing indexes", migrationV3Version, dupGroups)
		log.Printf("  面板将正常启动，但 peer_share_runtime 唯一索引未转换为 (share_id, resource_key) 复合。")
		log.Printf("  清理同一 share 内的重复 resource_key 行后重启面板即可自动完成转换。")
		return nil
	}

	start := time.Now()
	if legacyExists {
		if err := db.Exec(fmt.Sprintf("DROP INDEX %s", legacyResourceKeyIndexName)).Error; err != nil {
			return fmt.Errorf("migration %s: DROP INDEX %s failed: %w", migrationV3Version, legacyResourceKeyIndexName, err)
		}
		log.Printf("migration %s: dropped legacy single-column unique index %s", migrationV3Version, legacyResourceKeyIndexName)
	}
	if !compositeExists {
		if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON peer_share_runtime (share_id, resource_key)", compositeResourceIndexName)).Error; err != nil {
			return fmt.Errorf("migration %s: CREATE composite unique index failed: %w", migrationV3Version, err)
		}
	}
	log.Printf("migration %s: composite unique index %s on peer_share_runtime (share_id, resource_key) ready (%v)",
		migrationV3Version, compositeResourceIndexName, time.Since(start))
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

// rollbackV3PeerShareRuntimeCompositeUnique 回滚 v3：删除复合唯一索引，
// 按旧定义重建单列唯一索引。迁移后若已写入跨 share 同键行，重建会因
// 重复数据失败，需先删除多余的跨 share 同键 runtime 行再执行。
func rollbackV3PeerShareRuntimeCompositeUnique(db *gorm.DB) error {
	if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %s", compositeResourceIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: drop composite index: %w", migrationV3Version, err)
	}
	if err := db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON peer_share_runtime (resource_key)", legacyResourceKeyIndexName)).Error; err != nil {
		return fmt.Errorf("rollback %s: recreate legacy unique index failed (跨 share 同键行需先清理): %w", migrationV3Version, err)
	}
	if db.Migrator().HasTable(&schemaMigration{}) {
		_ = db.Where("version = ?", migrationV3Version).Delete(&schemaMigration{}).Error
	}
	log.Printf("rollback %s: restored legacy unique index %s", migrationV3Version, legacyResourceKeyIndexName)
	return nil
}
