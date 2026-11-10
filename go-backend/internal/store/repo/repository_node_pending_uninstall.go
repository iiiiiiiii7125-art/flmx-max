package repo

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-backend/internal/store/model"
)

// C4-G2 待卸载登记的状态集（与 FIXWAVE-PROTOCOL-G2 的状态机
// 一一对应）：issued 已登记/已下发待受理、acked agent 已受理
// 正在执行、succeeded agent 已报终局成功、failed 终局失败或
// 明确拒绝（带原因）、unconfirmed 超时未见终局。
const (
	NodePendingUninstallIssued      = "issued"
	NodePendingUninstallAcked       = "acked"
	NodePendingUninstallSucceeded   = "succeeded"
	NodePendingUninstallFailed      = "failed"
	NodePendingUninstallUnconfirmed = "unconfirmed"
)

// UpsertNodePendingUninstall 登记（或重试时刷新）一条待卸载：
// 状态重置为 issued、issued_at 刷新（新一轮幂等键）、原因清空。
func (r *Repository) UpsertNodePendingUninstall(nodeID int64, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if nodeID <= 0 {
		return errors.New("node id is required")
	}
	ms := now.UnixMilli()
	rec := &model.NodePendingUninstall{
		NodeID:      nodeID,
		State:       NodePendingUninstallIssued,
		IssuedAt:    ms,
		CreatedTime: ms,
		UpdatedTime: ms,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"state", "reason", "issued_at", "updated_time",
		}),
	}).Create(rec).Error
}

// GetNodePendingUninstall 返回节点的待卸载登记；无登记时返回
// (nil, nil)。
func (r *Repository) GetNodePendingUninstall(nodeID int64) (*model.NodePendingUninstall, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var row model.NodePendingUninstall
	err := r.db.Where("node_id = ?", nodeID).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// TransitionNodePendingUninstall 以「当前状态在 from 集内」为条
// 件翻转状态（CAS，与核验记录的 Resolve 同形态）：赢家返回
// true。reason 为空时不改既有原因（结案点的点名原因由调用方
// 在翻转时一并写入）。
func (r *Repository) TransitionNodePendingUninstall(nodeID int64, from []string, to string, reason string, now time.Time) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	updates := map[string]interface{}{
		"state": to, "updated_time": now.UnixMilli(),
	}
	if reason != "" {
		updates["reason"] = reason
	}
	res := r.db.Model(&model.NodePendingUninstall{}).
		Where("node_id = ? AND state IN ?", nodeID, from).
		Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SetNodePendingUninstallReason 只更新原因（状态不变）：供
// succeeded 后面板侧删记录被守卫拒绝时把阻塞原因留在登记上
// 供管理员看见，清扫下轮仍会重试。
func (r *Repository) SetNodePendingUninstallReason(nodeID int64, reason string, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodePendingUninstall{}).
		Where("node_id = ?", nodeID).
		Updates(map[string]interface{}{
			"reason": reason, "updated_time": now.UnixMilli(),
		}).Error
}

// DeleteNodePendingUninstall 清除节点的待卸载登记（节点记录删
// 除/管理员强制处置时调用）。
func (r *Repository) DeleteNodePendingUninstall(nodeID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("node_id = ?", nodeID).Delete(&model.NodePendingUninstall{}).Error
}

// ListNodePendingUninstalls 返回全部待卸载登记（清扫与列表透
// 出共用；量级为「正在卸载中的节点数」，恒小）。
func (r *Repository) ListNodePendingUninstalls() ([]model.NodePendingUninstall, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NodePendingUninstall
	err := r.db.Order("node_id ASC").Find(&rows).Error
	return rows, err
}
