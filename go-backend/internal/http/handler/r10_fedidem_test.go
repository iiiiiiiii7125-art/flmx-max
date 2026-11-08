package handler

import (
	"testing"
	"time"
)

// R10 修复批 · fedidem 路回归钉格（LEDGER10 R10-06 / R10-07）。
//
// - R10-06（I-C2-01）：移植轮的 save 型创建端点
//   subscriptionTemplateSave 漏接 Idempotency-Key 幂等族——带键双发各
//   建两行。钉格按 r2_b2 形态：同键重发只建一行且回放首次结果；同键
//   update（ID>0）正常执行不被回放吞掉。（同批的 nodeGroupSave 格已
//   随节点组功能移除一并摘除。）
// （R10-07 联邦 tunnel/create 回放格已随联邦功能删除一并摘除。）

// idemStore 为进程级全局，键加实例唯一后缀防同进程重复跑（-count>1）
// 互相回放污染（r2_b2 登记 #4 同口径）。
func r10FedidemKey(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000000")
}

func TestR10SubscriptionTemplateSaveCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	key := r10FedidemKey("r10-tpl-key")
	content := "proxies: []\nproxy-groups:\n  - name: PROBE\n    type: select\n    include-all: true\nrules:\n  - MATCH,PROBE\n"
	payload := map[string]interface{}{
		"name": "idem-tpl-r10", "kind": "clash", "content": content,
	}
	b1 := r2b2Post(t, h.subscriptionTemplateSave, "/api/v1/subscription/template/save", payload, key)
	r2b2AssertCode0(t, "subscriptionTemplateSave first", b1)
	b2 := r2b2Post(t, h.subscriptionTemplateSave, "/api/v1/subscription/template/save", payload, key)
	if b2 != b1 {
		t.Fatalf("subscriptionTemplateSave 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription_template WHERE name = 'idem-tpl-r10'`); n != 1 {
		t.Fatalf("subscriptionTemplateSave 同键建出 %d 行, want 1", n)
	}

	// 同键 update（ID>0）不参与幂等，必须正常执行改名。
	tplID := int64(mustQueryInt(t, r, `SELECT id FROM subscription_template WHERE name = 'idem-tpl-r10'`))
	update := map[string]interface{}{
		"id": tplID, "name": "idem-tpl-r10-renamed", "kind": "clash", "content": content,
	}
	b3 := r2b2Post(t, h.subscriptionTemplateSave, "/api/v1/subscription/template/save", update, key)
	r2b2AssertCode0(t, "subscriptionTemplateSave update same key", b3)
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription_template WHERE name = 'idem-tpl-r10-renamed'`); n != 1 {
		t.Fatalf("同键 update 应正常执行改名, renamed rows = %d（被回放吞掉）", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription_template WHERE name LIKE 'idem-tpl-r10%'`); n != 1 {
		t.Fatalf("subscription_template rows = %d, want 1", n)
	}
}

func r10TunnelID(t *testing.T, data interface{}) int64 {
	t.Helper()
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("tunnel/create data shape = %T, want object", data)
	}
	f, ok := m["tunnelId"].(float64)
	if !ok || f <= 0 {
		t.Fatalf("tunnelId = %v, want positive number", m["tunnelId"])
	}
	return int64(f)
}
