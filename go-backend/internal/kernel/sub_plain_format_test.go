package kernel

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// 2.1.2 原格式钉子（处理/规则/脚本删除后）：零选项渲染的尾巴逐字——
//   - Clash/Mihomo：rules 恒为 GEOIP,LAN,DIRECT,no-resolve + MATCH,
//     节点选择；无 rule-providers、无 DNS/嗅探设置段；
//   - sing-box：route 恒为内置三条（sniff / hijack-dns / 私网直连）
//   - final 节点选择，无 dns 段、无 rule_set；
//   - Surge：[Rule] 恒为 GEOIP,LAN,DIRECT + FINAL,节点选择；
//   - base64：解码后逐行＝各端点的原始分享 URI（同名 #N 后缀除外）。
func TestPlainFormatTails(t *testing.T) {
	eps := shareFixtures(t)

	clash, _, err := Subscription(FormatClash, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(clash, "rules:\n  - \"GEOIP,LAN,DIRECT,no-resolve\"\n  - \"MATCH,节点选择\"\n") {
		t.Fatalf("clash rules tail drifted:\n%s", clash)
	}
	for _, bad := range []string{"rule-providers", "fake-ip", "sniffer"} {
		if strings.Contains(clash, bad) {
			t.Fatalf("clash plain output must not carry %q:\n%s", bad, clash)
		}
	}

	sb, _, err := Subscription(FormatSingbox, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		DNS   map[string]interface{} `json:"dns"`
		Route struct {
			Final string                   `json:"final"`
			Rules []map[string]interface{} `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(sb), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DNS != nil || strings.Contains(sb, "rule_set") {
		t.Fatalf("sing-box plain output must have no dns / rule_set:\n%s", sb)
	}
	if cfg.Route.Final != "节点选择" || len(cfg.Route.Rules) != 3 ||
		cfg.Route.Rules[0]["action"] != "sniff" ||
		cfg.Route.Rules[1]["action"] != "hijack-dns" || cfg.Route.Rules[1]["protocol"] != "dns" ||
		cfg.Route.Rules[2]["ip_is_private"] != true || cfg.Route.Rules[2]["outbound"] != "direct" {
		t.Fatalf("sing-box built-in route drifted: %+v", cfg.Route)
	}

	surge, _, err := Subscription(FormatSurge, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(surge, "[Rule]\nGEOIP,LAN,DIRECT\nFINAL,节点选择\n") {
		t.Fatalf("surge rule tail drifted:\n%s", surge)
	}

	b64, _, err := Subscription(FormatBase64, eps, SubOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != len(eps) {
		t.Fatalf("base64 must carry %d raw URIs, got %d:\n%s", len(eps), len(lines), raw)
	}
	for i, line := range lines {
		src := eps[i]
		if i == len(lines)-1 {
			// 末端点与首端点同名（夹具的中转副本）：渲染先把名字唯一
			// 化为「东京 Reality #2」再生成 URI。
			src.Name += " #2"
		}
		uri, ok := ShareURI(src)
		if !ok {
			t.Fatalf("no share URI for fixture %d", i)
		}
		if line != uri {
			t.Fatalf("base64 line %d is not the raw share URI:\n got %s\nwant %s", i, line, uri)
		}
	}
}
