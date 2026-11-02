package repo

import (
	"errors"

	"go-backend/internal/store/model"
)

// Subscription templates (model.SubscriptionTemplate): admin-supplied
// profile skeletons subscriptions render into.

func (r *Repository) ListSubscriptionTemplates() ([]model.SubscriptionTemplate, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var out []model.SubscriptionTemplate
	err := r.db.Order("id ASC").Find(&out).Error
	return out, err
}

func (r *Repository) GetSubscriptionTemplate(id int64) (*model.SubscriptionTemplate, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var t model.SubscriptionTemplate
	res := r.db.Where("id = ?", id).Limit(1).Find(&t)
	if res.Error != nil || res.RowsAffected == 0 {
		return nil, res.Error
	}
	return &t, nil
}

// SaveSubscriptionTemplate creates the row when ID is 0, else updates it.
func (r *Repository) SaveSubscriptionTemplate(t *model.SubscriptionTemplate) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	if t.ID == 0 {
		return r.db.Create(t).Error
	}
	return r.db.Save(t).Error
}

func (r *Repository) DeleteSubscriptionTemplate(id int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("id = ?", id).Delete(&model.SubscriptionTemplate{}).Error
}
