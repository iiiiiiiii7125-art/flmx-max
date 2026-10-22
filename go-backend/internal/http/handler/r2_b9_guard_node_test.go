package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R2 批 9 · R2X-029（Force 清单按引用方 ID 去重 + 「名称（节点名）」消歧）
// 与 R2X-045（删节点回传跨节点引用清单）回归。

func TestR2B9KernelChainGuardCountsSameNameReferrersByID(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-guard.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	nodeA := seedRelayTargetNode(t, r, "node-a", "198.51.100.11")
	nodeB := seedRelayTargetNode(t, r, "node-b", "198.51.100.12")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	kernelA := seedChainKernel(t, r, nodeA, kernel.TypeXray)
	kernelB := seedChainKernel(t, r, nodeB, kernel.TypeXray)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)

	target := seedChainInbound(t, r, landingBox, "landing-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	// 两节点同名入站同指一落地。
	_ = seedChainInbound(t, r, kernelA, "same-name", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
	_ = seedChainInbound(t, r, kernelB, "same-name", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	// R3X-015 连带签名适配（断言不变）：kernelChainGuard /
	// chainInUseMessage 现回传守卫查询错误，健康库上必须为 nil。
	names, msg, gerr := h.kernelChainGuard(landingBox.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(names) != 2 {
		t.Fatalf("R2X-029: same-name referrers on two nodes must count 2, got %v (msg=%q)", names, msg)
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "same-name（node-a）") || !strings.Contains(joined, "same-name（node-b）") {
		t.Fatalf("R2X-029: referrer entries must be disambiguated by node name, got %v", names)
	}
	if names[0] == names[1] {
		t.Fatalf("R2X-029: referrer entries must be distinguishable, got %v", names)
	}
	if !strings.Contains(msg, "same-name（node-a）") || !strings.Contains(msg, "same-name（node-b）") {
		t.Fatalf("R2X-029: guard message must name referrers with node names, got %q", msg)
	}
}

// 防过修：同一引用方经两条路径（出站 + 分流）引用同一内核的两个入站，
// 只计 1。
func TestR2B9KernelChainGuardCountsDualPathReferrerOnce(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-guard-dual.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	frontXray := seedChainKernel(t, r, front, kernel.TypeXray)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)

	target1 := seedChainInbound(t, r, landingBox, "landing-1", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	target2 := seedChainInbound(t, r, landingBox, "landing-2", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 444,
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "t.example.com"}}, 0)
	ref := seedChainInbound(t, r, frontXray, "dual-ref", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target1.ID)
	ref.SplitRules = `[{"categories":["netflix"],"action":"proxy","outboundId":` + jsonID(target2.ID) + `}]`
	if err := r.SaveNodeInbound(ref); err != nil {
		t.Fatal(err)
	}

	names, _, gerr := h.kernelChainGuard(landingBox.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(names) != 1 || names[0] != "dual-ref（front）" {
		t.Fatalf("R2X-029: a dual-path referrer must count once, got %v", names)
	}
	if msg, gerr := h.chainInUseMessage(target1); gerr != nil || !strings.Contains(msg, "dual-ref（front）") {
		t.Fatalf("R2X-029: chainInUseMessage must disambiguate with node name, got %q %v", msg, gerr)
	}
}

// --- R2X-045 ---

type nodeDeleteEnvelope struct {
	Code int `json:"code"`
	Data struct {
		BrokenReferrers     []string `json:"brokenReferrers"`
		BrokenReferrerCount int      `json:"brokenReferrerCount"`
	} `json:"data"`
}

func TestR2B9NodeDeleteReturnsBrokenReferrers(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-node-del.db")
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
	_ = seedChainInbound(t, r, frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
	// 同节点自引用不计。
	_ = seedChainInbound(t, r, landingBox, "landing-self", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8445,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	res := httptest.NewRecorder()
	h.nodeDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": landing}))
	var env nodeDeleteEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode node delete response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("node delete must succeed (semantics unchanged), body=%s", res.Body.String())
	}
	if env.Data.BrokenReferrerCount != 1 || len(env.Data.BrokenReferrers) != 1 {
		t.Fatalf("R2X-045: delete must return the cross-node referrer list, got %+v", env.Data)
	}
	if env.Data.BrokenReferrers[0] != "front-vless（front）" {
		t.Fatalf("R2X-045: referrer entry must be disambiguated by node name, got %v", env.Data.BrokenReferrers)
	}
	if node, _ := h.repo.GetNodeByID(landing); node != nil {
		t.Fatalf("node must be deleted")
	}
	if row, _ := h.repo.GetNodeInbound(target.ID); row != nil {
		t.Fatalf("target inbound must be deleted with the node")
	}
}

func TestR2B9NodeDeleteWithoutReferrersReturnsEmptyList(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-node-del-empty.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	solo := seedRelayTargetNode(t, r, "solo", "198.51.100.5")
	box := seedChainKernel(t, r, solo, kernel.TypeSingbox)
	_ = seedChainInbound(t, r, box, "solo-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)

	res := httptest.NewRecorder()
	h.nodeDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": solo}))
	var env nodeDeleteEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode node delete response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("node delete must succeed, body=%s", res.Body.String())
	}
	if len(env.Data.BrokenReferrers) != 0 || env.Data.BrokenReferrerCount != 0 {
		t.Fatalf("R2X-045: delete without referrers must return an empty list, got %+v", env.Data)
	}
}

func TestR2B9NodeBatchDeleteReturnsBrokenReferrers(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r2b9-node-batch-del.db")
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
	_ = seedChainInbound(t, r, frontXray, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	res := httptest.NewRecorder()
	h.nodeBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"ids": []int64{landing}}))
	var env struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount    int      `json:"successCount"`
			BrokenReferrers []string `json:"brokenReferrers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode batch delete response: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 || env.Data.SuccessCount != 1 {
		t.Fatalf("batch delete must succeed, body=%s", res.Body.String())
	}
	if len(env.Data.BrokenReferrers) != 1 || env.Data.BrokenReferrers[0] != "front-vless（front）" {
		t.Fatalf("R2X-045: batch delete must return the cross-node referrer list, got %+v", env.Data)
	}
}
