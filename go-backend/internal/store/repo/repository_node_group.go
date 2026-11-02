package repo

import (
	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// ListNodeGroups returns every node group in display order.
func (r *Repository) ListNodeGroups() ([]model.NodeGroup, error) {
	var out []model.NodeGroup
	err := r.db.Order("inx asc, id asc").Find(&out).Error
	return out, err
}

// GetNodeGroup returns one node group, or nil when it does not exist.
func (r *Repository) GetNodeGroup(id int64) (*model.NodeGroup, error) {
	var g model.NodeGroup
	err := r.db.Where("id = ?", id).First(&g).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// SaveNodeGroup inserts (ID 0) or updates a node group.
func (r *Repository) SaveNodeGroup(g *model.NodeGroup) error {
	if g.ID == 0 {
		return r.db.Create(g).Error
	}
	return r.db.Model(&model.NodeGroup{}).Where("id = ?", g.ID).Updates(map[string]interface{}{
		"name":         g.Name,
		"source_all":   g.SourceAll,
		"inbound_ids":  g.InboundIDs,
		"source_ids":   g.SourceIDs,
		"options":      g.Options,
		"inx":          g.Inx,
		"updated_time": g.UpdatedTime,
	}).Error
}

// DeleteNodeGroup removes a node group. Subscriptions keep the id in
// their group list; rendering skips groups that no longer exist.
func (r *Repository) DeleteNodeGroup(id int64) error {
	return r.db.Where("id = ?", id).Delete(&model.NodeGroup{}).Error
}

// ReorderNodeGroups sets the display order: the ids' positions become
// their inx. Ids that do not exist are ignored.
func (r *Repository) ReorderNodeGroups(ids []int64) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for i, id := range ids {
			if err := tx.Model(&model.NodeGroup{}).Where("id = ?", id).
				Update("inx", i).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// NextNodeGroupIndex is the inx a new node group takes (after the last).
func (r *Repository) NextNodeGroupIndex() (int, error) {
	var maxInx *int
	err := r.db.Model(&model.NodeGroup{}).Select("MAX(inx)").Scan(&maxInx).Error
	if err != nil {
		return 0, err
	}
	if maxInx == nil {
		return 0, nil
	}
	return *maxInx + 1, nil
}
