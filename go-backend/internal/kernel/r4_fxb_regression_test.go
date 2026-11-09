package kernel

import (
	"strings"
	"testing"
)

// R4-C2C-02: CheckSettingsConditions 是 Normalize 条件谓词的只读形态，
// 供下发复检调用。三格存量毒行（ENC 明文渲染、ConnIdle 静默、Vision
// 爆单）逐格钉死，并附正例防误伤。
func TestCheckSettingsConditionsPoisonCells(t *testing.T) {
	cases := []struct {
		name       string
		kernelType string
		in         Inbound
		wantErr    string // 空 = 应通过
	}{
		{"enc on sing-box", TypeSingbox, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			Settings: Settings{EncryptionAuth: EncryptionAuthMLKEM}}, "VLESS 加密仅支持 Xray 内核的 VLESS 入站"},
		{"enc on sing-box kernel x25519", TypeSingbox, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			Settings: Settings{EncryptionAuth: EncryptionAuthX25519}}, "VLESS 加密仅支持 Xray 内核的 VLESS 入站"},
		{"enc on xray vless passes", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			Settings: Settings{EncryptionAuth: EncryptionAuthMLKEM}}, ""},
		{"connidle on sing-box", TypeSingbox, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			ConnIdleSeconds: 300}, "空闲超时（connIdle）仅支持 Xray 内核"},
		{"connidle unlimited on sing-box", TypeSingbox, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Security: Security{Type: SecurityNone}, ConnIdleSeconds: -1}, "空闲超时（connIdle）仅支持 Xray 内核"},
		{"connidle on xray passes", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			ConnIdleSeconds: 300}, ""},
		{"vision on ws", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportWS}, Security: Security{Type: SecurityTLS},
			Settings: Settings{Flow: FlowVision}}, "Vision 流控"},
		{"vision on tcp none without enc", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			Settings: Settings{Flow: FlowVision}}, "Vision 流控"},
		{"vision on tcp tls passes", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityTLS},
			Settings: Settings{Flow: FlowVision}}, ""},
		{"vision on tcp none with enc passes", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityNone},
			Settings: Settings{Flow: FlowVision, EncryptionAuth: EncryptionAuthMLKEM}}, ""},
		{"vmess with flow", TypeXray, Inbound{Protocol: ProtocolVMess, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityTLS},
			Settings: Settings{Flow: FlowVision}}, "VMess 不支持 flow"},
		{"bogus flow value", TypeXray, Inbound{Protocol: ProtocolVLESS, Port: 443,
			Transport: Transport{Type: TransportTCP}, Security: Security{Type: SecurityTLS},
			Settings: Settings{Flow: "banana"}}, "不支持的 VLESS flow"},
		{"trojan flow is not a condition cell", TypeSingbox, Inbound{Protocol: ProtocolTrojan, Port: 443,
			Security: Security{Type: SecurityTLS}, Settings: Settings{Flow: FlowVision}}, ""},
	}
	for _, c := range cases {
		err := CheckSettingsConditions(c.kernelType, &c.in)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: must pass, got %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.wantErr, err)
		}
	}
}

// R4-F2-01（后端半句）: 非 Xray 空闲超时文案为中性表述，不再以
// sing-box 机理为主语套到其他内核；Normalize 与复检同文（同源）。
func TestConnIdleRejectionCopyIsKernelNeutral(t *testing.T) {
	in := Inbound{Protocol: ProtocolVLESS, Port: 443, Transport: Transport{Type: TransportTCP},
		Security: Security{Type: SecurityNone}, ConnIdleSeconds: 300}
	for _, kt := range []string{TypeSingbox, TypeRealm} {
		err := CheckSettingsConditions(kt, &in)
		if err == nil {
			t.Fatalf("%s: connIdle must be rejected", kt)
		}
		if strings.Contains(err.Error(), "sing-box 没有") || strings.Contains(err.Error(), "keepalive") {
			t.Fatalf("%s: copy must not narrate sing-box mechanics: %v", kt, err)
		}
		if !strings.Contains(err.Error(), "仅支持 Xray 内核：其他内核没有 Xray 式的空闲超时") {
			t.Fatalf("%s: want neutral copy, got %v", kt, err)
		}
	}
	norm := Inbound{Protocol: ProtocolVLESS, Port: 443, Transport: Transport{Type: TransportTCP},
		Security: Security{Type: SecurityNone}, ConnIdleSeconds: 300}
	if err := Normalize(TypeSingbox, &norm); err == nil ||
		!strings.Contains(err.Error(), "仅支持 Xray 内核：其他内核没有 Xray 式的空闲超时") {
		t.Fatalf("Normalize must reject with the same neutral copy, got %v", err)
	}
}

// R4-C2C-01: 构建器创建面拒绝链式保留组名。
func TestTemplateBuilderRejectsChainGroupNames(t *testing.T) {
	for _, name := range []string{ChainLandingGroup, ChainRelayGroup} {
		b := builderFixture()
		b.Groups = []TemplateBuilderGroup{{Name: name, Type: "select"}}
		if _, err := BuildSubscriptionTemplate(TemplateClash, b); err == nil ||
			!strings.Contains(err.Error(), "保留名") {
			t.Fatalf("builder group %q must be refused as a reserved chain name, got %v", name, err)
		}
	}
}

// R4-C2C-01: CheckTemplate 对与链式保留名同名的模板组给出改名警告。
func TestCheckTemplateWarnsChainGroupName(t *testing.T) {
	tpl := "proxy-groups:\n  - {name: 落地节点, type: select, proxies: [DIRECT]}\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"
	s, err := CheckTemplate(TemplateClash, tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range s.Warnings {
		if strings.Contains(w, "落地节点") && strings.Contains(w, "链式") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a chain-rename warning, got %v", s.Warnings)
	}
}
