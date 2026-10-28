package kernel

import (
	"reflect"
	"strings"
	"testing"
)

// FLMX 1.7.1 发版后完全审计复审计轮 R2-L-01（P2）：
// 传输 Host 渲染侧单拼写读取——transportFields 仅 ws 双读，http/h2/xhttp
// 各单读，致合法 Host 在非 Clash 面静默丢失；ws 支用 str() 把列表值
// 串化为 "[example.com]"。每格先红后绿：以下断言在修复前失败、修复后通过。
// 探针复现：L 域 TestR2LProbeTransportHostSpelling（r2-l-probe-transport-host.log）。

const r2L01UUID = "11111111-1111-1111-1111-111111111111"

// r2L01Proxy builds a Clash-style raw proxy map (YAML-decoded shapes:
// list values are []interface{}) with the given network and transport opts.
func r2L01Proxy(t *testing.T, network string, optsKey string, opts Proxy) Proxy {
	t.Helper()
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": r2L01UUID, "network": network,
	}
	p[optsKey] = opts
	return p
}

func TestR2L01TransportHostSpelling(t *testing.T) {
	cells := []struct {
		name    string
		network string
		optsKey string
		opts    Proxy
	}{
		{
			name: "http-lowercase-host", network: "http", optsKey: "http-opts",
			opts: Proxy{"headers": Proxy{"host": []interface{}{"example.com"}}},
		},
		{
			name: "http-canonical-Host", network: "http", optsKey: "http-opts",
			opts: Proxy{"headers": Proxy{"Host": []interface{}{"example.com"}}},
		},
		{
			name: "xhttp-capital-Host", network: "xhttp", optsKey: "xhttp-opts",
			opts: Proxy{"Host": "example.com"},
		},
		{
			name: "xhttp-canonical-host", network: "xhttp", optsKey: "xhttp-opts",
			opts: Proxy{"host": "example.com"},
		},
		{
			name: "h2-capital-Host", network: "h2", optsKey: "h2-opts",
			opts: Proxy{"Host": []interface{}{"example.com"}},
		},
		{
			name: "h2-canonical-host", network: "h2", optsKey: "h2-opts",
			opts: Proxy{"host": []interface{}{"example.com"}},
		},
		{
			name: "ws-lowercase-host", network: "ws", optsKey: "ws-opts",
			opts: Proxy{"headers": Proxy{"host": "example.com"}},
		},
		{
			name: "ws-list-Host", network: "ws", optsKey: "ws-opts",
			opts: Proxy{"headers": Proxy{"Host": []interface{}{"example.com"}}},
		},
	}
	for _, c := range cells {
		t.Run(c.name, func(t *testing.T) {
			p := r2L01Proxy(t, c.network, c.optsKey, c.opts)
			host, _, _, _ := transportFields(p)
			if host != "example.com" {
				t.Fatalf("transportFields host=%q want %q", host, "example.com")
			}
			// URI 面：host 参数须带出，不得丢、不得串化为 "[example.com]"。
			uri, ok := proxyURI(Endpoint{Name: c.name, Host: "1.2.3.4", Port: 443, Direct: true, Proxy: p})
			if !ok {
				t.Fatalf("VLESS+%s 节点在 URI 面须下发", c.network)
			}
			if !strings.Contains(uri, "host=example.com") {
				t.Fatalf("URI 面须含 host=example.com，got %q", uri)
			}
			if strings.Contains(uri, "%5Bexample.com%5D") {
				t.Fatalf("URI 面不得把列表 Host 串化为 [example.com]，got %q", uri)
			}
			// sing-box 面：xhttp 按设计无此传输（恒略过），其余三网须带 host。
			outs, ok := proxySingbox(Endpoint{Name: c.name, Host: "1.2.3.4", Port: 443, Direct: true, Proxy: p})
			switch c.network {
			case "xhttp":
				if ok {
					t.Fatalf("sing-box 面 xhttp 按设计应略过，got ok")
				}
			default:
				if !ok || len(outs) == 0 {
					t.Fatalf("VLESS+%s 节点在 sing-box 面须下发", c.network)
				}
				tr, _ := outs[0]["transport"].(map[string]interface{})
				if tr == nil {
					t.Fatalf("sing-box 出站须有 transport，got %#v", outs[0])
				}
				switch c.network {
				case "ws":
					hdrs, _ := tr["headers"].(map[string]interface{})
					if hdrs == nil || hdrs["Host"] != "example.com" {
						t.Fatalf("sing-box ws transport headers Host 须为 example.com，got %#v", tr["headers"])
					}
				default: // h2 / http 映射为 sing-box http 传输，host 为单元素列表。
					if !reflect.DeepEqual(tr["host"], []string{"example.com"}) {
						t.Fatalf("sing-box %s transport host 须为 [example.com]，got %#v", c.network, tr["host"])
					}
				}
			}
		})
	}
}

// R2-L-01 阴性对照（B-01 不得放宽）：坏标签小写拼写仍须在进口被拒收。
func TestR2L01BadLabelLowercaseStillRejected(t *testing.T) {
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": r2L01UUID, "network": "http",
		"http-opts": Proxy{"headers": Proxy{"host": "a..b"}},
	}
	if _, err := finishProxy(p); err == nil || !strings.Contains(err.Error(), "Host 格式错误") {
		t.Fatalf("坏标签小写拼写进口须拒收并点名 Host 格式错误，got err=%v", err)
	}
}

// R2-L-01：双拼写并存时取数须确定——规范 "Host" 优先于 "host"。
func TestR2L01BothSpellingsPreferCanonical(t *testing.T) {
	p := Proxy{
		"type": "vless", "server": "1.2.3.4", "port": 443,
		"uuid": r2L01UUID, "network": "ws",
		"ws-opts": Proxy{"headers": Proxy{"Host": "canonical.example.com", "host": "other.example.com"}},
	}
	host, _, _, _ := transportFields(p)
	if host != "canonical.example.com" {
		t.Fatalf("双拼写并存时 transportFields 须取规范 Host，got %q", host)
	}
}
