package kernel

import "testing"

// R5-D-02（P3，r5 交叉复核裁定 x-verdicts-r5.md §1/§8）：
// 中继面 forEndpoint 的 ws 注入块只判空精确 "Host"/"host"，
// 存量 HOST/hOsT 行被注入 headers["Host"]=orig 遮蔽——
// transportFields（规范 Host 优先）读到的是 orig 域名而非用户值。
// 本测试在修复前跑红（HOST/hOsT 格）、修复后全绿。
// 验收格：中继面 4 拼写格（HOST/hOsT 行中继面 host 回归用户值、
// 单键不双键）+ 列表值格（含列表值在内的全拼写判空）
// + 直接面 4 拼写格不变 + Host/host 格不变
// + 空 headers 对照格（无用户值时仍注入 orig，不破坏既有缺省行为）。

func TestR5D02RelayHostShadowing(t *testing.T) {
	cells := []struct {
		name  string
		key   string
		value interface{}
	}{
		{name: "Host-string", key: "Host", value: "example.com"},
		{name: "host-string", key: "host", value: "example.com"},
		{name: "HOST-string", key: "HOST", value: "example.com"},
		{name: "hOsT-string", key: "hOsT", value: "example.com"},
		{name: "HOST-list", key: "HOST", value: []interface{}{"example.com"}},
		{name: "hOsT-list", key: "hOsT", value: []interface{}{"example.com"}},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			p := Proxy{
				"type": "vmess", "server": "cdn.example.net", "port": 443,
				"uuid": "11111111-1111-1111-1111-111111111111", "network": "ws",
				"ws-opts": Proxy{"path": "/ws", "headers": Proxy{c.key: c.value}},
			}
			// 中继面：不得注入，不得双键，host 须为用户值。
			relayed := p.forEndpoint(Endpoint{Name: c.name, Host: "cdn.example.net", Port: 443, Direct: false, Proxy: p})
			headers := relayed.sub("ws-opts").sub("headers")
			if len(headers) != 1 {
				t.Fatalf("relay headers=%v want exactly 1 key (no injected Host)", map[string]interface{}(headers))
			}
			for k := range headers {
				if k != c.key {
					t.Fatalf("relay headers key=%q want %q (injected canonical key shadowed the user key)", k, c.key)
				}
			}
			host, _, _, _ := transportFields(relayed)
			if host != "example.com" {
				t.Fatalf("relay transportFields host=%q want %q", host, "example.com")
			}
			// 直接面：早返，4 拼写均不受影响。
			direct := p.forEndpoint(Endpoint{Name: c.name, Host: "cdn.example.net", Port: 443, Direct: true, Proxy: p})
			dhost, _, _, _ := transportFields(direct)
			if dhost != "example.com" {
				t.Fatalf("direct transportFields host=%q want %q", dhost, "example.com")
			}
		})
	}

	// 空 headers 对照格：无用户值时仍注入 orig，缺省行为不变。
	t.Run("empty-headers-still-injected", func(t *testing.T) {
		p := Proxy{
			"type": "vmess", "server": "cdn.example.net", "port": 443,
			"uuid": "11111111-1111-1111-1111-111111111111", "network": "ws",
			"ws-opts": Proxy{"path": "/ws"},
		}
		relayed := p.forEndpoint(Endpoint{Name: "empty", Host: "cdn.example.net", Port: 443, Direct: false, Proxy: p})
		headers := relayed.sub("ws-opts").sub("headers")
		if len(headers) != 1 || headers.str("Host") != "cdn.example.net" {
			t.Fatalf("empty headers relay headers=%v want {Host: cdn.example.net}", map[string]interface{}(headers))
		}
		host, _, _, _ := transportFields(relayed)
		if host != "cdn.example.net" {
			t.Fatalf("empty headers relay transportFields host=%q want %q", host, "cdn.example.net")
		}
	})
}
