package repo

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
		status.Guide = "doc/guide/upgrade.md#从旧版本升级"
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
			status.Guide = "doc/guide/upgrade.md#从旧版本升级"
			status.Tables = append(status.Tables, DuplicateDetail{
				Table: "node_inbound",
				Sample: "索引 " + nodeInboundIndexName + " 仍为全量唯一定义，v2 部分索引迁移待处理（通常是 kernel 入站存在同节点同端口重复，需先清理）",
			})
		}
	}

	// K10 v5（B02-003）：折叠唯一索引未落地且存量有大小写变体
	// 同名组时如实报告待处理（无变体重复时下次启动自动建，不报）。
	if !applied(migrationK10V5Version) {
		if groups, err := listUsernameFoldDupGroups(r.db); err == nil && len(groups) > 0 {
			status.Required = true
			status.Guide = "doc/guide/upgrade.md#从旧版本升级"
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
func (r *Repository) PendingMigrations() []string {
	if s := r.GetPendingMigrationStatus(); s.Required {
		return []string{migrationV1Version}
	}
	return nil
}
