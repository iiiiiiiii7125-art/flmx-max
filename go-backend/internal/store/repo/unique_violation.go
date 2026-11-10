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

// ErrUsernameExists 是 errUsernameExists 的导出别名：handler 层用
// errors.Is 识别提交段复检/唯一约束命中的同名用户，映射回与锁外
// 预检一致的友好响应（R2X-049）。
var ErrUsernameExists = errUsernameExists

// ErrPortInUse 是 errPortInUse 的导出别名：handler 层用 errors.Is
// 识别端口占用冲突这一已映射的友好错误，在仓储错误收口面（普通
// 用户可达面改走 respondInternalError）保留其原文回执，其余仓储
// 错误才走通用文案（RR4-C1-01）。
var ErrPortInUse = errPortInUse

// ErrTunnelNameDuplicate 是 tunnel 提交段同名复检命中的哨兵错误
// （R2X-049）：tunnel 表无唯一索引，复检拒绝靠它回传 handler。
var ErrTunnelNameDuplicate = errors.New("隧道名称重复")

// ErrForwardNumExceeded / ErrUserTunnelForwardNumExceeded 是
// F2-05 的条数配额哨兵：CreateForwardTx 在事务内（用户行锁下）
// 复检命中上限时返回，handler 映射回与锁外预检一致的友好文案。
// 预检（ensureUserTunnelForwardAllowed）在事务外，并发下可被穿
// 透（基线 live-c：Num=1 并发 10 发成 9），事务内复检是权威面。
var ErrForwardNumExceeded = errors.New("转发数量已达上限")
var ErrUserTunnelForwardNumExceeded = errors.New("该隧道转发数量已达上限")

// ErrNodeNotFound 是节点行不存在的哨兵（R2-C04）：GORM 对不存在的
// 行 Update 不报错，UpdateNodeExpiryReminderDismissed 在
// RowsAffected==0 时以此回「节点不存在」，handler 以 errors.Is
// 分流原样回执、其余仓储错误才走通用文案。
var ErrNodeNotFound = errors.New("节点不存在")

// R5-B4: 唯一约束冲突检测。
//
// 将 DB 层的唯一约束冲突映射为友好错误，避免暴露 SQL 原文。
// 支持 SQLite (UNIQUE constraint failed) 和 PostgreSQL (duplicate key value)。

// isUniqueConstraintViolation 检查 err 是否为唯一约束冲突。
//
// FIX132-C（A3-09）：只认两方言的精确短语。旧实现另含宽子串
// "already exists"，PG 的 DDL 类错误（relation/index already
// exists）会被误判成业务唯一冲突、被映射成友好错误而掩盖真因。
func isUniqueConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value")
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
