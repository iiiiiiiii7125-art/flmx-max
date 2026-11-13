package repo

import (
	"errors"

	"go-backend/internal/store/model"
)

// InsertNodeLatencySamples stores one round of latency measurements.
func (r *Repository) InsertNodeLatencySamples(samples []model.NodeLatencySample) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if len(samples) == 0 {
		return nil
	}
	return r.db.CreateInBatches(samples, 200).Error
}

// ListNodeLatencySamples returns a node's samples since the given time,
// oldest first.
func (r *Repository) ListNodeLatencySamples(nodeID, sinceMs int64) ([]model.NodeLatencySample, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.NodeLatencySample
	err := r.db.Where("node_id = ? AND created_time >= ?", nodeID, sinceMs).
		Order("created_time ASC, id ASC").Find(&out).Error
	return out, err
}

// PruneNodeLatencySamples deletes samples older than the cutoff.
// L3-03（SWEEP-CPU）：与其它指标 prune 同口径分批删（每批 1000
// 行、各自提交），停机追赶时不长持写锁。
func (r *Repository) PruneNodeLatencySamples(olderThanMs int64) error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.deleteRowsInBatches((&model.NodeLatencySample{}).TableName(), "created_time < ?", olderThanMs)
}
