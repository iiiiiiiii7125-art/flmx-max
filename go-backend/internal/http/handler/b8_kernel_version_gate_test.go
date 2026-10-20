package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// F-021 (audit FINAL §5-B8 / A5-001): AnyTLS/ACME 版本门此前只在入站保存时
// 检查，内核换版与下发前复检均未覆盖。以下用例修复前全部失败。

func seedB8Kernel(t *testing.T, r *repo.Repository, nodeID int64, kernelType, version string) *model.NodeKernel {
	t.Helper()
	k := &model.NodeKernel{NodeID: nodeID, KernelType: kernelType, Version: version, Status: "pending", Enabled: 1,
		CreatedTime: time.Now().UnixMilli()}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func seedB8AnyTLS(t *testing.T, r *repo.Repository, k *model.NodeKernel) *model.NodeInbound {
	t.Helper()
	return seedChainInbound(t, r, k, "anytls-in", kernel.Inbound{Protocol: kernel.ProtocolAnyTLS, Port: 443,
		Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "www.bing.com", CertMode: kernel.CertModeSelf},
		Settings: kernel.Settings{Password: "anytls-secret"}}, 0)
}

func postKernelInstall(t *testing.T, h *Handler, nodeID int64, kernelType, version string) (int, string) {
	t.Helper()
	body := `{"nodeId":` + strconv.FormatInt(nodeID, 10) + `,"kernelType":"` + kernelType + `","version":"` + version + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/kernel/install", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.kernelInstall(rec, req)
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode install response: %v (body=%s)", err, rec.Body.String())
	}
	return env.Code, env.Msg
}

func TestKernelInstallRejectsAnyTLSDowngradeF021(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "gate-anytls", "198.51.100.7")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14")
	seedB8AnyTLS(t, r, k)

	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.11")
	if code == 0 {
		t.Fatal("installing sing-box 1.11 under an AnyTLS inbound must be refused")
	}
	if !strings.Contains(msg, "AnyTLS 需要 sing-box 1.12 及以上") {
		t.Fatalf("refusal must name the AnyTLS version gate, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14" {
		t.Fatalf("refused install must not rewrite the stored version, got %q", after.Version)
	}
}

func TestKernelInstallRejectsACMEDowngradeF021(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "gate-acme", "198.51.100.8")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14")
	seedChainInbound(t, r, k, "acme-in", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 8443,
		Settings: kernel.Settings{Password: "trojan-secret"},
		Security: kernel.Security{Type: kernel.SecurityTLS, CertMode: kernel.CertModeACME, SNI: "cf.example.com",
			ACMEDNS: kernel.ACMEDNSCloudflare, ACMEDNSKey: "cf-token"}}, 0)

	code, msg := postKernelInstall(t, h, nodeID, kernel.TypeSingbox, "1.13")
	if code == 0 {
		t.Fatal("installing sing-box 1.13 under an ACME inbound must be refused")
	}
	if !strings.Contains(msg, "自动申请证书需要 sing-box 1.14 及以上") {
		t.Fatalf("refusal must name the ACME version gate, got %q", msg)
	}
	after, err := r.GetNodeKernel(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "1.14" {
		t.Fatalf("refused install must not rewrite the stored version, got %q", after.Version)
	}
}

func TestBuildKernelApplyPayloadRechecksVersionGatesF021(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/gate.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "gate-payload", "198.51.100.9")
	// 存量脏数据：内核版本已低于入站要求（绕过保存期校验落库）。
	k := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.11")
	seedB8AnyTLS(t, r, k)

	if _, _, err := h.buildKernelApplyPayload(k); err == nil {
		t.Fatal("apply payload must refuse AnyTLS on sing-box 1.11")
	} else if !strings.Contains(err.Error(), "anytls-in") || !strings.Contains(err.Error(), "AnyTLS 需要 sing-box 1.12 及以上") {
		t.Fatalf("refusal must name the inbound and the gate, got %q", err.Error())
	}

	// 达标版本与 latest 语义不拦（正例防误伤）。
	k.Version = "1.14"
	if _, _, err := h.buildKernelApplyPayload(k); err != nil {
		t.Fatalf("sing-box 1.14 must accept the AnyTLS inbound: %v", err)
	}
	k.Version = "latest"
	if _, _, err := h.buildKernelApplyPayload(k); err != nil {
		t.Fatalf("\"latest\" must keep the existing not-below semantics: %v", err)
	}
}
