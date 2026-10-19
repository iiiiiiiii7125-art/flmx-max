package repo

import (
	"gorm.io/gorm"
)

// R5-B4C: 统一用户名可用性检查。
//
// 所有写入 user 表用户名的路径必须复用此函数：
//   - userCreate → CreateUser
//   - userUpdate → UpdateUser
//   - groupUserCreate / groupUserUpdate
//   - 备份导入 importUsers
//   - seedData（仅空库时执行）
//
// 语义：
//   - 写入前查重（UserExistsExcluding 或等价）；
//   - 唯一约束冲突映射为"用户名已存在"，不暴露 SQL；
//   - 并发场景由唯一约束兜底。
func ensureUsernameAvailable(tx *gorm.DB, username string, excludeID int64) error {
	var count int64
	query := tx.Model(&struct {
		ID int64
	}{}).Table("\"user\"").Where("\"user\" = ?", username)
	if excludeID > 0 {
		query = query.Where("id != ?", excludeID)
	}
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errUsernameExists
	}
	return nil
}
