package contract_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// R1-C08（B10-001）：「我的订阅」对按订阅分配的用户显示 nodeCount=0，
// 与其实际复制到的独立链接（path 自带 token、无 ?user=）内容不符。
// 修复口径：mine 的 nodeCount 必须等于无 ?user= 拉取实得端点数；
// 旧 ?user=&sig= 个性化拉取的按分配过滤（FIX2-B）不在此放宽。
func TestC08MineNodeCountMatchesIndependentLink(t *testing.T) {
	secret := "c08-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "c08-node")
	seedInboundOnNode(t, router, r, admin, nodeID,
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"c08 A","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	seedInboundOnNode(t, router, r, admin, nodeID,
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"c08 B","protocol":"vless","port":444,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)

	out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"c08-all","sourceAll":true}`)
	if out.Code != 0 {
		t.Fatalf("save sourceAll subscription: %+v", out)
	}
	subID := int64(out.Data.(map[string]interface{})["id"].(float64))

	// 普通用户只走订阅分配（1.0.12 主模型），无任何旧入站分配。
	seedContractUser(t, r, 2, "c08alice", 1, 1)
	set := kernelContractPost(t, router, admin, "/api/v1/user-subscription/set",
		`{"userId":2,"subscriptionIds":[`+jsonInt64(subID)+`]}`)
	if set.Code != 0 {
		t.Fatalf("assign subscription: %+v", set)
	}

	userToken, err := auth.GenerateToken(2, "c08alice", 1, secret)
	if err != nil {
		t.Fatalf("user token: %v", err)
	}
	mine := kernelContractPost(t, router, userToken, "/api/v1/user-subscription/mine", `{}`)
	if mine.Code != 0 {
		t.Fatalf("mine: %+v", mine)
	}
	subs, ok := mine.Data.(map[string]interface{})["subscriptions"].([]interface{})
	if !ok || len(subs) != 1 {
		t.Fatalf("mine subscriptions: %+v", mine.Data)
	}
	card := subs[0].(map[string]interface{})
	nodeCount := int(card["nodeCount"].(float64))
	path, _ := card["path"].(string)
	if path == "" {
		t.Fatalf("mine card missing path: %+v", card)
	}

	// 卡片实际呈现的独立链接（无 ?user=）拉取实得端点数。
	resp := getSub(t, router, path+"?format=base64", "")
	if resp.Code != 200 {
		t.Fatalf("independent pull: status=%d body=%s", resp.Code, resp.Body.String())
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp.Body.String()))
	if err != nil {
		t.Fatalf("independent pull not base64: %v", err)
	}
	pulled := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			pulled++
		}
	}
	if pulled != 2 {
		t.Fatalf("fixture sanity: independent pull = %d endpoints, want 2", pulled)
	}
	if nodeCount != pulled {
		t.Fatalf("B10-001: mine nodeCount = %d, independent link yields %d", nodeCount, pulled)
	}
}

// R1-C08（B10-002）：subscription/save 静默丢弃不存在的 inboundIds，
// 管理员无从察觉选错。修复口径：不存在 ID 显式报错并点名；伴生
// 入站仍排除（B-06）但在响应 excludedCompanionIds 中标注。
func TestC08SubscriptionSaveUnknownRejectedCompanionAnnotated(t *testing.T) {
	secret := "c08-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "c08-node")
	origin := seedInboundOnNode(t, router, r, admin, nodeID,
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"c08 origin","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	companion := seedInboundOnNode(t, router, r, admin, nodeID,
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"c08 companion","protocol":"vless","port":444,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	// 把第二条标为第一条的伴生入站（validate 只读 rows 的 companion_of）。
	if err := r.DB().Exec(`UPDATE node_inbound SET companion_of = ? WHERE id = ?`, origin, companion).Error; err != nil {
		t.Fatal(err)
	}

	// 不存在 ID 与真 ID 混传：整体报错、点名不存在 ID、不落库。
	bad := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"c08-bad","sourceAll":false,"inboundIds":[`+jsonInt64(origin)+`,77777]}`)
	if bad.Code == 0 {
		t.Fatalf("save with unknown inbound id must fail, got %+v", bad)
	}
	if !strings.Contains(bad.Msg, "77777") {
		t.Fatalf("error must name the unknown id, got msg=%q", bad.Msg)
	}
	if n := mustQueryInt(t, r, `SELECT COUNT(*) FROM subscription WHERE name = 'c08-bad'`); n != 0 {
		t.Fatalf("refused save must not persist, found %d rows", n)
	}

	// 伴生 ID 与真 ID 混传：保存成功、伴生被排除且在响应中标注。
	okOut := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"c08-mixed","sourceAll":false,"inboundIds":[`+jsonInt64(origin)+`,`+jsonInt64(companion)+`]}`)
	if okOut.Code != 0 {
		t.Fatalf("save with companion id must succeed: %+v", okOut)
	}
	data := okOut.Data.(map[string]interface{})
	ids, _ := data["inboundIds"].([]interface{})
	if len(ids) != 1 || int64(ids[0].(float64)) != origin {
		t.Fatalf("companion must be excluded from stored inboundIds, got %v", data["inboundIds"])
	}
	excluded, _ := data["excludedCompanionIds"].([]interface{})
	if len(excluded) != 1 || int64(excluded[0].(float64)) != companion {
		t.Fatalf("companion exclusion must be annotated in response, got %v", data["excludedCompanionIds"])
	}
}
