package repo

import (
	"encoding/json"
	"testing"

	"go-backend/internal/store/model"
)

func TestMigrateLegacySubscriptions(t *testing.T) {
	path := t.TempDir() + "/sub.db"
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a 3.0.10 database: no subscriptions yet, the old token and
	// settings in vite_config, one shared user.
	db := r.DB()
	if err := db.Where("1 = 1").Delete(&model.Subscription{}).Error; err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	for k, v := range map[string]string{legacySubTokenKey: token, legacySubRename: "a => b", legacySubRegions: "true"} {
		if err := db.Create(&model.ViteConfig{Name: k, Value: v, Time: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.SubUser{Name: "小明", Token: "fedcba9876543210fedcba9876543210", UUID: "u-1",
		Password: "p-1", InboundIDs: "3,5", ExpireTime: 99, Enabled: 1, CreatedTime: 1, UpdatedTime: 1}).Error; err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	r, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	subs, err := r.ListSubscriptions()
	if err != nil || len(subs) != 2 {
		t.Fatalf("subscriptions: %v %v", subs, err)
	}
	def, share := subs[0], subs[1]
	var opts map[string]interface{}
	_ = json.Unmarshal([]byte(def.Options), &opts)
	// 2.1.2：旧 vite 设置里的 rename / regionGroups 随「处理」整块
	// 删除——迁移仍保链接与凭据、仍清旧配置行，但不再把已删设置
	// 写进默认订阅的 Options。
	if def.Kind != model.SubscriptionShare || def.UUID == "" || def.Token != token || def.SourceAll != 1 ||
		opts["rename"] != nil || opts["regionGroups"] != nil {
		t.Fatalf("default keeps the old link, removed settings not carried over: %+v", def)
	}
	if share.Kind != model.SubscriptionShare || share.Token != "fedcba9876543210fedcba9876543210" || share.UUID != "u-1" ||
		share.InboundIDs != "3,5" || share.SourceAll != 0 || share.ExpireTime != 99 {
		t.Fatalf("shared user keeps link, credentials and inbounds: %+v", share)
	}
	var left int64
	db = r.DB()
	db.Model(&model.ViteConfig{}).Where("name = ?", legacySubTokenKey).Count(&left)
	if left != 0 {
		t.Fatal("the old token must be removed so it can't be used again")
	}
	db.Model(&model.SubUser{}).Count(&left)
	if left != 0 {
		t.Fatal("shared users are moved, not copied")
	}
}
