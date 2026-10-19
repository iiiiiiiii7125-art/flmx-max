package repo

import (
	"errors"
	"strings"
)

// R5-B4: 唯一约束冲突的友好错误（不暴露 SQL 原文）。
var (
	errUsernameExists = errors.New("用户名已存在")
	errPortInUse      = errors.New("端口已被占用")
)

// R5-B4: 唯一约束冲突检测。
//
// 将 DB 层的唯一约束冲突映射为友好错误，避免暴露 SQL 原文。
// 支持 SQLite (UNIQUE constraint failed) 和 PostgreSQL (duplicate key value)。

// isUniqueConstraintViolation 检查 err 是否为唯一约束冲突。
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "already exists")
}

// mapUniqueViolation 将唯一约束冲突映射为友好错误。
// constraintHint 用于区分是哪种冲突（username / port）。
func mapUniqueViolation(err error, constraintHint string) error {
	if !isUniqueConstraintViolation(err) {
		return err
	}
	switch constraintHint {
	case "username":
		return errUsernameExists
	case "port":
		return errPortInUse
	default:
		return err
	}
}
