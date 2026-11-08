package kernel

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// R4-C2C-01: 存量模板组名与链式组名相撞时，渲染面同名组不得双出——
// 链式组改名追加且拨号引用跟随改名后的中转组。三 kind 逐格封。
func TestTemplateChainGroupNameCollision(t *testing.T) {
	t.Run("clash landing collides", func(t *testing.T) {
		opts := chainOpts
		opts.Templates = map[string]string{TemplateClash: "proxy-groups:\n  - {name: 落地节点, type: select, proxies: [DIRECT]}\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"}
		body, _, err := Subscription(FormatClash, chainEndpoints(), opts)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Proxies []map[string]interface{} `yaml:"proxies"`
			Groups  []map[string]interface{} `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		var renamed []interface{}
		for _, g := range cfg.Groups {
			counts[g["name"].(string)]++
			if g["name"] == ChainLandingGroup+" · 链式 1" {
				renamed, _ = g["proxies"].([]interface{})
			}
		}
		if counts[ChainLandingGroup] != 1 || counts[ChainLandingGroup+" · 链式 1"] != 1 || counts[ChainRelayGroup] != 1 {
			t.Fatalf("group name counts must be exactly one each: %v\n%s", counts, body)
		}
		if len(renamed) != 2 {
			t.Fatalf("renamed chain landing group must hold the landing nodes: %v", renamed)
		}
		if cfg.Proxies[0]["dialer-proxy"] != ChainRelayGroup {
			t.Fatalf("relay name untouched, dialer-proxy must stay %q: %v", ChainRelayGroup, cfg.Proxies[0])
		}
	})

	t.Run("clash relay collides", func(t *testing.T) {
		opts := chainOpts
		opts.Templates = map[string]string{TemplateClash: "proxy-groups:\n  - {name: 中转节点, type: select, proxies: [DIRECT]}\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH,选择]\n"}
		body, _, err := Subscription(FormatClash, chainEndpoints(), opts)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Proxies []map[string]interface{} `yaml:"proxies"`
			Groups  []map[string]interface{} `yaml:"proxy-groups"`
		}
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, g := range cfg.Groups {
			counts[g["name"].(string)]++
		}
		if counts[ChainRelayGroup] != 1 || counts[ChainRelayGroup+" · 链式 1"] != 1 || counts[ChainLandingGroup] != 1 {
			t.Fatalf("group name counts must be exactly one each: %v\n%s", counts, body)
		}
		if cfg.Proxies[0]["dialer-proxy"] != ChainRelayGroup+" · 链式 1" {
			t.Fatalf("dialer-proxy must follow the renamed relay group: %v", cfg.Proxies[0])
		}
	})

	t.Run("singbox landing collides", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "落地节点", "outbounds": ["direct"]},
			{"type": "selector", "tag": "节点选择", "outbounds": ["{all}"]},
			{"type": "direct", "tag": "direct"}
		], "route": {"final": "节点选择"}}`
		opts := chainOpts
		opts.Templates = map[string]string{TemplateSingbox: tpl}
		body, _, err := Subscription(FormatSingbox, chainEndpoints(), opts)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Outbounds []map[string]interface{} `json:"outbounds"`
		}
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		var renamed []interface{}
		detour := ""
		for _, o := range cfg.Outbounds {
			tag, _ := o["tag"].(string)
			counts[tag]++
			if tag == ChainLandingGroup+" · 链式 1" {
				renamed, _ = o["outbounds"].([]interface{})
			}
			if tag == "美国 落地 01" {
				detour, _ = o["detour"].(string)
			}
		}
		if counts[ChainLandingGroup] != 1 || counts[ChainLandingGroup+" · 链式 1"] != 1 || counts[ChainRelayGroup] != 1 {
			t.Fatalf("outbound tag counts must be exactly one each (sing-box rejects duplicates): %v\n%s", counts, body)
		}
		if len(renamed) != 2 {
			t.Fatalf("renamed chain landing selector must hold the landing nodes: %v", renamed)
		}
		if detour != ChainRelayGroup {
			t.Fatalf("relay name untouched, detour must stay %q, got %q", ChainRelayGroup, detour)
		}
	})

	t.Run("surge landing collides", func(t *testing.T) {
		opts := chainOpts
		opts.Templates = map[string]string{TemplateSurge: "[Proxy Group]\n落地节点 = select, DIRECT\n选择 = select, include-all-proxies=1\n"}
		body, _, err := Subscription(FormatSurge, chainEndpoints(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(body, "\n"+ChainLandingGroup+" = "); n != 1 {
			t.Fatalf("template's own landing group must appear exactly once, got %d:\n%s", n, body)
		}
		if !strings.Contains(body, "\n"+ChainLandingGroup+" · 链式 1 = select, 美国 落地 01, 美国 落地 02\n") {
			t.Fatalf("renamed chain landing group missing:\n%s", body)
		}
		if !strings.Contains(body, "underlying-proxy="+ChainRelayGroup) {
			t.Fatalf("relay name untouched, underlying-proxy must stay %q:\n%s", ChainRelayGroup, body)
		}
	})

	t.Run("surge relay collides", func(t *testing.T) {
		opts := chainOpts
		opts.Templates = map[string]string{TemplateSurge: "[Proxy Group]\n中转节点 = select, DIRECT\n选择 = select, include-all-proxies=1\n"}
		body, _, err := Subscription(FormatSurge, chainEndpoints(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(body, "\n"+ChainRelayGroup+" = "); n != 1 {
			t.Fatalf("template's own relay group must appear exactly once, got %d:\n%s", n, body)
		}
		if !strings.Contains(body, "underlying-proxy="+ChainRelayGroup+" · 链式 1") {
			t.Fatalf("underlying-proxy must follow the renamed relay group:\n%s", body)
		}
	})
}
