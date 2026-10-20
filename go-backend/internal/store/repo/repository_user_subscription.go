package repo

import (
	"errors"
	"time"

	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// ListUserSubscriptionIDs returns the subscription IDs assigned to a user
// (1.0.12: 按订阅分配）.
func (r *Repository) ListUserSubscriptionIDs(userID int64) ([]int64, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	var rows []model.UserSubscription
	if err := r.db.Where("user_id = ?", userID).Order("subscription_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.SubscriptionID)
	}
	return out, nil
}

// SetUserSubscriptions replaces all subscription assignments for a user.
// ids are deduplicated and non-positive values dropped; existence of the
// subscriptions is validated by the caller (handler).
func (r *Repository) SetUserSubscriptions(userID int64, ids []int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	seen := make(map[int64]bool, len(ids))
	clean := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserSubscription{}).Error; err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		for _, id := range clean {
			row := model.UserSubscription{UserID: userID, SubscriptionID: id, CreatedTime: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListUserSubscriptions returns the subscriptions assigned to a user,
// ordered the same way as ListSubscriptions (inx, id).
func (r *Repository) ListUserSubscriptions(userID int64) ([]model.Subscription, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	ids, err := r.ListUserSubscriptionIDs(userID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []model.Subscription{}, nil
	}
	var out []model.Subscription
	if err := r.db.Where("id IN ?", ids).Order("inx ASC, id ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteUserSubscriptionsBySubscription removes all assignments pointing at
// a subscription (called when the subscription is deleted).
func (r *Repository) DeleteUserSubscriptionsBySubscription(subscriptionID int64) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("subscription_id = ?", subscriptionID).Delete(&model.UserSubscription{}).Error
}
