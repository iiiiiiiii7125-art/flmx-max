package contract_test

import (
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// F-058/F-136 (B4): 删除订阅的凭据下发此前无重试——节点离线时下发失败
// 仅改响应文案，订阅行已删，60s 同步循环再也看不到它，节点侧孤儿凭据
// 长期有效；且删行与清分配是两步非原子操作，第二步失败仅打日志。
//
// 修复口径：先落墓碑（pending_delete=1 + enabled=0），下发成功后才在
// 单事务内硬删订阅行 + 分配行；失败则墓碑保留，由同步循环重试。
// 本用例的节点恒离线（harness 无 agent），下发必然失败。
func TestSubscriptionDeleteRetriesCredentialPushContract(t *testing.T) {
	secret := "f058-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "f058-node")
	inboundID := seedInboundOnNode(t, router, r, admin, nodeID,
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"F058 VLESS","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)

	out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"待删订阅","kind":"share","inboundIds":[`+jsonInt64(inboundID)+`]}`)
	if out.Code != 0 {
		t.Fatalf("create share sub: %+v", out)
	}
	subID := int64(out.Data.(map[string]interface{})["id"].(float64))

	// 分配给用户 2（直接落库，分配 API 不是本用例对象）。
	if err := r.DB().Exec(`INSERT INTO user_subscription (user_id, subscription_id, created_time) VALUES (2, ?, 1)`, subID).Error; err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	// 第一次删除：节点离线，下发失败 → 墓碑保留、分配行保留。
	out = kernelContractPost(t, router, admin, "/api/v1/subscription/delete", `{"id":`+jsonInt64(subID)+`}`)
	if out.Code != 0 {
		t.Fatalf("delete should be accepted with retry semantics, got %+v", out)
	}
	if !strings.Contains(out.Msg, "下发") {
		t.Fatalf("delete response must disclose the failed push, got msg %q", out.Msg)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE id = ? AND pending_delete = 1 AND enabled = 0`, subID); got != 1 {
		t.Fatalf("F-058: failed push must leave a pending_delete tombstone, subscription rows matching = %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_subscription WHERE subscription_id = ?`, subID); got != 1 {
		t.Fatalf("F-136: assignments must survive until the delete completes, got %d", got)
	}

	// 让下发不再失败（入站行消失后 push 集合为空，下发平凡成功），
	// 再次删除应完成硬删：订阅行与分配行在同一事务内一并消失。
	if err := r.DB().Exec(`DELETE FROM node_inbound WHERE id = ?`, inboundID).Error; err != nil {
		t.Fatalf("drop inbound: %v", err)
	}
	out = kernelContractPost(t, router, admin, "/api/v1/subscription/delete", `{"id":`+jsonInt64(subID)+`}`)
	if out.Code != 0 {
		t.Fatalf("second delete: %+v", out)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE id = ?`, subID); got != 0 {
		t.Fatalf("completed delete must remove the subscription row, got %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT COUNT(*) FROM user_subscription WHERE subscription_id = ?`, subID); got != 0 {
		t.Fatalf("F-136: completed delete must remove assignments atomically, got %d", got)
	}
}
