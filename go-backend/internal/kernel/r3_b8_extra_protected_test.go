package kernel

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// R3 批 8 · R3X-013（D-02）：Extra 受保护键表漏 sing-box Snell 的
// psk/mode 拼写。本文件的回归分三层：
//  1. 逐协议全集 diff：渲染每个协议的入站对象，其全部顶层键必须被
//     分类为「受保护」或「明示良性增量键」——出现未分类键即红，
//     防第三次漏拼写（新协议/新键名落地时先在这里暴露）。
//  2. 凭据/版本类键逐协议点名：渲染对象实际含该键（枚举与渲染对齐）
//     且该键在受保护表内。
//  3. 保存期/渲染期行为：Extra {psk}/{mode} 两面被拒；合法增量键
//     （sniffing 类）仍可保存（防过修）。

// r3b8BenignTopKeys 是渲染对象顶层键中既非凭据/版本类、也允许 Extra
// 增量合并的键，逐个注明理由；不在此表又不受保护的键一律红。
var r3b8BenignTopKeys = map[string]map[string]string{
	TypeSingbox: {
		// hy2 调优布尔与 tuic 拥塞算法：非凭据/版本，改之不破坏面板不变量。
		"ignore_client_bandwidth": "tuning flag",
		"congestion_control":      "tuning value",
		// hy2 伪装目标 URL：外观项，分享侧不据此生成凭据。
		"masquerade": "camouflage appearance",
	},
	TypeXray: {
		// sniffing 正是 Extra 的预期用途本身（F-022 注释口径）。
		"sniffing": "documented Extra use case",
		// streamSettings 顶层不整表保护，其安全/传输子键由
		// extraProtectedStreamSubKeys 单独守（见子键 sweep）。
		"streamSettings": "subkeys guarded separately",
	},
}

type r3b8RenderSample struct {
	name     string
	kernel   string
	in       Inbound
	credKeys []string // 该协议渲染对象的凭据/版本类顶层键全集
}

