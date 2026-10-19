package repo_test

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R5-B7: 验证级联删除无孤儿。
func TestB7_DeleteNodeCascadeNoOrphans(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "b7.db"))
	if err != nil {
		t.Fatal(err)
	}
	// 创建节点及关联数据。
	node := &model.Node{Name: "test-node"}
	if err := r.DB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	// SSH 配置。
	r.DB().Create(&model.NodeSSHConfig{NodeID: node.ID, Host: "1.2.3.4"})
	// nft binding。
	r.DB().Create(&model.NftRuleBinding{NodeID: node.ID, ForwardID: 999})
	// 删除。
	if err := r.DeleteNodeCascade(node.ID); err != nil {
		t.Fatal(err)
	}
	// 断言无孤儿。
	var c int64
	r.DB().Model(&model.NodeSSHConfig{}).Where("node_id = ?", node.ID).Count(&c)
	if c != 0 {
		t.Errorf("node_ssh_config orphan: %d", c)
	}
	r.DB().Model(&model.NftRuleBinding{}).Where("node_id = ?", node.ID).Count(&c)
	if c != 0 {
		t.Errorf("nft_rule_binding orphan: %d", c)
	}
}
