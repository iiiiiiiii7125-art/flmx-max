package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R9X-005（批 4 · handler 面）：保存期 validateChainVersion 与下发
// 期 buildKernelApplyPayload 对「sing-box <1.13 节点 + Naive 链式
// 目标」同点点名拒绝（文案含 1.13 下限）；1.13 放行。

func TestR9B4NaiveDialGate(t *testing.T) {
	naive := &kernel.Endpoint{Inbound: kernel.Inbound{Protocol: kernel.ProtocolNaive}}
	if msg := naiveDialGate(kernel.TypeSingbox, "1.12.17", naive); !strings.Contains(msg, "1.13") {
		t.Fatalf("1.12 node dialing naive must be named-rejected, got %q", msg)
	}
	for _, v := range []string{"1.13.14", "1.14.3", "latest", ""} {
		if msg := naiveDialGate(kernel.TypeSingbox, v, naive); msg != "" {
			t.Fatalf("version %q must pass, got %q", v, msg)
		}
	}
	if msg := naiveDialGate(kernel.TypeXray, "1.12.17", naive); msg != "" {
		t.Fatalf("xray is not judged by the sing-box naive gate, got %q", msg)
	}
	vless := &kernel.Endpoint{Inbound: kernel.Inbound{Protocol: kernel.ProtocolVLESS}}
	if msg := naiveDialGate(kernel.TypeSingbox, "1.12.17", vless); msg != "" {
		t.Fatalf("non-naive target must pass, got %q", msg)
	}
	if msg := naiveDialGate(kernel.TypeSingbox, "1.12.17", nil); msg != "" {
		t.Fatalf("nil target must pass, got %q", msg)
	}
}

func TestR9B4NaiveChainSaveAndDispatchGate(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r9b4.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10")
	frontBox := seedChainKernel(t, r, front, kernel.TypeSingbox)
	landingBox := seedChainKernel(t, r, landing, kernel.TypeSingbox)

	target := seedChainInbound(t, r, landingBox, "landing-naive", kernel.Inbound{Protocol: kernel.ProtocolNaive, Port: 443,
		Settings: kernel.Settings{Username: "nu", Password: "naive-pass-1"},
		Security: kernel.Security{Type: kernel.SecurityTLS, CertMode: kernel.CertModeACME, SNI: "nv.example.com", ACMEEmail: "me@example.com"}}, 0)
	entry := seedChainInbound(t, r, frontBox, "front-vless", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)

	// 保存期：1.12 形节点点名拒绝、1.13 放行。
	if err := h.validateChainVersion(*entry, kernel.TypeSingbox, "1.12.17"); err == nil ||
		!strings.Contains(err.Error(), "1.13") {
		t.Fatalf("save on 1.12 must be named-rejected, got %v", err)
	}
	if err := h.validateChainVersion(*entry, kernel.TypeSingbox, "1.13.14"); err != nil {
		t.Fatalf("save on 1.13 must pass, got %v", err)
	}

	// 下发期：内核版本 1.12.17 整单点名拒绝；升到 1.13.14 放行且
	// 产物含 naive 出站。
	setVersion := func(v string) *model.NodeKernel {
		if err := r.UpdateNodeKernelFields(frontBox.ID, map[string]interface{}{"version": v}); err != nil {
			t.Fatal(err)
		}
		k, err := r.GetNodeKernel(frontBox.ID)
		if err != nil || k == nil {
			t.Fatalf("reload kernel: %v", err)
		}
		return k
	}
	if _, _, err := h.buildKernelApplyPayload(setVersion("1.12.17")); err == nil ||
		!strings.Contains(err.Error(), "1.13") {
		t.Fatalf("dispatch on 1.12 must be named-rejected, got %v", err)
	}
	payload, _, err := h.buildKernelApplyPayload(setVersion("1.13.14"))
	if err != nil {
		t.Fatalf("dispatch on 1.13 must pass, got %v", err)
	}
	cfg := payload["files"].([]kernel.File)[0].Content
	if !strings.Contains(cfg, `"type": "naive"`) {
		t.Fatalf("dispatch on 1.13 must carry the naive outbound:\n%s", cfg)
	}

	// 换版扫描：把 front 内核降到 1.12.17 会断掉经 naive 出站的入站，
	// 必须拒绝；1.13.14 放行。
	if err := h.checkKernelVersionGates(frontBox.ID, kernel.TypeSingbox, "1.12.17"); err == nil ||
		!strings.Contains(err.Error(), "1.13") {
		t.Fatalf("downgrade to 1.12 must be named-rejected, got %v", err)
	}
	if err := h.checkKernelVersionGates(frontBox.ID, kernel.TypeSingbox, "1.13.14"); err != nil {
		t.Fatalf("install 1.13 must pass, got %v", err)
	}
	_ = time.Now
}
