package contract_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// F-080 (audit FINAL §5-B8 / A6-005): 空订阅语义不一致——端点为 0 时
// 返回 503，端点被目标格式全过滤时却返回 200 空体。503 会被订阅客户端
// 当服务端故障处理：保留上一份（可能已删除的）节点列表反复重试。
// 统一口径：两种"无可用节点"都返回 200 + 该格式的空渲染，客户端以
// 空列表如实更新（停用/过期仍走既有 403，不在本条范围）。
// 修复前本测试失败：停用唯一入站后拉取返回 503。
func TestEmptySubscriptionSemanticsUnifiedF080(t *testing.T) {
	secret := "unit" + "-test-secret"
	router, r := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)
	landing := seedKernelNode(t, r, "landing")
	vless := seedInboundOnNode(t, router, r, admin, landing,
		`{"nodeId":`+jsonInt64(landing)+`,"kernelType":"xray","name":"东京 VLESS","protocol":"vless","port":443,"settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality"}}`)

	out := kernelContractPost(t, router, admin, "/api/v1/subscription/save",
		`{"name":"空订阅","sourceAll":false,"inboundIds":[`+jsonInt64(vless)+`]}`)
	if out.Code != 0 {
		t.Fatalf("save: %+v", out)
	}
	path := out.Data.(map[string]interface{})["path"].(string)

	// 有节点时正常 200 且含节点（正例防误伤）。
	resp := getSub(t, router, path+"?format=base64", "")
	body, _ := base64.StdEncoding.DecodeString(resp.Body.String())
	if resp.Code != 200 || !strings.Contains(string(body), "vless://") {
		t.Fatalf("non-empty subscription must render nodes: %d %s", resp.Code, body)
	}

	// 停用唯一入站 → 端点为 0。各格式必须口径一致地 200 + 空渲染。
	if err := r.DB().Exec(`UPDATE node_inbound SET enabled = 0 WHERE id = ?`, vless).Error; err != nil {
		t.Fatal(err)
	}
	resp = getSub(t, router, path+"?format=base64", "")
	body, _ = base64.StdEncoding.DecodeString(resp.Body.String())
	if resp.Code != 200 {
		t.Fatalf("zero endpoints must be 200 (unified with format-filtered empty), got %d", resp.Code)
	}
	if strings.Contains(string(body), "vless://") {
		t.Fatalf("disabled inbound must not render, got %s", body)
	}
	resp = getSub(t, router, path+"?format=clash", "")
	if resp.Code != 200 || strings.Contains(resp.Body.String(), "vless://") {
		t.Fatalf("clash zero-endpoint must be 200 without proxies: %d %s", resp.Code, resp.Body.String())
	}
	resp = getSub(t, router, path+"?format=singbox", "")
	if resp.Code != 200 {
		t.Fatalf("singbox zero-endpoint must be 200, got %d", resp.Code)
	}
}
