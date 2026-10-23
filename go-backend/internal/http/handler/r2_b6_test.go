package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2 批 6：R2X-007（ENC 拨号侧版本门三面）+ R2X-030（入站更新 presence 扩围）。

const b6ENCString = "mlkem768x25519plus.native.0rtt.100-111-1111.75-0-111.50-0-3333.testpub"

func seedB6ImportedENC(t *testing.T, r *repo.Repository, name, host string) *model.NodeInbound {
	t.Helper()
	raw, err := json.Marshal(kernel.Proxy{
		"type": "vless", "server": host, "port": 443,
		"uuid":       "11111111-2222-3333-4444-555555555555",
		"encryption": b6ENCString, "network": "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	row := model.NodeInbound{Name: name, Protocol: kernel.ProtocolVLESS, Listen: host, Port: 443,
		ShareHost: host, Extra: string(raw), Enabled: 1,
		CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	return &row
}

func seedB6ENCInbound(t *testing.T, r *repo.Repository, k *model.NodeKernel, name string, port int) *model.NodeInbound {
	t.Helper()
	return seedChainInbound(t, r, k, name, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
		Security: kernel.Security{Type: kernel.SecurityNone},
		Settings: kernel.Settings{EncryptionAuth: kernel.EncryptionAuthX25519}}, 0)
}

func b6SaveUpdate(t *testing.T, h *Handler, row *model.NodeInbound, mutate func(req *inboundRequest)) response.R {
	t.Helper()
	old, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	req := inboundRequest{
		ID: row.ID, Name: row.Name, Protocol: old.Protocol, Listen: old.Listen, Port: old.Port,
		Settings: old.Settings, Transport: old.Transport, Security: old.Security,
		OutboundID: row.OutboundID,
	}
	if len(rowSplitRules(*row)) > 0 {
		req.SplitRules = rowSplitRules(*row)
	}
	if mutate != nil {
		mutate(&req)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return callInboundSave(t, h, true, string(raw))
}

func b6ReloadInbound(t *testing.T, r *repo.Repository, id int64) kernel.Inbound {
	t.Helper()
	row, err := r.GetNodeInbound(id)
	if err != nil || row == nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	in, err := inboundFromModel(*row)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// ① 保存面：Xray 25.5.0 指向 ENC 目标（自建/导入/分流）被拒且 msg 含
// 25.9.5；25.9.5 成功；非 ENC 目标在 25.5.0 成功。
func TestR2B6SaveRejectsENCDialOnOldXray(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b6-save.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "b6-front", "198.51.100.31")
	landing := seedRelayTargetNode(t, r, "b6-landing", "203.0.113.31")
	frontXray := seedB8Kernel(t, r, front, kernel.TypeXray, "25.5.0")
	landingXray := seedB8Kernel(t, r, landing, kernel.TypeXray, "25.9.5")

	encTarget := seedB6ENCInbound(t, r, landingXray, "b6-enc-target", 443)
	plainTarget := seedChainInbound(t, r, landingXray, "b6-plain-target", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	importedENC := seedB6ImportedENC(t, r, "b6-imported-enc", "198.51.100.99")
	entry := seedChainInbound(t, r, frontXray, "b6-entry", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)

	if out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) { req.OutboundID = encTarget.ID }); out.Code == 0 || !strings.Contains(out.Msg, "25.9.5") {
		t.Fatalf("Xray 25.5.0 -> ENC self-built target must be refused with 25.9.5 in msg, got %+v", out)
	}
	if out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) { req.OutboundID = importedENC.ID }); out.Code == 0 || !strings.Contains(out.Msg, "25.9.5") {
		t.Fatalf("Xray 25.5.0 -> ENC imported target must be refused with 25.9.5 in msg, got %+v", out)
	}
	if out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
		req.OutboundID = 0
		req.SplitRules = []kernel.SplitRule{{Categories: []string{"netflix"}, Action: kernel.SplitProxy, OutboundID: encTarget.ID}}
	}); out.Code == 0 || !strings.Contains(out.Msg, "25.9.5") {
		t.Fatalf("Xray 25.5.0 split -> ENC target must be refused with 25.9.5 in msg, got %+v", out)
	}
	// 非 ENC 目标在 25.5.0 成功（防过修）。
	if out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
		req.OutboundID = plainTarget.ID
		req.SplitRules = nil
	}); out.Code != 0 {
		t.Fatalf("Xray 25.5.0 -> non-ENC target must succeed, got %+v", out)
	}
	// 升到 25.9.5 后 ENC 目标成功。
	if err := r.UpdateNodeKernelFields(frontXray.ID, map[string]interface{}{"version": "25.9.5"}); err != nil {
		t.Fatal(err)
	}
	if out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) { req.OutboundID = encTarget.ID }); out.Code != 0 {
		t.Fatalf("Xray 25.9.5 -> ENC target must succeed, got %+v", out)
	}
}

