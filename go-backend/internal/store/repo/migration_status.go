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
func (r *Repository) GetPendingMigrationStatus() *PendingMigrationStatus {
	status := &PendingMigrationStatus{Required: false}
	if r == nil || r.db == nil {
		return status
	}
	// 检查 v1 是否已应用。
	if r.db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := r.db.Where("version = ?", migrationV1Version).First(&m).Error; err == nil {
			return status // 已应用，无 pending
		}
	}
	// 未应用：运行预检看是否有重复。
	precheck, err := runV1Precheck(r.db)
	if err != nil {
		return status
	}
	if !precheck.hasDups() {
		return status // 无重复，下次启动会自动建
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

// PendingMigrations 保留兼容（返回版本列表）。
func (r *Repository) PendingMigrations() []string {
	if s := r.GetPendingMigrationStatus(); s.Required {
		return []string{migrationV1Version}
	}
	return nil
}