func r3b8Samples() []r3b8RenderSample {
	tlsSec := Security{Type: SecurityTLS, SNI: "www.bing.com"}
	return []r3b8RenderSample{
		{"singbox/vless", TypeSingbox, Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443,
			Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityReality}}, []string{"users"}},
		{"singbox/vmess", TypeSingbox, Inbound{ID: 2, Protocol: ProtocolVMess, Port: 8443,
			Settings: Settings{UUID: testUUID}, Security: Security{Type: SecurityNone}}, []string{"users"}},
		{"singbox/trojan", TypeSingbox, Inbound{ID: 3, Protocol: ProtocolTrojan, Port: 8444,
			Settings: Settings{Password: "trojan-secret"}, Security: tlsSec}, []string{"users"}},
		{"singbox/shadowsocks", TypeSingbox, Inbound{ID: 4, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: DefaultSSMethod, Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}, []string{"method", "password"}},
		{"singbox/shadowsocks+shadowtls", TypeSingbox, Inbound{ID: 5, Protocol: ProtocolShadowsocks, Port: 8389,
			Settings: Settings{Method: DefaultSSMethod, Password: "AAAAAAAAAAAAAAAAAAAAAA=="},
			Security: Security{Type: SecurityShadowTLS, SNI: "www.microsoft.com", ShadowTLSPassword: "stls-secret-123"}},
			[]string{"method", "password", "version", "users", "handshake", "detour"}},
		{"singbox/snell-v5", TypeSingbox, Inbound{ID: 6, Protocol: ProtocolSnell, Port: 6160,
			Settings: Settings{Password: "psk-aaaaaaaa", SnellVersion: 5}}, []string{"version", "psk"}},
		{"singbox/snell-v6-mode", TypeSingbox, Inbound{ID: 7, Protocol: ProtocolSnell, Port: 6161,
			Settings: Settings{Password: "psk-bbbbbbbbbbbb", SnellVersion: 6, SnellMode: SnellModeUnshaped}}, []string{"version", "psk", "mode"}},
		{"singbox/snell+shadowtls", TypeSingbox, Inbound{ID: 8, Protocol: ProtocolSnell, Port: 6162,
			Settings: Settings{Password: "psk-cccccccc"},
			Security: Security{Type: SecurityShadowTLS, SNI: "www.microsoft.com", ShadowTLSPassword: "stls-pass-1"}},
			[]string{"version", "psk", "users", "handshake", "detour"}},
		{"singbox/hysteria2", TypeSingbox, Inbound{ID: 9, Protocol: ProtocolHysteria2, Port: 443,
			Transport: Transport{Type: TransportQUIC}, Security: tlsSec,
			Settings: Settings{Password: "hy2-secret", Masquerade: "https://www.bing.com"}}, []string{"users"}},
		{"singbox/tuic", TypeSingbox, Inbound{ID: 10, Protocol: ProtocolTUIC, Port: 8445,
			Transport: Transport{Type: TransportQUIC}, Security: tlsSec,
			Settings: Settings{UUID: testUUID, Password: "tuic-secret"}}, []string{"users"}},
		{"singbox/anytls", TypeSingbox, Inbound{ID: 11, Protocol: ProtocolAnyTLS, Port: 9443,
			Security: tlsSec, Settings: Settings{Password: "anytls-secret"}}, []string{"users"}},
		{"singbox/naive", TypeSingbox, Inbound{ID: 12, Protocol: ProtocolNaive, Port: 9444,
			Security: Security{Type: SecurityTLS, CertMode: CertModeACME, SNI: "hk.example.com", ACMEEmail: "me@example.com"},
			Settings: Settings{Username: "naive-user", Password: "naive-secret"}}, []string{"users"}},
		{"xray/vless", TypeXray, Inbound{ID: 13, Protocol: ProtocolVLESS, Port: 443,
			Settings: Settings{UUID: testUUID, Flow: FlowVision}, Security: Security{Type: SecurityReality}}, []string{"settings"}},
		{"xray/vmess-ws", TypeXray, Inbound{ID: 14, Protocol: ProtocolVMess, Port: 8443,
			Settings: Settings{UUID: testUUID}, Transport: Transport{Type: TransportWS}, Security: Security{Type: SecurityNone}}, []string{"settings"}},
		{"xray/trojan-grpc-tls", TypeXray, Inbound{ID: 15, Protocol: ProtocolTrojan, Port: 8444,
			Settings: Settings{Password: "trojan-secret"}, Transport: Transport{Type: TransportGRPC}, Security: tlsSec}, []string{"settings"}},
		{"xray/shadowsocks", TypeXray, Inbound{ID: 16, Protocol: ProtocolShadowsocks, Port: 8388,
			Settings: Settings{Method: DefaultSSMethod, Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}, []string{"settings"}},
	}
}

func TestR3B8ProtectedKeyUniverseDiff(t *testing.T) {
	for _, sample := range r3b8Samples() {
		t.Run(sample.name, func(t *testing.T) {
			in := sample.in
			if err := Normalize(sample.kernel, &in); err != nil {
				t.Fatalf("normalize: %v", err)
			}
			out, err := Render(sample.kernel, []Inbound{in})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			cfg := renderJSONString(t, out.Files[0].Content)
			rawList, ok := cfg["inbounds"].([]interface{})
			if !ok || len(rawList) == 0 {
				t.Fatalf("rendered config has no inbounds: %v", cfg)
			}
			union := map[string]bool{}
			for _, raw := range rawList {
				item, ok := raw.(map[string]interface{})
				if !ok {
					t.Fatalf("inbound object is not an object: %v", raw)
				}
				for k := range item {
					union[k] = true
					if extraProtectedTopKeys[k] {
						continue
					}
					if _, benign := r3b8BenignTopKeys[sample.kernel][k]; benign {
						continue
					}
					t.Errorf("unclassified top key %q in rendered %s object: it is neither in extraProtectedTopKeys nor in the benign allowlist — if it carries credentials/version semantics it must join the protected table (R3X-013 防第三次漏拼写)", k, sample.name)
				}
				// Xray streamSettings 子键同法 sweep：安全/传输子对象
				// 必须受子表保护，sockopt 是唯一良性子键。
				if stream, ok := item["streamSettings"].(map[string]interface{}); ok {
					for sk := range stream {
						if extraProtectedStreamSubKeys[sk] || sk == "sockopt" {
							continue
						}
						t.Errorf("unclassified streamSettings subkey %q in %s: neither protected nor the benign sockopt", sk, sample.name)
					}
				}
			}
			// 凭据/版本类键名全集与受保护表 diff 必须为空：逐个先证
			// 渲染对象真含该键（枚举与渲染对齐），再证其受保护。
			for _, ck := range sample.credKeys {
				if !union[ck] {
					t.Errorf("credential/version key %q missing from rendered %s object — the per-protocol enumeration drifted from the renderer", ck, sample.name)
				}
				if !extraProtectedTopKeys[ck] {
					t.Errorf("credential/version key %q of %s is NOT in extraProtectedTopKeys (diff non-empty)", ck, sample.name)
				}
			}
		})
	}
}

