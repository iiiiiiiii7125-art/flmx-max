package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// R1 批次 3 · X-P2-2（D-R1-02，BC-06，按 V-03）：卸载内核前对该内核全部
// 入站逐个跑 chainReferrers——存在跨内核引用时默认拒绝并列明引用方；
// Force 放行但响应回传被断引用清单。

type kernelRemoveEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		BrokenReferrers     []string `json:"brokenReferrers"`
		BrokenReferrerCount int      `json:"brokenReferrerCount"`
	} `json:"data"`
}

func callKernelRemove(t *testing.T, h *Handler, kernelID int64, force bool) kernelRemoveEnvelope {
	t.Helper()
	body := map[string]any{"id": kernelID, "action": "remove"}
	if force {
		body["force"] = true
	}
	res := httptest.NewRecorder()
	h.kernelControl(res, newAuthenticatedJSONRequest(t, body))
	var env kernelRemoveEnvelope
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode kernel remove response: %v body=%s", err, res.Body.String())
	}
	return env
}

func TestR1B3KernelRemoveBlockedByChainReferrer(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r1b3-kernel-remove.db")
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

	// 非 Force 卸载：应被拒且错误列明引用方「front-vless」，内核保留。
	env := callKernelRemove(t, h, landingBox.ID, false)
	if env.Code == 0 {
		t.Fatalf("remove must be rejected while front-vless chains through it, got success")
	}
	if !strings.Contains(env.Msg, "front-vless") {
		t.Fatalf("rejection must name the referrer inbound, got msg=%q", env.Msg)
	}
	if k, _ := h.repo.GetNodeKernel(landingBox.ID); k == nil {
		t.Fatalf("kernel row deleted despite guard rejection")
	}
	if row, _ := h.repo.GetNodeInbound(target.ID); row == nil {
		t.Fatalf("target inbound deleted despite guard rejection")
	}

	// Force 卸载：放行，且响应 data 回传被断引用清单。
	env = callKernelRemove(t, h, landingBox.ID, true)
	if env.Code != 0 {
		t.Fatalf("force remove must succeed, got code=%d msg=%q", env.Code, env.Msg)
	}
	found := false
	for _, name := range env.Data.BrokenReferrers {
		if name == "front-vless" {
			found = true
		}
	}
	if !found || env.Data.BrokenReferrerCount != 1 {
		t.Fatalf("force remove must return broken referrer list, got %+v", env.Data)
	}
	if k, _ := h.repo.GetNodeKernel(landingBox.ID); k != nil {
		t.Fatalf("kernel row remains after force remove")
	}
}
