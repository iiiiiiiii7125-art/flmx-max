package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R3 批 8 · R3X-014（D-03）：批量删节点的回传清单在聚合层按显示串
// 去重，同名同节点的两个不同引用方被少计。单删口径
// （nodeCrossReferrers 按引用方 ID 去重）本就正确，聚合层须按 ID
// 合并；单删响应契约 []string 不变（由 R2B9 既有用例守）。

type r3b8BatchDeleteEnvelope struct {
	Code int `json:"code"`
	Data struct {
		SuccessCount    int      `json:"successCount"`
		FailCount       int      `json:"failCount"`
		BrokenReferrers []string `json:"brokenReferrers"`
	} `json:"data"`
}

func r3b8BatchDelete(t *testing.T, h *Handler, ids []int64) r3b8BatchDeleteEnvelope {
	t.Helper()
	res := httptest.NewRecorder()
	h.nodeBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": ids}))
	var env r3b8BatchDeleteEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode batch delete response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("batch delete must succeed, body=%s", res.Body.String())
	}
	return env
}

// 探针同 D 域构造：同一节点（front）上的两个不同入站同名
// 「same-name」、同指被删节点的一个入站。两条显示串完全相同，
// 但确为两个引用方，批量回传必须为 2 条。
func TestR3B8NodeBatchDeleteCountsSameNameReferrersByID(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b8-batch-samename.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	frontXray := seedChainKernel(t, r, front, kernel.TypeXray)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)

	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	_ = seedChainInbound(t, r, frontXray, "same-name", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
	_ = seedChainInbound(t, r, frontXray, "same-name", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	// 单删口径基线：nodeCrossReferrers 本身按 ID 回传 2 条。
	if got := h.nodeCrossReferrers(landing); len(got) != 2 {
		t.Fatalf("single-delete source list must carry 2 referrers, got %v", got)
	}

	env := r3b8BatchDelete(t, h, []int64{landing})
	if env.Data.SuccessCount != 1 {
		t.Fatalf("batch delete success count = %d, want 1", env.Data.SuccessCount)
	}
	if len(env.Data.BrokenReferrers) != 2 {
		t.Fatalf("R3X-014: two distinct same-name referrers must both be reported, got %v", env.Data.BrokenReferrers)
	}
	for _, entry := range env.Data.BrokenReferrers {
		if entry != "same-name（front）" {
			t.Fatalf("R3X-014: referrer entry must keep the display form, got %q", entry)
		}
	}
}

// 防过修：同一引用方同时引用两个被删节点的入站（出站 + 分流双路
// 径），批量回传仍只报 1 条——按 ID 合并不得退化为逐节点重复播报。
func TestR3B8NodeBatchDeleteCountsCrossNodeReferrerOnce(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b8-batch-crossnode.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landingA := seedRelayTargetNode(t, r, "landing-a", "203.0.113.10")
	landingB := seedRelayTargetNode(t, r, "landing-b", "203.0.113.11")
	frontXray := seedChainKernel(t, r, front, kernel.TypeXray)
	boxA := seedChainKernel(t, r, landingA, kernel.TypeSingbox)
	boxB := seedChainKernel(t, r, landingB, kernel.TypeSingbox)

	targetA := seedChainInbound(t, r, boxA, "landing-a-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	targetB := seedChainInbound(t, r, boxB, "landing-b-trojan", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 444,
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com"}}, 0)
	ref := seedChainInbound(t, r, frontXray, "dual-ref", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, targetA.ID)
	ref.SplitRules = `[{"categories":["netflix"],"action":"proxy","outboundId":` + jsonID(targetB.ID) + `}]`
	if err := r.SaveNodeInbound(ref); err != nil {
		t.Fatal(err)
	}

	env := r3b8BatchDelete(t, h, []int64{landingA, landingB})
	if env.Data.SuccessCount != 2 {
		t.Fatalf("batch delete success count = %d, want 2", env.Data.SuccessCount)
	}
	if len(env.Data.BrokenReferrers) != 1 || env.Data.BrokenReferrers[0] != "dual-ref（front）" {
		t.Fatalf("R3X-014: one referrer spanning two deleted nodes must be reported once, got %v", env.Data.BrokenReferrers)
	}
}
