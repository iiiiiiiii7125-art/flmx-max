package repo

import (
	"errors"

	"go-backend/internal/store/model"

	"gorm.io/gorm/clause"
)

func (r *Repository) ListRemoteSources() ([]model.RemoteSource, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.RemoteSource
	err := r.db.Order("id ASC").Find(&out).Error
	return out, err
}

func (r *Repository) GetRemoteSource(id int64) (*model.RemoteSource, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out model.RemoteSource
	res := r.db.Where("id = ?", id).Limit(1).Find(&out)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &out, nil
}

// SaveRemoteSource stores the user-editable fields (the status fields are
// written by UpdateRemoteSourceStatus).
func (r *Repository) SaveRemoteSource(s *model.RemoteSource) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if s.ID == 0 {
		return r.db.Create(s).Error
	}
	return r.db.Model(&model.RemoteSource{}).Where("id = ?", s.ID).Updates(map[string]interface{}{
		"name": s.Name, "url": s.URL, "interval_hours": s.IntervalHours, "relay_only": s.RelayOnly,
		"kind": s.Kind, "role": s.Role, "enabled": s.Enabled, "updated_time": s.UpdatedTime,
	}).Error
}

func (r *Repository) UpdateRemoteSourceStatus(id int64, fields map[string]interface{}) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.RemoteSource{}).Where("id = ?", id).Updates(fields).Error
}

func (r *Repository) DeleteRemoteSource(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("id = ?", id).Delete(&model.RemoteSource{}).Error
}

// AddRemoteSourceShield records a manually deleted imported node in the
// source's shield registry (R10-04). Idempotent on (source, identity):
// re-deleting a node whose tuple is already shielded is a no-op.
func (r *Repository) AddRemoteSourceShield(s *model.RemoteSourceShield) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}, {Name: "identity_key"}},
		DoNothing: true,
	}).Create(s).Error
}

func (r *Repository) ListRemoteSourceShields(sourceID int64) ([]model.RemoteSourceShield, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.RemoteSourceShield
	err := r.db.Where("source_id = ?", sourceID).Order("id ASC").Find(&out).Error
	return out, err
}

// DeleteRemoteSourceShields clears a source's shield registry; called
// when the source itself is deleted (the registry must not outlive it).
func (r *Repository) DeleteRemoteSourceShields(sourceID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("source_id = ?", sourceID).Delete(&model.RemoteSourceShield{}).Error
}

// DetachSourceInbounds keeps a deleted source's nodes as plain imports.
func (r *Repository) DetachSourceInbounds(sourceID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Model(&model.NodeInbound{}).Where("source_id = ?", sourceID).Update("source_id", 0).Error
}
