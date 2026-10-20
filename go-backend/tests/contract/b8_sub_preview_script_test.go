package contract_test

import (
	"testing"

	"go-backend/internal/auth"
)

// F-024 (audit FINAL §5-B8 / A6-003): 订阅脚本运行时出错时，预览此前直接
// 丢弃全部节点（items 空、dropped=全部），与 guide「脚本执行失败不影响
// 其他步骤的结果」相悖。修复前本测试失败：items 为 0。
func TestSubscriptionPreviewKeepsNodesOnScriptErrorF024(t *testing.T) {
	secret := "unit" + "-test-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "landing")
	seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"东京 VLESS","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)
	seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"香港 SS","protocol":"shadowsocks","port":8388}`)

	out := kernelContractPost(t, router, admin, "/api/v1/subscription/preview",
		`{"sourceAll":true,"options":{"script":"function operator(nodes){ throw new Error('boom') }"}}`)
	if out.Code != 0 {
		t.Fatalf("preview: %+v", out)
	}
	prev := out.Data.(map[string]interface{})
	items, _ := prev["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("script error must keep the other pipeline steps' nodes, got %d items: %v", len(items), prev)
	}
	if se, _ := prev["scriptError"].(string); se == "" {
		t.Fatalf("preview must surface scriptError, got %v", prev)
	}
	if dropped := valueAsInt(prev["dropped"]); dropped != 0 {
		t.Fatalf("nothing was filtered, dropped must be 0, got %d (%v)", dropped, prev)
	}

	// 无脚本错误时不带 scriptError 字段（或为空），结果不变。
	ok := kernelContractPost(t, router, admin, "/api/v1/subscription/preview",
		`{"sourceAll":true,"options":{"script":"function operator(nodes){ return nodes }"}}`).Data.(map[string]interface{})
	if se, _ := ok["scriptError"].(string); se != "" {
		t.Fatalf("valid script must not report scriptError, got %q", se)
	}
	if items, _ := ok["items"].([]interface{}); len(items) != 2 {
		t.Fatalf("valid script preview must list 2 items: %v", ok)
	}
}
