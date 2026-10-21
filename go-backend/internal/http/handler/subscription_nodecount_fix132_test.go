package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// A5-02/BC-11：列表的 NodeCount 按请求的目标格式实际渲染数计。
// 夹具 5 入站（VLESS Reality、Snell v5、Snell v6、SS2022、Trojan）：
// 旧口径恒为 5（渲染前计数）；新口径 clash 4 / sing-box 5 / surge 4 /
// base64 3（缺省与非法 format 均回落 base64）。
func TestSubscriptionListNodeCountByFormat(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_nodecount.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	nodeID := seedRelayTargetNode(t, r, "nc-node", "203.0.113.10")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	uuid := "11111111-2222-3333-4444-555555555555"
	seedChainInbound(t, r, box, "nc-reality", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Settings: kernel.Settings{UUID: uuid},
		Security: kernel.Security{Type: kernel.SecurityReality, SNI: "www.microsoft.com", RealityPublicKey: "pk", RealityShortIDs: []string{"ab"}}}, 0)
	seedChainInbound(t, r, box, "nc-snell5", kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 6160,
		Settings: kernel.Settings{Password: "psk-cccccccc", SnellVersion: 5}}, 0)
	seedChainInbound(t, r, box, "nc-snell6", kernel.Inbound{Protocol: kernel.ProtocolSnell, Port: 6161,
		Settings: kernel.Settings{Password: "psk-dddddddd", SnellVersion: 6}}, 0)
	seedChainInbound(t, r, box, "nc-ss", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388,
		Settings: kernel.Settings{Method: "2022-blake3-aes-256-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}, 0)
	seedChainInbound(t, r, box, "nc-trojan", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8443,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com"}}, 0)

	sub := &model.Subscription{Name: "nc-mix", Kind: model.SubscriptionShare, Token: strings.Repeat("c", 32),
		UUID: "u-nc", Password: "p", Enabled: 1, SourceAll: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}

	countFor := func(body string) int {
		req := httptest.NewRequest("POST", "/api/v1/subscription/list", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.subscriptionList(rec, req)
		var env struct {
			Code int `json:"code"`
			Data struct {
				Items []subscriptionView `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode list (%s): %v (body=%s)", body, err, rec.Body.String())
		}
		for _, it := range env.Data.Items {
			if it.ID == sub.ID {
				return it.NodeCount
			}
		}
		t.Fatalf("subscription %d not in list (%s): %s", sub.ID, body, rec.Body.String())
		return -1
	}

	cases := map[string]int{
		`{}`:                    3, // 缺省 = 无 UA 默认格式 base64
		`{"format":"base64"}`:   3,
		`{"format":"clash"}`:    4, // snell v6 不下发
		`{"format":"singbox"}`:  5, // 现行 sing-box 客户端全收（含 snell v6）
		`{"format":"surge"}`:    4, // vless 不下发
		`{"format":"nonsense"}`: 3, // 非法值回落 base64，不整单失败
	}
	for body, want := range cases {
		if got := countFor(body); got != want {
			t.Fatalf("list %s: nodeCount = %d, want %d", body, got, want)
		}
	}
}
