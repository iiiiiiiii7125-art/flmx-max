package repo

import (
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
		if err := r.db.Exec(`INSERT INTO node_seq (node_id, last_seq, legacy_count, updated_time)
			VALUES (?, 0, 1, ?)
			ON CONFLICT (node_id) DO UPDATE SET legacy_count = legacy_count + 1, updated_time = ?`,
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
