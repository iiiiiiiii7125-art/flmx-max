package repo

import (
	"gorm.io/gorm"
)

// R5-B4C: 统一用户名可用性检查。
//
// B02-003（用户 2026-10-30 拍板）：判重不区分大小写。本检查与全部
// 判重面共用 username_fold.go 的 usernameFoldCount（LOWER 两侧折叠），
// 不得再写精确比较。写入 user 表用户名的路径全集（K10 枚举闭合，
// 复扫口径见 lanes/K10/r2/username-write-surface.md）：
//   - userCreate → UserExists 预检 + CreateUserTx（事务内折叠复检 +
//     折叠键咨询锁 + v5 函数唯一索引兜底）
//   - userUpdate → UserExistsExcluding 预检 + UpdateUserWithPassword /
//     UpdateUserWithoutPassword（唯一冲突映射兜底）
//   - updatePassword 改名 → UsernameExistsExceptID 预检 +
//     UpdateUserNameAndPassword（唯一冲突映射兜底）
//   - 备份导入 importUsers → 本函数（事务内）+ upsert 冲突映射
//   - seedData 管理员补建 → usernameFoldCount 占用检查（仅 id=1 缺失时）
//   - Repository.CreateUser（非事务版，生产无调用方）→ 唯一索引 +
//     冲突映射兜底，无应用层预检（与改前同构）
//
// 旧注曾列 groupUserCreate/groupUserUpdate：经 K10 复核，二者写的是
// user_group.name（分组名判重属 B03 族），不写 user 表用户名，不在本
// 全集内。
//
// 语义：
//   - 写入前折叠查重（usernameFoldCount 或等价）；
//   - 唯一约束冲突映射为"用户名已存在"，不暴露 SQL；
//   - 并发场景由折叠键咨询锁（PG）/串行连接（SQLite）+ v5 折叠
//     唯一索引兜底；索引因存量折叠重复被 D23 跳过时，应用层判重
//     仍按本口径执法（CreateUserTx 的锁+复检不依赖索引）。
//
// 登录不在判重口径内：GetUserByUsername 保持精确匹配、本次未动——
// 改成折叠匹配会让存量大小写双生子（Admin 与 admin 并存）的登录
// 归属产生歧义、静默改变鉴权语义；新判重只阻止新增双生子，存量
// 仍以各自存储形态精确登录，行为不变。
func ensureUsernameAvailable(tx *gorm.DB, username string, excludeID int64) error {
	// 比较经 Model(&model.User{}) 解析表名（与 CreateUserTx 事务内
	// 复检同形）；不得改回 Table("user") 裸拼——AUDIT-R1 (TST-003)
	// 的引号教训见 git 历史。
	count, err := usernameFoldCount(tx, username, excludeID)
	if err != nil {
		return err
	}
	if count > 0 {
		return errUsernameExists
	}
	return nil
}
