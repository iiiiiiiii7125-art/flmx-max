package repo

import (
	"errors"
	"go-backend/internal/store/model"
	"time"
)

// A pending release remains active until the agent acknowledges deletion. This
// keeps its port reserved even if the control connection is unavailable.
func (r *Repository) SetPeerShareRuntimeReleasePending(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShareRuntime{}).Where("id = ? AND status = 1", id).Updates(map[string]interface{}{"release_pending": 1, "updated_time": time.Now().UnixMilli()}).Error
}

// ExpireStalePeerShareReservations releases pure reservations of one share
// that were never applied and have not been touched since cutoffMillis
// (B13-003): a consumer that crashes or abandons a reservation after
// reserve-port must not hold the port forever. Only rows that never entered
// apply qualify (role still empty, never applied, not already releasing);
// role-bearing rows are desired state owned by reconnect reconciliation and
// are never expired here. The release is the same terminal state as
// CompletePeerShareRuntimeRelease, applied in bulk.
func (r *Repository) ExpireStalePeerShareReservations(shareID int64, cutoffMillis int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("repository not initialized")
	}
	res := r.db.Model(&model.PeerShareRuntime{}).
		Where("share_id = ? AND status = 1 AND applied = 0 AND release_pending = 0 AND role = '' AND updated_time < ?", shareID, cutoffMillis).
		Updates(map[string]interface{}{"status": 0, "updated_time": time.Now().UnixMilli()})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (r *Repository) CompletePeerShareRuntimeRelease(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.PeerShareRuntime{}).Where("id = ?", id).Updates(map[string]interface{}{"status": 0, "applied": 0, "release_pending": 0, "updated_time": time.Now().UnixMilli()}).Error
}

func (r *Repository) ListActivePeerShareRuntimesByNode(nodeID int64) ([]model.PeerShareRuntime, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var items []model.PeerShareRuntime
	err := r.db.Where("node_id = ? AND status = 1", nodeID).Order("release_pending DESC, id ASC").Find(&items).Error
	return items, err
}
