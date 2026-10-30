package repo

import (
	"errors"
	"strings"

	"go-backend/internal/store/model"
)

func (r *Repository) ListMonitorNodes() ([]model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var nodes []model.Node
	err := r.db.Select("id", "inx", "name", "status", "version", "updated_time", "latency_targets").
		Where("is_remote = ?", 0).
		Order("inx ASC, id ASC").
		Find(&nodes).Error
	return nodes, err
}

// SetNodesLatencyTargets 存储节点自选的延迟测试点（"" 表示跟随全局默认）。
// 移植自 kure29/flyx。
//
// R2 X01-001（硬化）：被调层按 chunkInt64IDs 分块，与 IN 分块家族
// 口径对齐——当前唯一批量调用方有 200 条硬上限、影响不可达，但
// 函数本身不得依赖调用方守约，未来新增无上限调用方会直接撞
// SQLite/PG 的 SQL 变量上限。
func (r *Repository) SetNodesLatencyTargets(ids []int64, value string) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(ids) == 0 {
		return nil
	}
	for _, chunk := range chunkInt64IDs(ids) {
		if err := r.db.Model(&model.Node{}).Where("id IN ? AND is_remote = ?", chunk, 0).
			Update("latency_targets", value).Error; err != nil {
			return err
		}
	}
	return nil
}

// ListNodeLatencyTargets returns every node's stored explicit latency
// test-point choice (raw JSON), keyed by node id. An empty value means
// "follow the global defaults" and is omitted, so callers can tell an
// explicit choice (including an explicit empty one) from no choice.
func (r *Repository) ListNodeLatencyTargets() (map[int64]string, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var nodes []model.Node
	if err := r.db.Select("id", "latency_targets").Find(&nodes).Error; err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		if strings.TrimSpace(n.LatencyTargets) == "" {
			continue
		}
		out[n.ID] = n.LatencyTargets
	}
	return out, nil
}

// ListNodesWithExpiry returns local nodes that have an expiry time set.
func (r *Repository) ListNodesWithExpiry() ([]model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var nodes []model.Node
	err := r.db.Select("id", "name", "expiry_time", "renewal_cycle", "expiry_reminder_dismissed").
		Where("is_remote = ? AND expiry_time IS NOT NULL AND expiry_time > 0", 0).
		Order("id ASC").
		Find(&nodes).Error
	return nodes, err
}

// ListUsersForNotify returns the fields the quota and expiry reminders need.
func (r *Repository) ListUsersForNotify() ([]model.User, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var users []model.User
	err := r.db.Select("id", "user", "role_id", "status", "exp_time", "flow", "flow_mib", "in_flow", "out_flow").
		Order("id ASC").Find(&users).Error
	return users, err
}

// SumStatisticsFlowSince adds up the hourly forward traffic increments
// recorded since the given time (unix ms).
func (r *Repository) SumStatisticsFlowSince(sinceMs int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var total int64
	err := r.db.Model(&model.StatisticsFlow{}).Where("created_time >= ?", sinceMs).
		Select("COALESCE(SUM(flow), 0)").Scan(&total).Error
	return total, err
}

// SumInboundTraffic returns the cumulative traffic of all inbounds.
func (r *Repository) SumInboundTraffic() (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	var total int64
	err := r.db.Model(&model.NodeInbound{}).Select("COALESCE(SUM(upload_bytes + download_bytes), 0)").Scan(&total).Error
	return total, err
}

// CountNodesOnline returns (online, total) for local nodes.
func (r *Repository) CountNodesOnline() (int64, int64, error) {
	if r == nil || r.db == nil {
		return 0, 0, errors.New("repository not initialized")
	}
	var total, online int64
	if err := r.db.Model(&model.Node{}).Where("is_remote = ?", 0).Count(&total).Error; err != nil {
		return 0, 0, err
	}
	if err := r.db.Model(&model.Node{}).Where("is_remote = ? AND status = ?", 0, 1).Count(&online).Error; err != nil {
		return 0, 0, err
	}
	return online, total, nil
}
