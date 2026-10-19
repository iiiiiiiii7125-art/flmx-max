package repo

import (
	"gorm.io/gorm"
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
		// 计数+1。
		r.db.Exec(`INSERT INTO node_seq (node_id, last_seq, legacy_count, updated_time)
			VALUES (?, 0, 1, ?)
			ON CONFLICT (node_id) DO UPDATE SET legacy_count = legacy_count + 1, updated_time = ?`,
			nodeID, now, now)
		return true, false, true, nil
	}
	// 有 seq：原子检查并更新。
	err = r.db.Transaction(func(tx *gorm.DB) error {
		var ns NodeSeq
		if err := tx.Where("node_id = ?", nodeID).First(&ns).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				// 首次：直接插入。
				return tx.Create(&NodeSeq{NodeID: nodeID, LastSeq: *seq, UpdatedTime: now}).Error
			}
			return err
		}
		if *seq <= ns.LastSeq {
			isReplay = true
			return nil // 不更新，标记为重放
		}
		return tx.Model(&NodeSeq{}).Where("node_id = ?", nodeID).
			Updates(map[string]interface{}{"last_seq": *seq, "updated_time": now}).Error
	})
	if err != nil {
		return false, false, false, err
	}
	if isReplay {
		return false, true, false, nil
	}
	return true, false, false, nil
}
