package contract_test

import (
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// B10-001（Round 2）：POST /inbound/reset-traffic 此前对不存在 id
// 静默 code=0——同族 toggle/update/regenerate 与
// subscription/reset-traffic 均先查行、不存在即报错。本测试锁定：
// 不存在 id 报错「节点服务不存在」，真实 id 仍正常重置。

func TestInboundResetTrafficMissingInboundContract(t *testing.T) {
	secret := "k5-reset-traffic-secret"
	router, r := setupContractRouter(t, secret)
	admin, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	nodeID := seedKernelNode(t, r, "k5-landing")

	// 节点离线：内核登记为 pending、入站可建（下发警告不阻断落库），
	// 与 TestKernelInboundLifecycleContract 同一路径。
	install := kernelContractPost(t, router, admin, "/api/v1/kernel/install",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","version":"latest"}`)
	if install.Code == 0 || !strings.Contains(install.Msg, "不在线") {
		t.Fatalf("offline install should report dispatch failure, got %+v", install)
	}
	kernelID := mustQueryInt64(t, r, `SELECT id FROM node_kernel WHERE node_id = ?`, nodeID)

	create := kernelContractPost(t, router, admin, "/api/v1/inbound/create",
		`{"nodeId":`+jsonInt64(nodeID)+`,"kernelType":"xray","name":"k5-reset","protocol":"vless","port":443,
		  "settings":{"flow":"xtls-rprx-vision"},"security":{"type":"reality","sni":"www.example.com"}}`)
	if create.Code != 0 {
		t.Fatalf("create inbound: %+v", create)
	}
	inboundID := mustQueryInt64(t, r, `SELECT id FROM node_inbound WHERE kernel_id = ?`, kernelID)

	// 真实 id：重置成功。
	if out := kernelContractPost(t, router, admin, "/api/v1/inbound/reset-traffic", `{"id":`+jsonInt64(inboundID)+`}`); out.Code != 0 {
		t.Fatalf("reset-traffic on existing inbound: %+v", out)
	}

	// 不存在 id：必须报错，与同族端点同口径。
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/reset-traffic", `{"id":999999}`)
	if out.Code == 0 {
		t.Fatalf("B10-001: reset-traffic on missing inbound silently succeeded: %+v", out)
	}
	if !strings.Contains(out.Msg, "节点服务不存在") {
		t.Fatalf("B10-001: reset-traffic on missing inbound msg = %q, want 节点服务不存在", out.Msg)
	}

	// 对照：同族 toggle 对不存在 id 同样报错（口径一致性锚点）。
	toggle := kernelContractPost(t, router, admin, "/api/v1/inbound/toggle", `{"id":999999,"enabled":true}`)
	if toggle.Code == 0 || !strings.Contains(toggle.Msg, "节点服务不存在") {
		t.Fatalf("toggle on missing inbound should report 节点服务不存在, got %+v", toggle)
	}
}
