package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// RMShareOrphanTunnelsKey 是 v6 联邦移除迁移写的一次性 vite_config
// 待办键（R18-B7-01）：值是 JSON 数组，列出因远端节点被删而丢了
// 入口/出口节点的隧道 ID。tunnelList 据此给这些隧道打
// orphanedByFederationRemoval 标记；管理员重部署或删除隧道后逐条
// 清除（键空时整键删除）。
const RMShareOrphanTunnelsKey = "rmshare_orphan_tunnels"

// ListOrphanedType2TunnelIDs derives the orphaned Type 2 tunnels
// at list time (R18-R3-B7-01): a Type 2 tunnel with no live entry
// segment — zero chain_tunnel rows, no chain_type='1' row at all,
// or an entry row pointing at a node that no longer exists. This
// covers DBs that already ran the v6 migration before this fix
// existed: the migration recorded its version and will skip on
// upgrade, so the todo key is never written for them, but the
// orphaned rows are still there. Read-only by design: tunnelList
// derives the flag from this query instead of backfilling the key,
// so listing never writes (no repeat full-table rewrite per call).
// SQLite and PostgreSQL share this one statement (chain_type is
// varchar '1' on both; tunnel.type is a non-reserved column name).
func (r *Repository) ListOrphanedType2TunnelIDs() ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var ids []int64
	err := r.db.Raw(`SELECT t.id FROM tunnel t WHERE t.type = 2 AND NOT EXISTS (
		SELECT 1 FROM chain_tunnel c WHERE c.tunnel_id = t.id AND c.chain_type = '1'
		AND EXISTS (SELECT 1 FROM node n WHERE n.id = c.node_id)
	) ORDER BY t.id ASC`).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// DEV-RM-SHARE v6：跨面板共享/联邦功能整体移除的一次性数据清理。
//
// 2.1.0 删除共享板块后，存量库里的功能自有数据按「删除不留残留」
// 口径在首次启动时清净：
//  1. is_remote=1 的远端节点行（他面板共享进来的节点）连同其节点级
//     级联行一并删除（复用 DeleteNodeCascade 的级联全集）；引用它
//     们的隧道行本身保留，须管理员在隧道编辑面处理（发版说明已
//     点名）。
//  2. 五张联邦自有表整表删除：peer_share、peer_share_runtime、
//     peer_share_resource、federation_tunnel_binding、
//     federation_pending_release。
//  3. node 表的远端四列删除：is_remote、remote_url、remote_token、
//     remote_config。
//
// 全部步骤以 HasTable/HasColumn 守卫、幂等；新装库（无存量表/列）
// 直接记版本跳过。清理失败返回 error 阻断启动，不留半清理状态
// 后继续记版本——下次启动从头重跑（步骤本身幂等）。
const migrationRMShareV6Version = "rmshare_v6_federation_removal"

var federationRemovalTables = []string{
	"peer_share",
	"peer_share_runtime",
	"peer_share_resource",
	"federation_tunnel_binding",
	"federation_pending_release",
}

var nodeRemoteColumns = []string{"is_remote", "remote_url", "remote_token", "remote_config"}

