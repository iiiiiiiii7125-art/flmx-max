package repo

import "strconv"

// R5-B4C: 查询待执行的迁移（用于健康检查可观测性）。

// DuplicateDetail 重复详情。
type DuplicateDetail struct {
	Table           string `json:"table"`
	DuplicateGroups int64  `json:"duplicateGroups"`
	Sample          string `json:"sample,omitempty"`
}

// PendingMigrationStatus 待迁移状态（结构化）。
type PendingMigrationStatus struct {
	Required bool              `json:"required"`
	Tables   []DuplicateDetail `json:"tables,omitempty"`
	Guide    string            `json:"guide,omitempty"`

	// pendingVersions 是本次判定实际待处理（被预检/存量形态挡下）
	// 的版本名，供 PendingMigrations() 如实回报——C1-01 前该兼容
	// 接口恒回 v1 名，v2/v3/v4/v5 单独待处理时属误标。
	pendingVersions []string
}

// GetPendingMigrationStatus 返回结构化的待迁移状态。
//
// B4/F-009: 此前只看 v1 且对 PG 谎报——PG 分支根本不执行迁移，状态却
// 恒报「无重复，下次启动会自动建」。F-003 已把 v1+v2 接到双后端启动
// 路径，该承诺现在成立；同时补上 v2 感知：v1 已应用但 node_inbound
// 索引仍是全量唯一（v2 因 kernel 入站同口重复被预检挡下）时，必须如
// 实报告待处理，而不是被 v1 的「已应用」短路成无待迁移。
func (r *Repository) GetPendingMigrationStatus() *PendingMigrationStatus {
	status := &PendingMigrationStatus{Required: false}
	if r == nil || r.db == nil {
		return status
	}
	applied := func(version string) bool {
		if !r.db.Migrator().HasTable(&schemaMigration{}) {
			return false
		}
		var m schemaMigration
		return r.db.Where("version = ?", version).First(&m).Error == nil
	}

	if !applied(migrationV1Version) {
		// 未应用：运行预检看是否有重复。
		precheck, err := runV1Precheck(r.db)
		if err != nil {
			return status
		}
		if !precheck.hasDups() {
			return status // 无重复，下次启动会自动建（SQLite/PG 均已接线）
		}
		status.Required = true
		status.pendingVersions = append(status.pendingVersions, migrationV1Version)
		status.Guide = "doc/guide/upgrade.md#从旧版本升级-含-r5-b4-并发修复"
		if precheck.ForwardPortDups > 0 {
			var sample string
			r.db.Raw(`SELECT 'node_id=' || node_id || ', port=' || port FROM forward_port GROUP BY node_id, port HAVING COUNT(*) > 1 LIMIT 1`).Scan(&sample)
			status.Tables = append(status.Tables, DuplicateDetail{Table: "forward_port", DuplicateGroups: precheck.ForwardPortDups, Sample: sample})
		}
		if precheck.ChainTunnelDups > 0 {
			status.Tables = append(status.Tables, DuplicateDetail{Table: "chain_tunnel", DuplicateGroups: precheck.ChainTunnelDups})
		}
		if precheck.NodeInboundDups > 0 {
			status.Tables = append(status.Tables, DuplicateDetail{Table: "node_inbound", DuplicateGroups: precheck.NodeInboundDups})
		}
		if precheck.UsernameDups > 0 {
			status.Tables = append(status.Tables, DuplicateDetail{Table: "user", DuplicateGroups: precheck.UsernameDups})
		}
		return status
	}

	// v1 已应用：再查 v2（node_inbound 部分唯一索引）。
	if !applied(migrationV2Version) {
		if def, exists, err := nodeInboundIndexDefinition(r.db); err == nil && exists && !nodeInboundIndexIsPartial(def) {
			status.Required = true
			status.pendingVersions = append(status.pendingVersions, migrationV2Version)
			status.Guide = "doc/guide/upgrade.md#从旧版本升级-含-r5-b4-并发修复"
			status.Tables = append(status.Tables, DuplicateDetail{
				Table:  "node_inbound",
				Sample: "索引 " + nodeInboundIndexName + " 仍为全量唯一定义，v2 部分索引迁移待处理（通常是 kernel 入站存在同节点同端口重复，需先清理）",
			})
		}
	}

	// C1-01：v3（peer_share_runtime 复合唯一索引）此前从不查——
	// v3 被预检挡下时不记版本，复合索引可长期缺失而状态面恒报
	// 无待迁移，联邦第二消费方 reserve-port 撞旧单列唯一键的形
	// 态完全不可见。判据与迁移本体同形：版本未记且（复合索引
	// 不在 或 旧单列索引仍在）即待处理。
	if !applied(migrationV3Version) && r.db.Migrator().HasTable(peerShareRuntimeTableName) {
		composite, compErr := peerShareRuntimeIndexExists(r.db, compositeResourceIndexName)
		legacy, legErr := peerShareRuntimeIndexExists(r.db, legacyResourceKeyIndexName)
		if compErr == nil && legErr == nil && (!composite || legacy) {
			status.Required = true
			status.pendingVersions = append(status.pendingVersions, migrationV3Version)
			status.Guide = "doc/guide/upgrade.md#从旧版本升级-含-r5-b4-并发修复"
			sample := "复合唯一索引 " + compositeResourceIndexName + " 缺失，v3 迁移待处理（通常是同 share 下 resource_key 重复，需先清理）"
			if legacy {
				sample = "旧单列唯一索引 " + legacyResourceKeyIndexName + " 仍在、复合唯一索引未落地，v3 迁移待处理（通常是同 share 下 resource_key 重复，需先清理）"
			}
			status.Tables = append(status.Tables, DuplicateDetail{
				Table:  peerShareRuntimeTableName,
				Sample: sample,
			})
		}
	}

	// C1-01：v4（转发查询普通索引）同查——版本未记且任一索引
	// 缺失即待处理（仅性能面，但状态面不得假阴性）。
	if !applied(migrationC12V4Version) && r.db.Migrator().HasTable("forward_port") && r.db.Migrator().HasTable("forward") {
		var missing []string
		for _, idx := range c12V4Indexes {
			if exists, err := c12IndexExists(r.db, idx.name); err == nil && !exists {
				missing = append(missing, idx.name)
			}
		}
		if len(missing) > 0 {
			status.Required = true
			status.pendingVersions = append(status.pendingVersions, migrationC12V4Version)
			status.Guide = "doc/guide/upgrade.md#从旧版本升级-含-r5-b4-并发修复"
			status.Tables = append(status.Tables, DuplicateDetail{
				Table:  "forward_port",
				Sample: "转发查询索引缺失（" + missing[0] + " 等 " + strconv.Itoa(len(missing)) + " 个），v4 迁移待处理",
			})
		}
	}

	// K10 v5（B02-003）：折叠唯一索引未落地且存量有大小写变体
	// 同名组时如实报告待处理（无变体重复时下次启动自动建，不报）。
	if !applied(migrationK10V5Version) {
		if groups, err := listUsernameFoldDupGroups(r.db); err == nil && len(groups) > 0 {
			status.Required = true
			status.pendingVersions = append(status.pendingVersions, migrationK10V5Version)
			status.Guide = "doc/guide/upgrade.md#从旧版本升级-含-r5-b4-并发修复"
			sample := "大小写变体同名: folded=" + groups[0].Fold + " ids=[" + groups[0].IDs + "] stored=[" + groups[0].Forms + "]"
			status.Tables = append(status.Tables, DuplicateDetail{
				Table:           "user",
				DuplicateGroups: int64(len(groups)),
				Sample:          sample,
			})
		}
	}
	return status
}

// PendingMigrations 保留兼容（返回版本列表）。
// C1-01：返回实际待处理的版本名（可多个），不再恒回 v1 名误标。
func (r *Repository) PendingMigrations() []string {
	if s := r.GetPendingMigrationStatus(); s.Required && len(s.pendingVersions) > 0 {
		return append([]string(nil), s.pendingVersions...)
	}
	return nil
}
