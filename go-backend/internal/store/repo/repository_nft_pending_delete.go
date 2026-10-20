package repo

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"go-backend/internal/store/model"
)

// R5-B3 D1-001 / D7: pending-deletion bookkeeping for nftables forwards whose
// data-plane rule removal failed. The forward row (status =
// ForwardStatusPendingDelete) is the marker; this side table carries the retry
// schedule and last error so the background job can retry with backoff and
// operators can observe stuck deletions (补充 E).

// nftPendingDeleteBackoff returns the delay before the next retry given how
// many attempts have already failed. Schedule: 1m, 5m, 30m, then 2h repeating.
// The 24h overall cap is enforced by nftPendingDeleteExhausted.
func nftPendingDeleteBackoff(retryCount int) time.Duration {
	switch {
	case retryCount <= 0:
		return time.Minute
	case retryCount == 1:
		return 5 * time.Minute
	case retryCount == 2:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// nftPendingDeleteMaxAge is the overall cap: after 24h in pending state the
// job stops retrying and surfaces the item for manual handling (补充 E).
const nftPendingDeleteMaxAge = 24 * time.Hour

// MarkForwardPendingDelete marks a forward as pending-delete and (re)creates
// its retry bookkeeping row. It is idempotent: calling it twice refreshes the
// schedule without losing the original creation time.
func (r *Repository) MarkForwardPendingDelete(forwardID, nodeID int64, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if forwardID <= 0 || nodeID <= 0 {
		return errors.New("invalid pending-delete target")
	}
	nowMs := now.UnixMilli()
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Forward{}).Where("id = ?", forwardID).
			UpdateColumns(map[string]interface{}{
				"status":       model.ForwardStatusPendingDelete,
				"updated_time": nowMs,
			}).Error; err != nil {
			return err
		}
		var existing model.NftPendingDelete
		err := tx.Where("forward_id = ?", forwardID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.NftPendingDelete{
				ForwardID:   forwardID,
				NodeID:      nodeID,
				RetryCount:  0,
				NextRetryAt: nowMs + nftPendingDeleteBackoff(0).Milliseconds(),
				CreatedAt:   nowMs,
				UpdatedAt:   nowMs,
			}).Error
		}
		return tx.Model(&model.NftPendingDelete{}).Where("forward_id = ?", forwardID).
			UpdateColumns(map[string]interface{}{
				"node_id":       nodeID,
				"next_retry_at": nowMs + nftPendingDeleteBackoff(existing.RetryCount).Milliseconds(),
				"updated_at":    nowMs,
			}).Error
	})
}

// GetForwardStatusMap 批量返回 forward 的 status（R5-B3 D1-001 矩阵 #9：
// 采集层过滤 pending 行的 delta）。
func (r *Repository) GetForwardStatusMap(forwardIDs []int64) (map[int64]int, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	out := make(map[int64]int, len(forwardIDs))
	if len(forwardIDs) == 0 {
		return out, nil
	}
	ids := make([]int64, 0, len(forwardIDs))
	seen := make(map[int64]struct{}, len(forwardIDs))
	for _, id := range forwardIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID     int64
		Status int
	}
	if err := r.db.Model(&model.Forward{}).Select("id, status").Where("id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.Status
	}
	return out, nil
}

