package repo

// R5-B4C: 查询待执行的迁移（用于健康检查可观测性）。

// PendingMigrations 返回未应用的迁移版本列表。
// 目前仅检查 r5b4_v1（预检失败时未记录版本即为 pending）。
func (r *Repository) PendingMigrations() []string {
	if r == nil || r.db == nil {
		return nil
	}
	var pending []string
	// 检查 v1 是否已应用。
	if r.db.Migrator().HasTable(&schemaMigration{}) {
		var m schemaMigration
		if err := r.db.Where("version = ?", migrationV1Version).First(&m).Error; err != nil {
			// 未找到记录 → pending（可能是预检失败跳过）。
			// 进一步确认：运行预检看是否有重复。
			if precheck, err := runV1Precheck(r.db); err == nil && precheck.hasDups() {
				pending = append(pending, migrationV1Version)
			}
		}
	} else {
		// 表不存在 → 迁移未运行过。
		pending = append(pending, migrationV1Version)
	}
	return pending
}