// ② 下发面：存量脏组合（保存期未拦住）在 buildKernelApplyPayload 整单拒绝。
func TestR2B6ApplyPayloadRejectsENCDialOnOldXray(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b6-apply.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "b6-front", "198.51.100.32")
	landing := seedRelayTargetNode(t, r, "b6-landing", "203.0.113.32")
	frontXray := seedB8Kernel(t, r, front, kernel.TypeXray, "25.5.0")
	landingXray := seedB8Kernel(t, r, landing, kernel.TypeXray, "25.9.5")
	encTarget := seedB6ENCInbound(t, r, landingXray, "b6-enc-target", 443)
	importedENC := seedB6ImportedENC(t, r, "b6-imported-enc", "198.51.100.98")

	// Via 面：直接落库绕过保存期校验。
	seedChainInbound(t, r, frontXray, "b6-entry-via", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, encTarget.ID)
	if _, _, err := h.buildKernelApplyPayload(frontXray); err == nil || !strings.Contains(err.Error(), "25.9.5") {
		t.Fatalf("apply payload must refuse ENC via on Xray 25.5.0, got %v", err)
	}

	// Split 面：另一内核上入站经分流指向导入 ENC 节点。
	front2 := seedRelayTargetNode(t, r, "b6-front2", "198.51.100.33")
	front2Xray := seedB8Kernel(t, r, front2, kernel.TypeXray, "25.5.0")
	splitEntry := seedChainInbound(t, r, front2Xray, "b6-entry-split", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	splitEntry.SplitRules = `[{"categories":["netflix"],"action":"proxy","outboundId":` + jsonID(importedENC.ID) + `}]`
	if err := r.SaveNodeInbound(splitEntry); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.buildKernelApplyPayload(front2Xray); err == nil || !strings.Contains(err.Error(), "25.9.5") {
		t.Fatalf("apply payload must refuse ENC split target on Xray 25.5.0, got %v", err)
	}

	// 达标版本下发成功（防过修）。
	frontXray.Version = "25.9.5"
	if _, _, err := h.buildKernelApplyPayload(frontXray); err != nil {
		t.Fatalf("apply payload on Xray 25.9.5 must accept ENC via: %v", err)
	}
}

// ③ 换版面：已有 ENC 出站的内核降级装 25.5.0 被拒，存量版本不变。
func TestR2B6KernelInstallRejectsENCDialDowngrade(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b6-install.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "b6-front", "198.51.100.34")
	landing := seedRelayTargetNode(t, r, "b6-landing", "203.0.113.34")
	frontXray := seedB8Kernel(t, r, front, kernel.TypeXray, "25.9.5")
	landingXray := seedB8Kernel(t, r, landing, kernel.TypeXray, "25.9.5")
	encTarget := seedB6ENCInbound(t, r, landingXray, "b6-enc-target", 443)
	seedChainInbound(t, r, frontXray, "b6-entry", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, encTarget.ID)

	code, msg := postKernelInstall(t, h, front, kernel.TypeXray, "25.5.0")
	if code == 0 || !strings.Contains(msg, "25.9.5") {
		t.Fatalf("downgrading a kernel with an ENC outbound to 25.5.0 must be refused with 25.9.5 in msg, got code=%d msg=%q", code, msg)
	}
	after, err := r.GetNodeKernel(frontXray.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != "25.9.5" {
		t.Fatalf("refused install must not rewrite the stored version, got %q", after.Version)
	}
	// 直接调扫描函数同口径（导入 ENC 目标）。
	importedENC := seedB6ImportedENC(t, r, "b6-imported-enc", "198.51.100.97")
	seedChainInbound(t, r, frontXray, "b6-entry-imported", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24444,
		Security: kernel.Security{Type: kernel.SecurityNone}}, importedENC.ID)
	if err := h.checkKernelVersionGates(frontXray.ID, kernel.TypeXray, "25.5.0"); err == nil || !strings.Contains(err.Error(), "25.9.5") {
		t.Fatalf("gate scan must refuse imported ENC target on 25.5.0, got %v", err)
	}
	if err := h.checkKernelVersionGates(frontXray.ID, kernel.TypeXray, "25.9.5"); err != nil {
		t.Fatalf("gate scan must accept ENC targets on 25.9.5: %v", err)
	}
}