// GetNftPendingDelete 返回单条 pending bookkeeping（不存在返回 nil, nil）。
func (r *Repository) GetNftPendingDelete(forwardID int64) (*model.NftPendingDelete, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var row model.NftPendingDelete
	if err := r.db.Where("forward_id = ?", forwardID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// ListDueNftPendingDeletes returns pending deletions whose next retry is due,
// ordered by schedule time. The caller (background job) groups them by node.
func (r *Repository) ListDueNftPendingDeletes(now time.Time, limit int) ([]model.NftPendingDelete, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	var rows []model.NftPendingDelete
	err := r.db.Where("next_retry_at <= ?", now.UnixMilli()).
		Order("next_retry_at ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListAllNftPendingDeletes returns every pending deletion for the admin
// observability endpoint (补充 E).
func (r *Repository) ListAllNftPendingDeletes() ([]model.NftPendingDelete, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.NftPendingDelete
	err := r.db.Order("created_at ASC").Find(&rows).Error
	return rows, err
}

// RecordNftPendingDeleteAttempt records a failed retry: bumps the counter,
// stores the error, and schedules the next attempt. It reports whether the
// item is now exhausted (past the 24h cap) so the caller can alert once.
func (r *Repository) RecordNftPendingDeleteAttempt(forwardID int64, attemptErr error, now time.Time) (exhausted bool, retErr error) {
	if r == nil || r.db == nil {
		return false, errors.New("repository not initialized")
	}
	nowMs := now.UnixMilli()
	var row model.NftPendingDelete
	if err := r.db.Where("forward_id = ?", forwardID).First(&row).Error; err != nil {
		return false, err
	}
	newCount := row.RetryCount + 1
	msg := ""
	if attemptErr != nil {
		msg = attemptErr.Error()
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
	}
	exhausted = time.Duration(nowMs-row.CreatedAt)*time.Millisecond >= nftPendingDeleteMaxAge
	next := nowMs + nftPendingDeleteBackoff(newCount).Milliseconds()
	if exhausted {
		// Stop scheduling; keep the row for the admin endpoint until manual handling.
		next = 0
	}
	retErr = r.db.Model(&model.NftPendingDelete{}).Where("forward_id = ?", forwardID).
		UpdateColumns(map[string]interface{}{
			"retry_count":   newCount,
			"next_retry_at": next,
			"last_error":    msg,
			"updated_at":    nowMs,
		}).Error
	return exhausted, retErr
}

// DeleteNftPendingDelete removes the bookkeeping row after the forward was
// finally deleted (reconcile success or force-delete).
func (r *Repository) DeleteNftPendingDelete(forwardID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("forward_id = ?", forwardID).Delete(&model.NftPendingDelete{}).Error
}

// ForwardPortConflict describes one forward occupying a (node_id, port).
// R5-B3 补充 B: returned inside the caller's tx — never open a second query.
//
// O2-003: chain_tunnel 同样占用节点端口。命中 chain_tunnel 时 ForwardID 为
// 对应链式记录的 ID，Source 为 "chain_tunnel"；命中 forward_port 时 Source
// 为 "forward"。
type ForwardPortConflict struct {
	ForwardID int64
	Name      string
	Status    int
	Source    string
}

// FindOtherForwardOnNodePort is the non-tx wrapper. Safe only when the caller
// holds no tx (SQLite MaxOpenConns(1)); tx holders must use the Tx version.
func (r *Repository) FindOtherForwardOnNodePort(nodeID int64, port int, currentForwardID int64) (*ForwardPortConflict, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	return r.FindOtherForwardOnNodePortTx(r.db, nodeID, port, currentForwardID)
}

// FindOtherForwardOnNodePortTx reports the first other forward occupying
// (nodeID, port), including its status so the caller can distinguish a normal
// occupant from a pending-delete occupant (纠正 C). It MUST run inside the
// caller's tx (补充 B: no second global query — that is the C-001 pattern).
func (r *Repository) FindOtherForwardOnNodePortTx(tx *gorm.DB, nodeID int64, port int, currentForwardID int64) (*ForwardPortConflict, error) {
	if tx == nil {
		return nil, errors.New("database unavailable")
	}
	if nodeID <= 0 || port <= 0 {
		return nil, nil
	}
	var row struct {
		ForwardID int64
		Name      string
		Status    int
	}
	err := tx.Model(&model.ForwardPort{}).
		Select("forward_port.forward_id AS forward_id, forward.name AS name, COALESCE(forward.status, 1) AS status").
		Joins("LEFT JOIN forward ON forward.id = forward_port.forward_id").
		Where("forward_port.node_id = ? AND forward_port.port = ? AND forward_port.forward_id <> ?",
			nodeID, port, currentForwardID).
		Order("forward_port.forward_id ASC").
		Limit(1).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ForwardID != 0 {
		return &ForwardPortConflict{ForwardID: row.ForwardID, Name: row.Name, Status: row.Status, Source: "forward"}, nil
	}

	// O2-003：chain_tunnel 也有 idx_chain_tunnel_node_port 唯一索引，同样占用节点端口。
	// 预检遗漏它会导致跨表端口冲突漏检。chain_tunnel 的 Port 是 NullInt64，
	// 只有 port > 0 才真实占用监听端口（GetUsedPortsOnNodeAsMap 同样语义）。
	var ct struct {
		ID       int64
		TunnelID int64
	}
	err = tx.Model(&model.ChainTunnel{}).
		Select("id, tunnel_id").
		Where("node_id = ? AND port = ?", nodeID, port).
		Order("id ASC").
		Limit(1).
		Scan(&ct).Error
	if err != nil {
		return nil, err
	}
	if ct.ID == 0 {
		return nil, nil
	}
	var tunnelName string
	_ = tx.Model(&model.Tunnel{}).Select("name").Where("id = ?", ct.TunnelID).Scan(&tunnelName).Error
	return &ForwardPortConflict{ForwardID: ct.ID, Name: tunnelName, Status: 1, Source: "chain_tunnel"}, nil
}
