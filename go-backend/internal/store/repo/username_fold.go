package repo

import (
	"strings"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// B02-003（用户 2026-10-30 拍板）：用户名判重不区分大小写——
// `Admin` 与 `admin` 视为同一用户名。本文件是全部判重面的唯一
// 比较口径落点，任何新增的用户名写入/查重路径必须经这里的助手，
// 不得再写裸 `"user" = ?` 精确比较（username_check.go 头注列全集）。
//
// 口径构成（三层，与既有 R5-B4/X01-001 机制对齐、不另起一套）：
//   - 应用层判重：usernameFoldCount 用 SQL LOWER() 两侧折叠比较，
//     SQLite 与 PostgreSQL 同形。方言差异如实登记：SQLite 的
//     LOWER() 仅折叠 ASCII，PG 的 lower() 按库编码/locale 折叠
//     （UTF8 库下含 Unicode）——裁决示例的 ASCII 名在两方言下
//     结论一致，非 ASCII 名的折叠范围随部署方言，属已知边界。
//   - 并发串行化：CreateUserTx 的 PG 咨询锁以 foldUsername（Go
//     strings.ToLower）为键，使大小写变体在锁层互斥（pg_name_lock.go）。
//   - DB 兜底：v5 迁移建函数唯一索引 (LOWER("user"))，见
//     migration_k10_v5.go；存量折叠重复时按 D23 范式跳过建索引、
//     逐组点名日志，应用层判重仍照常执法。
//
// 登录不在本口径内：GetUserByUsername 保持精确匹配（见
// username_check.go 头注的未动理由）。

// foldUsername 返回用户名的 Go 侧折叠形态，仅用于咨询锁键与
// 日志/检测分组（不落库、不改存储形态：存储保留用户输入的大小写）。
func foldUsername(username string) string {
	return strings.ToLower(username)
}

// usernameFoldCount 统计与 username 大小写不敏感同名的用户行数；
// excludeID > 0 时排除该行（改名自查）。db 可为 r.db 或事务句柄。
func usernameFoldCount(db *gorm.DB, username string, excludeID int64) (int64, error) {
	var count int64
	query := db.Model(&model.User{}).Where(`LOWER("user") = LOWER(?)`, username)
	if excludeID > 0 {
		query = query.Where("id != ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// usernameFoldDupGroup 是一组仅大小写不同的存量同名用户。
type usernameFoldDupGroup struct {
	Fold  string `gorm:"column:fold"`
	IDs   string `gorm:"column:ids"`
	Forms string `gorm:"column:forms"`
}

// countUsernameFoldDupGroups 统计折叠重复组数（迁移预检/状态面共用）。
func countUsernameFoldDupGroups(db *gorm.DB) (int64, error) {
	var count int64
	err := db.Raw(`SELECT COUNT(*) FROM (SELECT 1 FROM "user" GROUP BY LOWER("user") HAVING COUNT(*) > 1)`).Scan(&count).Error
	return count, err
}

// listUsernameFoldDupGroups 列出全部折叠重复组（折叠键 + 成员 id +
// 存储形态），供 v5 迁移点名日志与运维定位。成员 id/形态的聚合
// 函数两方言不同（GROUP_CONCAT vs string_agg），按方言分支。
func listUsernameFoldDupGroups(db *gorm.DB) ([]usernameFoldDupGroup, error) {
	agg := `GROUP_CONCAT(id) AS ids, GROUP_CONCAT("user") AS forms`
	if db.Dialector.Name() == "postgres" {
		agg = `string_agg(id::text, ',') AS ids, string_agg("user", ',') AS forms`
	}
	var groups []usernameFoldDupGroup
	err := db.Raw(`SELECT LOWER("user") AS fold, ` + agg + ` FROM "user" GROUP BY LOWER("user") HAVING COUNT(*) > 1 ORDER BY fold`).Scan(&groups).Error
	return groups, err
}
