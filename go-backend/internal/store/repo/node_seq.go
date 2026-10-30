package repo

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// R5-B6 E-001: 上报 seq 幂等。
//
// agent 侧每次上报携带持久化单调递增 seq（重启不回退，落盘）。
// 面板侧按节点记录 last_seq，拒收 seq <= last_seq（重放）。
// 幂等键：(node_id, seq)。
//
// 向后兼容：旧 agent 不带 seq → 接受但标记 legacy，记录计数。
// 保留一个版本的兼容窗口，下个版本移除。

// NodeSeq 记录节点上报 seq。
type NodeSeq struct {
	NodeID  int64 `gorm:"primaryKey;column:node_id"`
	LastSeq int64 `gorm:"column:last_seq;not null;default:0"`
	// LegacyCount 无 seq 上报的次数（兼容窗口观测）。
	LegacyCount int64 `gorm:"column:legacy_count;not null;default:0"`
	UpdatedTime int64 `gorm:"column:updated_time;not null"`
}

func (NodeSeq) TableName() string { return "node_seq" }

// CheckAndUpdateSeq 检查 seq 并更新（原子）。
// 返回：accepted（是否接受）、isReplay（是否为重放）、isLegacy（是否无 seq）。
func (r *Repository) CheckAndUpdateSeq(nodeID int64, seq *int64, now int64) (accepted bool, isReplay bool, isLegacy bool, err error) {
	if r == nil || r.db == nil {
		return false, false, false, gorm.ErrInvalidDB
	}
	// 无 seq → legacy 兼容。
	if seq == nil {
		// 计数+1。B4/F-071: 此前 Exec 的错误被直接丢弃，计数失败无声；
		// 现在把错误返回给调用方（上报路径会记录并按失败处理）。
		// PG-2: DO UPDATE 的右值必须用表名限定原行列（node_seq.legacy_count）；
		// 裸列名在 PG 解析期于目标行与 EXCLUDED 间歧义（SQLSTATE 42702），
		// 空表首插亦报错。限定形式 SQLite 同样接受，两方言同形。
		// SAT2: legacy_count 是上报驱动的持久累加器，+1 必须饱和——裸加在
		// MaxInt64 处会于 SQLite 宽化为 REAL 毒化行、于 PG 报错中止入账，
		// 与 saturatingExcludedAddExpr 同一 CASE 守卫形态（字面量边界、
		// 两侧有类型）。advanceSeqTx 的同名语句必须与此处逐字同形。
		if err := r.db.Exec(`INSERT INTO node_seq (node_id, last_seq, legacy_count, updated_time)
			VALUES (?, 0, 1, ?)
			ON CONFLICT (node_id) DO UPDATE SET legacy_count = CASE WHEN node_seq.legacy_count < 9223372036854775807 THEN node_seq.legacy_count + 1 ELSE node_seq.legacy_count END, updated_time = ?`,
			nodeID, now, now).Error; err != nil {
			return false, false, true, err
		}
		return true, false, true, nil
	}
	// 有 seq：原子检查并更新。
	//
	// B4/F-071: 此前是「事务内 First → 不存在则 Create」的两步式：SQLite
	// 靠 MaxOpenConns(1) 串行化不出问题，PG 上两个并发首次上报会同时
	// 读到 NotFound 并双双 Create，后者撞主键使整条上报被拒。改为单事务
	// 内的冲突安全写法：先 ON CONFLICT DO NOTHING 插入（并发下只有一个
	// 成功），未插入说明行已存在，再做「仅当更大才更新」的条件更新，
	// RowsAffected 判定接受/重放。两步都在同一事务内且各自原子。
	err = r.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&NodeSeq{NodeID: nodeID, LastSeq: *seq, UpdatedTime: now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 1 {
			return nil // 首次插入成功 → 接受
		}
		upd := tx.Model(&NodeSeq{}).
			Where("node_id = ? AND last_seq < ?", nodeID, *seq).
			Updates(map[string]interface{}{"last_seq": *seq, "updated_time": now})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 0 {
			isReplay = true
		}
		return nil
	})
	if err != nil {
		return false, false, false, err
	}
	if isReplay {
		return false, true, false, nil
	}
	return true, false, false, nil
}

// CheckSeq X-P3-31（V-08）：只读重放校验——不推进 last_seq、不计
// legacy。入账失败时 seq 必须保持未推进（agent 同 seq 重报入账），
// 故校验与推进分离：推进由 AdvanceSeq 在入账成功后执行，或由
// ApplyFlowUploadAccounting 与入账同 tx 落地。
func (r *Repository) CheckSeq(nodeID int64, seq *int64) (isReplay bool, isLegacy bool, err error) {
	if r == nil || r.db == nil {
		return false, false, gorm.ErrInvalidDB
	}
	if seq == nil {
		return false, true, nil
	}
	var row NodeSeq
	err = r.db.Where("node_id = ?", nodeID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return *seq <= row.LastSeq, false, nil
}

// ErrSeqReplay 是 R2X-011 的重放哨兵：入账事务内的 seq 推进
// 未能推进（同 seq 已被另一并发入账消费）时返回，整 tx 随之
// 回滚；handler 对该哨兵按重放分支回 ok 不入账，其余错误仍回
// error。CheckSeq 是事务外只读校验，并发双发可同时通过——
// 事务内的 RowsAffected 判定才是最终防线。
var ErrSeqReplay = errors.New("flow upload seq replay")

// advanceSeqTx 在给定事务内推进 seq（seq=nil 时计 legacy 次数）。
// 非 legacy 分支必须恰有 1 行被插入/条件更新才算推进：插入未
// 命中且条件更新 0 行说明 seq <= last_seq（已被并发入账推进），
// 返回 ErrSeqReplay 使整 tx 回滚，杜绝 TOCTOU 双入账。
func advanceSeqTx(tx *gorm.DB, nodeID int64, seq *int64, now int64) error {
	if seq == nil {
		// SAT2: legacy_count +1 饱和守卫，与 CheckAndUpdateSeq 的同名
		// 语句逐字同形（理由见彼处注释）。
		return tx.Exec(`INSERT INTO node_seq (node_id, last_seq, legacy_count, updated_time)
			VALUES (?, 0, 1, ?)
			ON CONFLICT (node_id) DO UPDATE SET legacy_count = CASE WHEN node_seq.legacy_count < 9223372036854775807 THEN node_seq.legacy_count + 1 ELSE node_seq.legacy_count END, updated_time = ?`,
			nodeID, now, now).Error
	}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&NodeSeq{NodeID: nodeID, LastSeq: *seq, UpdatedTime: now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		return nil
	}
	upd := tx.Model(&NodeSeq{}).
		Where("node_id = ? AND last_seq < ?", nodeID, *seq).
		Updates(map[string]interface{}{"last_seq": *seq, "updated_time": now})
	if upd.Error != nil {
		return upd.Error
	}
	if upd.RowsAffected != 1 {
		return ErrSeqReplay
	}
	return nil
}

// AdvanceSeq 在入账成功后推进 seq（独立事务版）。gost 入账主路径
// 使用 ApplyFlowUploadAccounting 的同 tx 推进；本方法供其它调用方
// 在确认入账已提交后推进。
func (r *Repository) AdvanceSeq(nodeID int64, seq *int64, now int64) error {
	if r == nil || r.db == nil {
		return gorm.ErrInvalidDB
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		return advanceSeqTx(tx, nodeID, seq, now)
	})
}
