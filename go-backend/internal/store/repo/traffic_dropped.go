package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 流量死信字节的面板入账（流量审计 A-中-1）：agent 侧死信
// 隔离的未确认字节计入 spool 的 DroppedBytes、随下批 v2 信封以
// dropped_bytes 上行；面板此前信封无该字段，编码解码时静默丢
// 弃、死信量全仓无入账引用。入账口径与 ConnlogNodeDropped
// （repo/connlog.go）同构：随批报上行的是「尚未被面板确认」的
// 死信快照——同一数值会随后续批次反复出现，直到某批被真正应
// 用、agent 才把它从待确认计数里扣掉。故面板只在 ack.Applied
// 时把本批数值作增量入账（traffic_node_dropped 按节点累计），
// 重放/落后批次不重复入账。

// AddTrafficNodeDropped 把一个已应用批次携带的死信字节增量入账
// 到上报节点名下（upsert 累加）。调用方（handler）只在批次 ack
// 为 Applied 时调用一次。
func (r *Repository) AddTrafficNodeDropped(nodeID, delta, nowMs int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if delta <= 0 {
		return nil
	}
	row := model.TrafficNodeDropped{NodeID: nodeID, DroppedTotal: delta, UpdatedTime: nowMs}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"dropped_total": gorm.Expr("traffic_node_dropped.dropped_total + ?", delta),
			"updated_time":  nowMs,
		}),
	}).Create(&row).Error
}

// ListTrafficNodeDropped 返回全部节点的累计流量死信字节
// （node_id -> dropped_total），供管理面按节点展示。
func (r *Repository) ListTrafficNodeDropped() (map[int64]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.TrafficNodeDropped
	if err := r.db.Model(&model.TrafficNodeDropped{}).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, row := range rows {
		out[row.NodeID] = row.DroppedTotal
	}
	return out, nil
}

// SumTrafficNodeDroppedByNodes 汇总一组节点的累计流量死信字节。
// 无行按 0 计。
func (r *Repository) SumTrafficNodeDroppedByNodes(nodeIDs []int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	if len(nodeIDs) == 0 {
		return 0, nil
	}
	var total int64
	for _, chunk := range chunkInt64IDs(nodeIDs) {
		var sub int64
		err := r.db.Model(&model.TrafficNodeDropped{}).
			Where("node_id IN ?", chunk).
			Select("COALESCE(SUM(dropped_total), 0)").
			Scan(&sub).Error
		if err != nil {
			return 0, err
		}
		total += sub
	}
	return total, nil
}
