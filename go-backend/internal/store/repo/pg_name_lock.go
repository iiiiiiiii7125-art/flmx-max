package repo

import (
	"errors"

	"gorm.io/gorm"
)

// X01-001：事务内「先 Count 复检、再写入」的同名防线在 PG 上的互斥兜底。
//
// 背景：CreateUserTx（用户名）与 InsertTunnelTx / tunnelUpdate 改名复检
// （隧道名）都是「事务内 Count 复检 + 随后 Create/Update」的两步式，
// 且复检被代码自述为唯一防线——用户名唯一索引在 R5-B4 v1 预检发现
// 任意一类重复时按 D23 整批跳过（受支持的降级状态），隧道表则根本
// 没有唯一索引。SQLite 靠 MaxOpenConns(1) 把事务整体串行化，复检
// 成立；PG 默认 READ COMMITTED + 32 连接池下，两个并发事务的 Count
// 都是语句级快照、彼此看不见未提交行，可双双读到 0 后双双落库。
//
// 修法取舍：在复检之前按「归一键 = 命名空间 + 名称原文」取 PG
// 事务级咨询锁 pg_advisory_xact_lock(hashtext(key))。同名并发被
// 串行化：后到者阻塞至先行事务提交/回滚，随后其 Count 必然看见
// 已提交行并按既有哨兵错误拒绝；先行回滚时后到者正常继续。未选
// SERIALIZABLE + 重试的理由：userCreate 的事务还含配额与分组写入、
// tunnelUpdate 事务更长，序列化失败重试需在 handler 层重放整单，
// 侵入面与新错误面都大；咨询锁把改动锁在复检点本身，正常路径行为、
// 既有错误文案与哨兵错误均不变。hashtext 碰撞只会让无关名称偶发
// 串行（锁后仍有精确 Count 把关），不影响正确性。每个受影响事务
// 只取一把名锁，不存在锁序死锁；咨询锁随事务结束自动释放。
//
// SQLite 路径为空操作：其串行化由 MaxOpenConns(1) 提供，已由
// X01 的 SQLite 并发探针实证（8 并发同名恰 1 成功），本文件同名
// 回归测试继续锁定该机理（见 x01_name_lock_test.go）。
const (
	nameLockScopeUser   = "user"
	nameLockScopeTunnel = "tunnel"
)

// lockNameForTx 在 PG 下对 (scope, name) 取事务级咨询锁；其他方言
// 直接放行。必须在持有事务内、复检 Count 之前调用。
func lockNameForTx(tx *gorm.DB, scope, name string) error {
	if tx == nil {
		return errors.New("database unavailable")
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "flmx:name:"+scope+":"+name).Error
}

// LockTunnelNameTx 是隧道改名复检的导出入口：handler 的 tunnelUpdate
// 提交段在调用 TunnelNameExistsTx 之前先取同名咨询锁，与
// InsertTunnelTx 的事务内口径一致（X01-001 同族）。SQLite 下为空操作。
func (r *Repository) LockTunnelNameTx(tx *gorm.DB, name string) error {
	return lockNameForTx(tx, nameLockScopeTunnel, name)
}