func migrateV6FederationRemoval(db *gorm.DB) error {
	if db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := db.Where("version = ?", migrationRMShareV6Version).First(&m).Error; err == nil {
			log.Printf("migration %s already applied, skipping", migrationRMShareV6Version)
			return nil
		}
	} else {
		if err := db.Migrator().CreateTable(&schemaMigration{}); err != nil {
			return fmt.Errorf("create schema_migration: %w", err)
		}
	}

	if !db.Migrator().HasTable("node") {
		// 表尚不存在（AutoMigrate 异常路径）：无存量可清，下一次
		// 正常启动再执行——不记录版本（与 v5 同口径）。
		log.Printf("migration %s: node table missing, deferring to next startup", migrationRMShareV6Version)
		return nil
	}

	// 1. 远端节点行 + 级联。
	if db.Migrator().HasColumn(&model.Node{}, "is_remote") {
		var remoteNodeIDs []int64
		if err := db.Table("node").Where("is_remote = ?", 1).Pluck("id", &remoteNodeIDs).Error; err != nil {
			return fmt.Errorf("migration %s: list remote nodes: %w", migrationRMShareV6Version, err)
		}
		// R18-B7-01：先算出引用远端节点的隧道——DeleteNodeCascade
		// 会静默删掉 chain_tunnel 行，删完就再也算不出来了；隧道
		// 行本身保留，但入口随节点一起没了，不断链也无信号。
		var orphanTunnelIDs []int64
		if len(remoteNodeIDs) > 0 && db.Migrator().HasTable("chain_tunnel") {
			if err := db.Table("chain_tunnel").Where("node_id IN ?", remoteNodeIDs).Distinct("tunnel_id").Pluck("tunnel_id", &orphanTunnelIDs).Error; err != nil {
				return fmt.Errorf("migration %s: list tunnels referencing remote nodes: %w", migrationRMShareV6Version, err)
			}
		}
		r := &Repository{db: db}
		for _, nodeID := range remoteNodeIDs {
			if err := r.DeleteNodeCascade(nodeID); err != nil {
				return fmt.Errorf("migration %s: delete remote node %d: %w", migrationRMShareV6Version, nodeID, err)
			}
		}
		if len(remoteNodeIDs) > 0 {
			log.Printf("migration %s: removed %d remote (federation) node row(s)", migrationRMShareV6Version, len(remoteNodeIDs))
		}
		if len(orphanTunnelIDs) > 0 {
			// 断链点名：日志写清隧道 ID，另写 vite_config 一次性
			// 待办键供隧道列表打标记；键写失败只警告不阻断启动
			// （清理本身已完成，待办键是提示信号而非数据状态）。
			log.Printf("migration %s: %d tunnel(s) referenced removed remote node(s) and lost entry/exit nodes, manual fix required (tunnel_ids=%v)", migrationRMShareV6Version, len(orphanTunnelIDs), orphanTunnelIDs)
			if !db.Migrator().HasTable("vite_config") {
				log.Printf("migration %s: WARNING vite_config table missing, orphan-tunnel todo key not recorded (tunnel_ids=%v)", migrationRMShareV6Version, orphanTunnelIDs)
			} else if value, err := json.Marshal(orphanTunnelIDs); err != nil {
				log.Printf("migration %s: WARNING orphan-tunnel todo key not recorded: %v (tunnel_ids=%v)", migrationRMShareV6Version, err, orphanTunnelIDs)
			} else if err := r.UpsertConfig(RMShareOrphanTunnelsKey, string(value), time.Now().UnixMilli()); err != nil {
				log.Printf("migration %s: WARNING orphan-tunnel todo key write failed: %v (tunnel_ids=%v)", migrationRMShareV6Version, err, orphanTunnelIDs)
			}
		}
	}

	// 2. 联邦自有表整表删除。
	for _, table := range federationRemovalTables {
		if db.Migrator().HasTable(table) {
			if err := db.Migrator().DropTable(table); err != nil {
				return fmt.Errorf("migration %s: drop table %s: %w", migrationRMShareV6Version, table, err)
			}
			log.Printf("migration %s: dropped table %s", migrationRMShareV6Version, table)
		}
	}

	// 3. node 远端列删除。SQLite 走原生 ALTER：glebarez 的
	// Migrator().DropColumn 以表重建实现，其实测在 node 表上静默
	// 空转（日志报成功、四列仍在——其 DDL 解析未匹配到这四列），
	// modernc sqlite 支持 DROP COLUMN（>= 3.35），直接用之；
	// PG 的 gorm migrator 本就是真 ALTER，沿用之。
	for _, column := range nodeRemoteColumns {
		if db.Migrator().HasColumn(&model.Node{}, column) {
			var err error
			if db.Dialector.Name() == "sqlite" {
				err = db.Exec("ALTER TABLE node DROP COLUMN " + column).Error
			} else {
				err = db.Migrator().DropColumn(&model.Node{}, column)
			}
			if err != nil {
				return fmt.Errorf("migration %s: drop node column %s: %w", migrationRMShareV6Version, column, err)
			}
			log.Printf("migration %s: dropped node column %s", migrationRMShareV6Version, column)
		}
	}

	if err := db.Create(&schemaMigration{
		Version:   migrationRMShareV6Version,
		AppliedAt: time.Now().Unix(),
	}).Error; err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}
	log.Printf("migration %s applied", migrationRMShareV6Version)
	return nil
}
