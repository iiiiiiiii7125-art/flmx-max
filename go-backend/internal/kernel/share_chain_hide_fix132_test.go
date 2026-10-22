package kernel

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A5-03：ChainHideRelays 此前只在 Clash/sing-box/Surge 三条主渲染路径
// 生效，Loon/QX/Shadowrocket 的列表与规则配置全部绕过——开启「中转
// 节点仅出现在中转组」后，中转节点在这些格式里仍以普通节点出现。
func TestChainHideRelaysCoversClientFormats(t *testing.T) {
	opts := chainOpts
	opts.ChainHideRelays = true
	eps := chainEndpoints()

	// 列表格式无组概念：隐藏即不下发该行（落地在 Loon/QX 本就直连，
	// 无悬空引用）。
	for _, format := range []string{FormatLoon, FormatQX} {
		body, _, err := Subscription(format, eps, opts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, "香港 中转") {
			t.Fatalf("%s list must hide the relay node:\n%s", format, body)
		}
		if !strings.Contains(body, "美国 落地 01") || !strings.Contains(body, "日本 直连") {
			t.Fatalf("%s list lost landing/plain nodes:\n%s", format, body)
		}
	}

	// 规则配置：中转节点既不出现在节点行、也不进任何策略组。
	ruleOpts := opts
	ruleOpts.Rules = true
	for _, format := range []string{FormatLoon, FormatQX} {
		body, _, err := Subscription(format, eps, ruleOpts)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body, "香港 中转") {
			t.Fatalf("%s config must hide the relay node:\n%s", format, body)
		}
		if !strings.Contains(body, "美国 落地 01") {
			t.Fatalf("%s config lost landing nodes:\n%s", format, body)
		}
	}

	// Shadowrocket 列表：中转条目是落地副本 dialer-proxy 的指向目标，
	// 删之整链悬空——与 Clash 把中转保留在 proxies 同口径，条目保留、
	// 落地副本仍成链（格式无组层可藏，这是格式限制而非口径分叉）。
	sr, _, err := Subscription(FormatShadowrocket, eps, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sr, "香港 中转") || !strings.Contains(sr, "dialer-proxy") {
		t.Fatalf("shadowrocket profile must keep the relay as dialer target:\n%s", sr)
	}

	// Shadowrocket 规则配置：中转名不进节点选择等普通组（与三主格式
	// 的 listed() 语义对齐），但 proxies 里的中转条目与落地副本保留。
	src, _, err := Subscription(FormatShadowrocket, eps, ruleOpts)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
		Groups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	foundRelay := false
	for _, p := range doc.Proxies {
		if p["name"] == "香港 中转" {
			foundRelay = true
		}
	}
	if !foundRelay {
		t.Fatalf("shadowrocket config must keep the relay proxy entry:\n%s", src)
	}
	for _, g := range doc.Groups {
		for _, m := range g.Proxies {
			if m == "香港 中转" {
				t.Fatalf("shadowrocket group %s must not list the hidden relay:\n%s", g.Name, src)
			}
		}
	}
}