func TestR3B8ExtraPskModeRejectedAtSaveAndRender(t *testing.T) {
	snellV6 := func(extra string) Inbound {
		return Inbound{Protocol: ProtocolSnell, Port: 6160,
			Settings: Settings{Password: "psk-bbbbbbbbbbbb", SnellVersion: 6},
			Extra:    json.RawMessage(extra)}
	}
	snellV5 := func(extra string) Inbound {
		return Inbound{Protocol: ProtocolSnell, Port: 6161,
			Settings: Settings{Password: "psk-aaaaaaaa", SnellVersion: 5},
			Extra:    json.RawMessage(extra)}
	}
	cases := []struct {
		name string
		in   Inbound
		key  string
	}{
		{"snell v6 psk override", snellV6(`{"psk":"ab"}`), "psk"},
		{"snell v5 mode injection", snellV5(`{"mode":"unshaped"}`), "mode"},
		{"snell v6 mode override", snellV6(`{"mode":"unshaped"}`), "mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			err := Normalize(TypeSingbox, &in)
			if err == nil {
				t.Fatalf("Extra {%q} must be rejected at save time", tc.key)
			}
			if !strings.Contains(err.Error(), "受保护") || !strings.Contains(err.Error(), `"`+tc.key+`"`) {
				t.Fatalf("error must name the protected field %q, got %q", tc.key, err.Error())
			}
			// 渲染期兜底：绕过 Normalize 的存量行同样被拒（共用同一
			// 校验函数，补表即两面同关）。
			raw := tc.in
			raw.Listen = "::"
			if _, err := Render(TypeSingbox, []Inbound{raw}); err == nil {
				t.Fatalf("render must reject Extra {%q} bypassing Normalize", tc.key)
			} else if !strings.Contains(err.Error(), "受保护") {
				t.Fatalf("render error must name the protected field, got %q", err.Error())
			}
		})
	}
}

func TestR3B8BenignExtraStillAllowed(t *testing.T) {
	// 防过修：sniffing 类合法增量键在 sing-box Snell 入站上仍可保存
	// 与渲染（受保护表补两键不得扩大到整表拒绝）。
	in := Inbound{Protocol: ProtocolSnell, Port: 6160,
		Settings: Settings{Password: "psk-aaaaaaaa", SnellVersion: 5},
		Extra:    json.RawMessage(`{"sniffing":{"enabled":false}}`)}
	if err := Normalize(TypeSingbox, &in); err != nil {
		t.Fatalf("benign Extra must stay allowed at save time: %v", err)
	}
	out, err := Render(TypeSingbox, []Inbound{in})
	if err != nil {
		t.Fatalf("benign Extra must stay allowed at render time: %v", err)
	}
	if !strings.Contains(out.Files[0].Content, "sniffing") {
		t.Fatalf("benign Extra must be merged into rendered config:\n%s", out.Files[0].Content)
	}
	keys := make([]string, 0, len(extraProtectedTopKeys))
	for k := range extraProtectedTopKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	t.Logf("protected table after fix: %v", keys)
}
