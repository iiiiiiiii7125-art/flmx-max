package repo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// 订阅模板随 inbounds 段备份往返：模板行按 id 恢复，订阅 Options 中
// 指向已恢复模板的槽位保留；指向不存在模板的槽位在导入时剔除（订阅
// 本体照常恢复、改走内置渲染），不留悬空引用。

func seedTemplateSubscription(t *testing.T, r *Repository, token, options string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO subscription(id, name, kind, token, uuid, password, source_all, inbound_ids, source_ids, options, expire_time, enabled, remark, inx, created_time, updated_time)
		VALUES(10, '模板订阅', 'share', ?, 'uuid-t', 'pw', 0, '', '', ?, 0, 1, '', 0, ?, ?)
	`, token, options, now, now).Error; err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
}

func subscriptionOptionsOf(t *testing.T, r *Repository, token string) map[string]int64 {
	t.Helper()
	var row model.Subscription
	if err := r.DB().Where("token = ?", token).First(&row).Error; err != nil {
		t.Fatalf("load subscription %s: %v", token, err)
	}
	var opts struct {
		Templates map[string]int64 `json:"templates"`
	}
	if err := json.Unmarshal([]byte(row.Options), &opts); err != nil {
		t.Fatalf("parse options %q: %v", row.Options, err)
	}
	return opts.Templates
}

func TestBackupRoundTripsSubscriptionTemplates(t *testing.T) {
	source := openBackupRepo(t, "tpl-src.db")
	if err := source.SaveSubscriptionTemplate(&model.SubscriptionTemplate{
		ID: 7, Name: "机场骨架", Kind: "clash",
		Content: "proxies: []\nproxy-groups:\n  - {name: 节点选择, type: select, proxies: []}\nrules:\n  - MATCH,节点选择\n",
	}); err != nil {
		t.Fatalf("save template: %v", err)
	}
	seedTemplateSubscription(t, source, "tok-tpl", `{"templates":{"clash":7}}`)

	dest := openBackupRepo(t, "tpl-dst.db")
	result := roundTripBackup(t, source, dest)
	if result.InboundTemplatesImported != 1 {
		t.Fatalf("InboundTemplatesImported = %d, want 1", result.InboundTemplatesImported)
	}
	templates, err := dest.ListSubscriptionTemplates()
	if err != nil || len(templates) != 1 {
		t.Fatalf("restored templates = %v, err = %v", templates, err)
	}
	if templates[0].ID != 7 || !strings.Contains(templates[0].Content, "节点选择") {
		t.Fatalf("restored template = %+v", templates[0])
	}
	slots := subscriptionOptionsOf(t, dest, "tok-tpl")
	if slots["clash"] != 7 {
		t.Fatalf("subscription template slots = %v, want clash=7 preserved", slots)
	}
}

func TestBackupImportDropsDanglingTemplateRefs(t *testing.T) {
	source := openBackupRepo(t, "tpl-src2.db")
	seedTemplateSubscription(t, source, "tok-dangle", `{"templates":{"clash":99,"surge":98}}`)

	dest := openBackupRepo(t, "tpl-dst2.db")
	result := roundTripBackup(t, source, dest)
	if result.InboundTemplatesImported != 0 {
		t.Fatalf("InboundTemplatesImported = %d, want 0", result.InboundTemplatesImported)
	}
	slots := subscriptionOptionsOf(t, dest, "tok-dangle")
	if len(slots) != 0 {
		t.Fatalf("dangling template slots survived import: %v", slots)
	}
}
