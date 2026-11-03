package handler

import (
	"fmt"
	"testing"
	"time"
)

// R10 修复批 · fedidem 路回归钉格（LEDGER10 R10-06 / R10-07）。
//
// - R10-06（I-C2-01）：移植轮两个 save 型创建端点（nodeGroupSave、
//   subscriptionTemplateSave）漏接 Idempotency-Key 幂等族——带键双发各
//   建两行。钉格按 r2_b2 形态：同键重发只建一行且回放首次结果；同键
//   update（ID>0）正常执行不被回放吞掉。
// - R10-07（I-C2-02）：联邦 tunnel/create 无重投回放语义——首投建成
//   而回执丢失后，同参重试被 (share, port) 占位检查恒 403 挡死。钉格：
//   同 (share, port) 且 Target/Protocol 一致时回放既有 tunnelId；不一
//   致维持 403；新端口仍可正常新建。

// idemStore 为进程级全局，键加实例唯一后缀防同进程重复跑（-count>1）
// 互相回放污染（r2_b2 登记 #4 同口径）。
func r10FedidemKey(prefix string) string {
	return prefix + "-" + time.Now().Format("150405.000000000")
}

func TestR10NodeGroupSaveCreateIdempotent(t *testing.T) {
	r, h := r2b2Open(t)
	key := r10FedidemKey("r10-ngroup-key")
	payload := map[string]interface{}{
		"name": "idem-ngroup-r10", "sourceAll": true,
		"inboundIds": []int64{}, "sourceIds": []int64{},
		"options": map[string]interface{}{},
	}
	b1 := r2b2Post(t, h.nodeGroupSave, "/api/v1/subscription/group/save", payload, key)
	r2b2AssertCode0(t, "nodeGroupSave first", b1)
	b2 := r2b2Post(t, h.nodeGroupSave, "/api/v1/subscription/group/save", payload, key)
	if b2 != b1 {
		t.Fatalf("nodeGroupSave 同键应回放首次结果:\n%s\n%s", b1, b2)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_group WHERE name = 'idem-ngroup-r10'`); n != 1 {
		t.Fatalf("nodeGroupSave 同键建出 %d 行, want 1", n)
	}

	// 同键 update（ID>0）不参与幂等，必须正常执行改名。
	groupID := int64(mustQueryInt(t, r, `SELECT id FROM node_group WHERE name = 'idem-ngroup-r10'`))
	update := map[string]interface{}{
		"id": groupID, "name": "idem-ngroup-r10-renamed", "sourceAll": true,
		"inboundIds": []int64{}, "sourceIds": []int64{},
		"options": map[string]interface{}{},
	}
	b3 := r2b2Post(t, h.nodeGroupSave, "/api/v1/subscription/group/save", update, key)
	r2b2AssertCode0(t, "nodeGroupSave update same key", b3)
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_group WHERE name = 'idem-ngroup-r10-renamed'`); n != 1 {
		t.Fatalf("同键 update 应正常执行改名, renamed rows = %d（被回放吞掉）", n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM node_group WHERE name LIKE 'idem-ngroup-r10%'`); n != 1 {
		t.Fatalf("node_group rows = %d, want 1", n)
	}
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

func TestR10FederationTunnelCreateReplay(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 20000, 20010)
	create := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "1.1.1.1:443"}

	first := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if first.Code != 0 {
		t.Fatalf("tunnel/create first: expected code 0, got %d (%s)", first.Code, first.Msg)
	}
	tunnelID := r10TunnelID(t, first.Data)

	// 同参重投（首投回执丢失后的用户重试）：必须回放既有 tunnelId，
	// 不得 403、不得重复建隧道/runtime。
	second := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, create, h.federationTunnelCreate)
	if second.Code != 0 {
		t.Fatalf("tunnel/create same-params retry: expected replay code 0, got %d (%s)", second.Code, second.Msg)
	}
	if got := r10TunnelID(t, second.Data); got != tunnelID {
		t.Fatalf("tunnel/create retry tunnelId = %d, want replay of %d", got, tunnelID)
	}
	name := fmt.Sprintf("Share-%d-Port-%d", share.ID, 20005)
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM tunnel WHERE name = ?`, name); n != 1 {
		t.Fatalf("tunnels named %q = %d, want 1", name, n)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(1) FROM peer_share_runtime WHERE share_id = ? AND port = 20005 AND status = 1`, share.ID); n != 1 {
		t.Fatalf("active runtimes for port 20005 = %d, want 1", n)
	}

	// 同端口异目标：不是同一笔提交的重投，维持 403 防占用。
	other := federationTunnelRequest{Protocol: "tcp", RemotePort: 20005, Target: "2.2.2.2:443"}
	denied := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, other, h.federationTunnelCreate)
	if denied.Code != 403 {
		t.Fatalf("tunnel/create same port other target: expected 403, got %d (%s)", denied.Code, denied.Msg)
	}

	// 同端口异协议：同样维持 403。
	otherProto := federationTunnelRequest{Protocol: "udp", RemotePort: 20005, Target: "1.1.1.1:443"}
	denied = fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, otherProto, h.federationTunnelCreate)
	if denied.Code != 403 {
		t.Fatalf("tunnel/create same port other protocol: expected 403, got %d (%s)", denied.Code, denied.Msg)
	}

	// 回放不得污染后续新建：另一端口同目标仍可正常建成新隧道。
	fresh := federationTunnelRequest{Protocol: "tcp", RemotePort: 20006, Target: "1.1.1.1:443"}
	created := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token, fresh, h.federationTunnelCreate)
	if created.Code != 0 {
		t.Fatalf("tunnel/create fresh port: expected code 0, got %d (%s)", created.Code, created.Msg)
	}
	if got := r10TunnelID(t, created.Data); got == tunnelID {
		t.Fatalf("tunnel/create fresh port reused tunnelId %d, want a new tunnel", got)
	}
}
