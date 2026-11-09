package kernel

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// RR4-C2-01: the node dedupe must consult the kind's full output
// namespace (template groups, non-group outbound tags, endpoint tags,
// Surge [Proxy] entries) plus the final node names.
// （2.1.2: the chain rename-pick cells left with the subscription
// chain removal — no chain groups are generated any more.）

func extraEndpoint(name string) Endpoint {
	return Endpoint{Name: name, Host: "203.0.113.5", Port: 8399, Inbound: Inbound{
		Protocol: ProtocolShadowsocks,
		Settings: Settings{Method: "aes-256-gcm", Password: "pw"},
	}}
}

func clashNameCounts(t *testing.T, body string) map[string]int {
	t.Helper()
	var cfg struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
		Groups  []map[string]interface{} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, p := range cfg.Proxies {
		counts[p["name"].(string)]++
	}
	for _, g := range cfg.Groups {
		counts[g["name"].(string)]++
	}
	return counts
}

func singboxTagCounts(t *testing.T, body string) map[string]int {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
		Endpoints []map[string]interface{} `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, o := range cfg.Outbounds {
		counts[o["tag"].(string)]++
	}
	for _, o := range cfg.Endpoints {
		counts[o["tag"].(string)]++
	}
	return counts
}

func allCountsOne(t *testing.T, counts map[string]int, body string) {
	t.Helper()
	for name, n := range counts {
		if n != 1 {
			t.Fatalf("name %q defined %d times, want exactly one: %v\n%s", name, n, counts, body)
		}
	}
}

func TestRR4C2TemplateUniverseCells(t *testing.T) {
	t.Run("singbox endpoint tag equals node name", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "节点选择", "outbounds": ["{all}"]},
			{"type": "direct", "tag": "direct"}
		], "endpoints": [
			{"type": "wireguard", "tag": "WG出口", "address": ["10.0.0.2/32"]}
		], "route": {"final": "节点选择"}}`
		opts := SubOptions{Templates: map[string]string{TemplateSingbox: tpl}}
		body, _, err := Subscription(FormatSingbox, namedEndpoints("WG出口", "美国 01"), opts)
		if err != nil {
			t.Fatal(err)
		}
		counts := singboxTagCounts(t, body)
		allCountsOne(t, counts, body)
		if counts["WG出口 #2"] != 1 {
			t.Fatalf("the node must move off the endpoint tag, the endpoint keeps it: %v", counts)
		}
	})

	t.Run("singbox non-group outbound tag equals node name", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "节点选择", "outbounds": ["{all}"]},
			{"type": "socks", "tag": "家宽出口", "server": "192.0.2.1", "server_port": 1080},
			{"type": "direct", "tag": "direct"}
		], "route": {"final": "节点选择"}}`
		opts := SubOptions{Templates: map[string]string{TemplateSingbox: tpl}}
		body, _, err := Subscription(FormatSingbox, namedEndpoints("家宽出口", "美国 01"), opts)
		if err != nil {
			t.Fatal(err)
		}
		counts := singboxTagCounts(t, body)
		allCountsOne(t, counts, body)
		if counts["家宽出口 #2"] != 1 {
			t.Fatalf("the node must move off the template outbound tag: %v", counts)
		}
	})

}

// RR4-C2-02: the ShadowTLS companion tag is derived at render time
// and used to sit outside every dedupe; a node literally named
// "X (shadowtls)" collided with node X's companion and sing-box
// rejected the whole profile. The companion moves, the detour
// follows, pre-existing names never move.

func shadowTLSInbound(name string) Endpoint {
	e := extraEndpoint(name)
	e.Inbound.Security = Security{Type: SecurityShadowTLS, SNI: "www.example.com",
		ShadowTLSPassword: "stlspw", Fingerprint: "chrome"}
	return e
}

func singboxOutboundByTag(t *testing.T, body, tag string) map[string]interface{} {
	t.Helper()
	var cfg struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] == tag {
			return o
		}
	}
	return nil
}

func TestRR4C2ShadowTLSCompanionDedupe(t *testing.T) {
	assertPair := func(t *testing.T, body string) {
		counts := singboxTagCounts(t, body)
		allCountsOne(t, counts, body)
		inner := singboxOutboundByTag(t, body, "甲")
		if inner == nil || inner["detour"] != "甲 (shadowtls) #2" {
			t.Fatalf("the inner outbound's detour must follow the renamed companion: %v\n%s", inner, body)
		}
		companion := singboxOutboundByTag(t, body, "甲 (shadowtls) #2")
		if companion == nil || companion["type"] != "shadowtls" {
			t.Fatalf("the renamed companion must be the shadowtls outbound: %v", companion)
		}
		if plain := singboxOutboundByTag(t, body, "甲 (shadowtls)"); plain == nil || plain["type"] == "shadowtls" {
			t.Fatalf("the pre-existing node keeps its name and is not the companion: %v", plain)
		}
	}

	t.Run("default profile", func(t *testing.T) {
		eps := []Endpoint{shadowTLSInbound("甲"), extraEndpoint("甲 (shadowtls)")}
		body, _, err := Subscription(FormatSingbox, eps, SubOptions{})
		if err != nil {
			t.Fatal(err)
		}
		assertPair(t, body)
	})

	t.Run("template profile", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "节点选择", "outbounds": ["{all}"]},
			{"type": "direct", "tag": "direct"}
		], "route": {"final": "节点选择"}}`
		opts := SubOptions{Templates: map[string]string{TemplateSingbox: tpl}}
		eps := []Endpoint{shadowTLSInbound("甲"), extraEndpoint("甲 (shadowtls)")}
		body, _, err := Subscription(FormatSingbox, eps, opts)
		if err != nil {
			t.Fatal(err)
		}
		assertPair(t, body)
	})

	t.Run("template endpoint holds the companion name", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "节点选择", "outbounds": ["{all}"]},
			{"type": "direct", "tag": "direct"}
		], "endpoints": [
			{"type": "wireguard", "tag": "甲 (shadowtls)", "address": ["10.0.0.2/32"]}
		], "route": {"final": "节点选择"}}`
		opts := SubOptions{Templates: map[string]string{TemplateSingbox: tpl}}
		body, _, err := Subscription(FormatSingbox, []Endpoint{shadowTLSInbound("甲")}, opts)
		if err != nil {
			t.Fatal(err)
		}
		counts := singboxTagCounts(t, body)
		allCountsOne(t, counts, body)
		if inner := singboxOutboundByTag(t, body, "甲"); inner == nil || inner["detour"] != "甲 (shadowtls) #2" {
			t.Fatalf("detour must follow the companion renamed off the endpoint tag: %v", inner)
		}
	})

	t.Run("imported shadow-tls proxy", func(t *testing.T) {
		imported := Endpoint{Name: "乙", Host: "203.0.113.9", Port: 8388, Proxy: Proxy{
			"type": "ss", "server": "203.0.113.9", "port": 8388,
			"cipher": "aes-256-gcm", "password": "pw",
			"plugin":      "shadow-tls",
			"plugin-opts": map[string]interface{}{"host": "www.example.com", "password": "stlspw", "version": 3},
		}}
		eps := []Endpoint{imported, extraEndpoint("乙 (shadowtls)")}
		body, _, err := Subscription(FormatSingbox, eps, SubOptions{})
		if err != nil {
			t.Fatal(err)
		}
		counts := singboxTagCounts(t, body)
		allCountsOne(t, counts, body)
		if inner := singboxOutboundByTag(t, body, "乙"); inner == nil || inner["detour"] != "乙 (shadowtls) #2" {
			t.Fatalf("imported pair: detour must follow the renamed companion: %v\n%s", inner, body)
		}
	})
}

// RR4-C2-03: CheckTemplate names duplicate definitions and dangling
// references inside handwritten templates (the builder face refuses
// the same defects outright). Reference checks need the node names;
// duplicates are judged on the template alone.

func warnHas(s TemplateSummary, sub string) bool {
	for _, w := range s.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestRR4C2CheckTemplateInternalIntegrity(t *testing.T) {
	t.Run("clash duplicate groups", func(t *testing.T) {
		tpl := "proxy-groups:\n  - {name: 选择, type: select, proxies: [DIRECT]}\n  - {name: 选择, type: select, include-all: true}\nrules: [MATCH，选择]\n"
		s, err := CheckTemplate(TemplateClash, tpl, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !warnHas(s, "同名") || !warnHas(s, "选择") {
			t.Fatalf("duplicate group must be named: %v", s.Warnings)
		}
	})

	t.Run("clash dangling member needs names", func(t *testing.T) {
		tpl := "proxy-groups:\n  - {name: 选择, type: select, proxies: [不存在的组, 香港 01]}\nrules: [MATCH，选择]\n"
		s, err := CheckTemplate(TemplateClash, tpl, []string{"香港 01"})
		if err != nil {
			t.Fatal(err)
		}
		if !warnHas(s, "不存在的组") {
			t.Fatalf("dangling member must be named: %v", s.Warnings)
		}
		if warnHas(s, "香港 01") && warnHas(s, "引用了") {
			t.Fatalf("a member naming a node is not dangling: %v", s.Warnings)
		}
		s, err = CheckTemplate(TemplateClash, tpl, nil)
		if err != nil {
			t.Fatal(err)
		}
		if warnHas(s, "不存在的组") {
			t.Fatalf("without node names a member cannot be judged: %v", s.Warnings)
		}
	})

	t.Run("singbox duplicate tags across outbounds and endpoints", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "选择", "outbounds": ["{all}"]},
			{"type": "socks", "tag": "重名", "server": "192.0.2.1", "server_port": 1080},
			{"type": "direct", "tag": "direct"}
		], "endpoints": [
			{"type": "wireguard", "tag": "重名", "address": ["10.0.0.2/32"]}
		], "route": {"final": "选择"}}`
		s, err := CheckTemplate(TemplateSingbox, tpl, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !warnHas(s, "同名") || !warnHas(s, "重名") {
			t.Fatalf("duplicate tag across outbounds/endpoints must be named: %v", s.Warnings)
		}
	})

	t.Run("singbox dangling final rule and member", func(t *testing.T) {
		tpl := `{"outbounds": [
			{"type": "selector", "tag": "选择", "outbounds": ["不存在甲", "{all}"]},
			{"type": "direct", "tag": "direct"}
		], "route": {"final": "不存在乙", "rules": [
			{"domain_suffix": ["example.com"], "outbound": "不存在丙"},
			{"type": "logical", "mode": "and", "rules": [{"protocol": ["dns"], "outbound": "不存在丁"}]}
		]}}`
		s, err := CheckTemplate(TemplateSingbox, tpl, []string{"节点一"})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"不存在甲", "不存在乙", "不存在丙", "不存在丁"} {
			if !warnHas(s, name) {
				t.Fatalf("dangling reference %q must be named: %v", name, s.Warnings)
			}
		}
	})

	t.Run("singbox shipped fixture stays clean", func(t *testing.T) {
		s, err := CheckTemplate(TemplateSingbox, singboxTpl, []string{"香港 01", "HK IPLC"})
		if err != nil {
			t.Fatal(err)
		}
		if warnHas(s, "引用了") || warnHas(s, "同名") || warnHas(s, "outbound not found") {
			t.Fatalf("the fixture template must not gain integrity warnings: %v", s.Warnings)
		}
	})

	t.Run("surge duplicates and dangling final", func(t *testing.T) {
		tpl := "[Proxy]\n同名代理 = direct\n同名代理 = direct\n\n[Proxy Group]\n选择 = select, DIRECT\n选择 = select, DIRECT\n\n[Rule]\nFINAL,没有的\n"
		s, err := CheckTemplate(TemplateSurge, tpl, []string{"香港 01"})
		if err != nil {
			t.Fatal(err)
		}
		if !warnHas(s, "同名") || !warnHas(s, "选择") || !warnHas(s, "同名代理") {
			t.Fatalf("duplicate groups and proxies must be named: %v", s.Warnings)
		}
		if !warnHas(s, "没有的") {
			t.Fatalf("dangling FINAL target must be named: %v", s.Warnings)
		}
	})

	t.Run("surge shipped fixture stays clean", func(t *testing.T) {
		s, err := CheckTemplate(TemplateSurge, surgeTpl, []string{"香港 01", "美国 01"})
		if err != nil {
			t.Fatal(err)
		}
		if warnHas(s, "引用了") || warnHas(s, "同名") || warnHas(s, "指向") {
			t.Fatalf("the fixture template must not gain integrity warnings: %v", s.Warnings)
		}
	})
}
