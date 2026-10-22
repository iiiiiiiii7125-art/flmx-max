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

// X-P3-2（B-F2）：计数侧 nodeCountFormat 此前不识别 stash、回落
// base64——同一订阅在 list 里的 NodeCount 与 Stash 实际拉到的节点数
// 不一致。1.4.0（DA-7）起 stash 是一等格式 FormatStash（渲染面与
// Clash 同门控、仅规则集序列化不同），本断言的口径随之更新为：
// stash 计数必须与 stash 实得（= Clash 家族携带面）恒等。
// 夹具含 NaïveProxy 入站：Clash 无 Naïve 客户端（计数 0）、base64 有
// 分享链接（计数 1），两口径必然分叉，可钉住别名是否生效。
func TestR1B7SubscriptionListNodeCountStashMatchesClash(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/sub_stash_count.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()

	nodeID := seedRelayTargetNode(t, r, "stash-node", "203.0.113.10")
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	seedChainInbound(t, r, box, "stash-naive", kernel.Inbound{Protocol: kernel.ProtocolNaive, Port: 443,
		Settings: kernel.Settings{Username: "nu", Password: "naive-pass-1"},
		Security: kernel.Security{Type: kernel.SecurityTLS, CertMode: kernel.CertModeACME, SNI: "nv.example.com", ACMEEmail: "me@example.com"}}, 0)
	seedChainInbound(t, r, box, "stash-trojan", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8443,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com"}}, 0)

	sub := &model.Subscription{Name: "stash-mix", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-stash", Password: "p", Enabled: 1, SourceAll: 1, CreatedTime: now, UpdatedTime: now}
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

	clash := countFor(`{"format":"clash"}`)
	base64n := countFor(`{"format":"base64"}`)
	if clash == base64n {
		t.Fatalf("fixture must distinguish clash (%d) from base64 (%d)", clash, base64n)
	}
	if got := countFor(`{"format":"stash"}`); got != clash {
		t.Fatalf("format=stash NodeCount = %d, want clash count %d (base64 count %d)", got, clash, base64n)
	}
	// 别名与拉取侧同口径大小写不敏感。
	if got := countFor(`{"format":"Stash"}`); got != clash {
		t.Fatalf("format=Stash NodeCount = %d, want clash count %d", got, clash)
	}
}
