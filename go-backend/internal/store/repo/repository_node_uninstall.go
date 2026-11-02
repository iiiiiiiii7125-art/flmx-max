package repo

import (
	"errors"
	"strings"
	"time"

	"go-backend/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// nodeUninstallRetention 是待卸载登记的保留期：超过这个时长还没重连
// 的 agent 视为已随机器消亡，登记在下一次写入时顺带清掉，避免表
// 无限增长。
const nodeUninstallRetention = 180 * 24 * time.Hour

// AddNodeUninstall 登记一条待卸载记录。同一 secret 重复登记幂等
// （OnConflict 不覆盖原记录，保留首次登记时间与节点名）。
func (r *Repository) AddNodeUninstall(secret, nodeName string, now time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return errors.New("secret is required")
	}
	if err := r.db.Where("created_time < ?", now.Add(-nodeUninstallRetention).UnixMilli()).Delete(&model.NodeUninstall{}).Error; err != nil {
		return err
	}
	return r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.NodeUninstall{
		Secret:      secret,
		NodeName:    nodeName,
		CreatedTime: now.UnixMilli(),
	}).Error
}

// GetNodeUninstallBySecret 返回该 secret 的待卸载登记；没有登记时
// 返回 (nil, nil)。
func (r *Repository) GetNodeUninstallBySecret(secret string) (*model.NodeUninstall, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, nil
	}
	var row model.NodeUninstall
	err := r.db.Where("secret = ?", secret).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// DeleteNodeUninstall 在 agent 已确认卸载（或确认其版本不支持自卸载、
// 再问也无意义）后清掉登记。
func (r *Repository) DeleteNodeUninstall(secret string) error {
	if r == nil || r.db == nil {
		return errors.New("repository not initialized")
	}
	return r.db.Where("secret = ?", secret).Delete(&model.NodeUninstall{}).Error
}
