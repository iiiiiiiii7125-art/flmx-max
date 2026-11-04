package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R4-C2C-02: 下发复检补 Settings 级条件字段。存量毒行（绕过保存期
// Normalize 直写落库）经 buildKernelApplyPayload 必须整单拒绝并点名，
// 不得再被渲染面静默降级（ENC 明文渲染）、静默忽略（ConnIdle）或带病
// 下发（Vision 爆单）。seedPoisonInbound 刻意不跑 Normalize，复刻守卫
// 前存量行。

func seedPoisonInbound(t *testing.T, r *repo.Repository, k *model.NodeKernel, name string, in kernel.Inbound) *model.NodeInbound {
	t.Helper()
	row := model.NodeInbound{KernelID: k.ID, NodeID: k.NodeID, Name: name, Enabled: 1,
		CreatedTime: time.Now().UnixMilli()}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	return &row
}

func TestBuildKernelApplyPayloadRechecksSettingsConditions(t *testing.T) {
	cases := []struct {
		name       string
		kernelType string
		version    string
		inbound    kernel.Inbound
		wantErr    string // 空 = 应通过
	}{
		// ENC 格：sing-box + latest 此前靠版本数值巧合/豁免放行，渲染成
		// 无加密普通 VLESS。内核条件与版本无关地拒绝。
		{"enc poison on sing-box latest", kernel.TypeSingbox, "latest",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport: kernel.Transport{Type: kernel.TransportTCP},
				Security:  kernel.Security{Type: kernel.SecurityNone},
				Settings:  kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666", EncryptionAuth: kernel.EncryptionAuthMLKEM}},
			"VLESS 加密仅支持 Xray 内核的 VLESS 入站"},
		{"enc poison on sing-box pinned", kernel.TypeSingbox, "1.14.0",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport: kernel.Transport{Type: kernel.TransportTCP},
				Security:  kernel.Security{Type: kernel.SecurityNone},
				Settings:  kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666", EncryptionAuth: kernel.EncryptionAuthMLKEM}},
			"VLESS 加密仅支持 Xray 内核的 VLESS 入站"},
		// ConnIdle 格：下发此前无任何门，渲染面静默忽略。
		{"connidle poison on sing-box", kernel.TypeSingbox, "1.14.0",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport:       kernel.Transport{Type: kernel.TransportTCP},
				Security:        kernel.Security{Type: kernel.SecurityNone},
				Settings:        kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666"},
				ConnIdleSeconds: 300},
			"空闲超时（connIdle）仅支持 Xray 内核"},
		// Vision 格：两门均盲，逐字带 flow 下发后在节点侧整单爆。
		{"vision poison on xray ws", kernel.TypeXray, "latest",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport: kernel.Transport{Type: kernel.TransportWS},
				Security:  kernel.Security{Type: kernel.SecurityTLS, SNI: "www.bing.com", CertMode: kernel.CertModeSelf},
				Settings:  kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666", Flow: kernel.FlowVision}},
			"Vision 流控"},
		// 正例：同形合法行不得误伤。
		{"vision on xray tcp tls passes", kernel.TypeXray, "latest",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport: kernel.Transport{Type: kernel.TransportTCP},
				Security:  kernel.Security{Type: kernel.SecurityTLS, SNI: "www.bing.com", CertMode: kernel.CertModeSelf},
				Settings:  kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666", Flow: kernel.FlowVision}},
			""},
		{"plain sing-box vless passes", kernel.TypeSingbox, "1.14.0",
			kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 443,
				Transport: kernel.Transport{Type: kernel.TransportTCP},
				Security:  kernel.Security{Type: kernel.SecurityNone},
				Settings:  kernel.Settings{UUID: "3f8a2b1c-1111-4222-8333-444455556666"}},
			""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := repo.Open(t.TempDir() + "/fxb.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			h := New(r, "unit-test-secret")
			nodeID := seedRelayTargetNode(t, r, "fxb-"+c.name, "198.51.100.21")
			k := seedB8Kernel(t, r, nodeID, c.kernelType, c.version)
			seedPoisonInbound(t, r, k, "poison-in", c.inbound)

			_, _, err = h.buildKernelApplyPayload(k)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("apply payload must accept the clean inbound: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("apply payload must refuse the poisoned inbound (%s)", c.wantErr)
			}
			if !strings.Contains(err.Error(), "poison-in") || !strings.Contains(err.Error(), c.wantErr) ||
				!strings.Contains(err.Error(), "已拒绝下发") {
				t.Fatalf("refusal must name the inbound, the condition and the refusal, got %q", err.Error())
			}
		})
	}
}
