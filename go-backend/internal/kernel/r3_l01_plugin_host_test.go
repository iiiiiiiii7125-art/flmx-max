package kernel

import (
	"strings"
	"testing"
)

// FLMX 1.7.1 发版后完全审计复审计轮 R3-L-01（P2）：
// 插件载体 Host 拼写交叉维——校验侧 EqualFold 全拼写放行、渲染侧
// 10 处精确读小写 `host`，大写 `Host` 合法值在 URI / sing-box /
// Surge / Loon / QX 五面静默丢失或损坏。与 R2-L-01（传输载体）
// 同机制、不同载体；修复分支 (a)：渲染侧改走与
// firstHeaderHostValue 同形的拼写无关读法，存储键不改写
// （CLOSED-LIST §2e 决策维持）。每格先红后绿：以下断言在修复前
// 失败、修复后通过。探针复现：L 域 TestR3LProbePluginHostSpelling
//（r3-l-probe.log）。

// r3L01ClashSS builds a Clash SS node whose plugin Host is carried
// under the given key spelling, holding the given value.
func r3L01ClashSS(name, plugin, opts string) string {
	return "proxies:\n" +
		"  - name: " + name + "\n" +
		"    type: ss\n" +
		"    server: 1.2.3.4\n" +
		"    port: 8388\n" +
		"    cipher: aes-128-gcm\n" +
		"    password: testpassword123\n" +
		"    plugin: " + plugin + "\n" +
		"    plugin-opts:\n" + opts
}

func r3L01Endpoint(n ImportedNode) Endpoint {
	return Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Direct: true, Proxy: n.Proxy}
}

func TestR3L01PluginHostSpelling(t *testing.T) {
	// Full import path: real Clash text through ParseImport, then
	// render on every face. Storage assertion first: a capital Host
	// must survive import verbatim (the validator accepts it).
	t.Run("ss-v2ray-capital-Host", func(t *testing.T) {
		n := mustImportOne(t, r3L01ClashSS("ss-v2ray-capital-Host", "v2ray-plugin",
			"      mode: websocket\n      path: /ws\n      Host: example.com\n"))
		if got := n.Proxy.sub("plugin-opts").str("Host"); got != "example.com" {
			t.Fatalf("capital Host must survive import, got %q", got)
		}
		e := r3L01Endpoint(n)
		// proxyURI URL-escapes the plugin string, so assert the raw
		// plugin string (what proxyURI embeds) rather than the URI.
		if got := ssPluginString(n.Proxy); !strings.Contains(got, "host=example.com") {
			t.Fatalf("URI face ssPluginString must carry host=example.com, got %q", got)
		}
		if _, ok := proxyURI(e); !ok {
			t.Fatalf("URI face must render node")
		}
		outs, ok := proxySingbox(e)
		if !ok || len(outs) == 0 {
			t.Fatalf("sing-box face must render node, ok=%v outs=%v", ok, outs)
		}
		po, _ := outs[0]["plugin_opts"].(string)
		if !strings.Contains(po, "host=example.com") {
			t.Fatalf("sing-box plugin_opts must carry host=example.com, got %q", po)
		}
	})
	t.Run("ss-obfs-capital-Host", func(t *testing.T) {
		n := mustImportOne(t, r3L01ClashSS("ss-obfs-capital-Host", "obfs",
			"      mode: http\n      Host: example.com\n"))
		e := r3L01Endpoint(n)
		if got := ssPluginString(n.Proxy); !strings.Contains(got, "obfs-host=example.com") {
			t.Fatalf("URI face ssPluginString must carry obfs-host=example.com, got %q", got)
		}
		if _, ok := proxyURI(e); !ok {
			t.Fatalf("URI face must render node")
		}
		outs, ok := proxySingbox(e)
		if !ok || len(outs) == 0 {
			t.Fatalf("sing-box face must render node, ok=%v outs=%v", ok, outs)
		}
		po, _ := outs[0]["plugin_opts"].(string)
		if !strings.Contains(po, "obfs-host=example.com") {
			t.Fatalf("sing-box plugin_opts must carry obfs-host=example.com, got %q", po)
		}
	})
	t.Run("ss-shadowtls-capital-Host", func(t *testing.T) {
		n := mustImportOne(t, r3L01ClashSS("ss-shadowtls-capital-Host", "shadow-tls",
			"      Host: example.com\n      password: testpassword123\n      version: 3\n"))
		e := r3L01Endpoint(n)
		got := ssPluginString(n.Proxy)
		if !strings.Contains(got, "shadow-tls;host=example.com") {
			t.Fatalf("URI face ssPluginString must carry shadow-tls;host=example.com, got %q", got)
		}
		if strings.Contains(got, "shadow-tls;host=;") {
			t.Fatalf("URI face must not emit an empty host, got %q", got)
		}
		if _, ok := proxyURI(e); !ok {
			t.Fatalf("URI face must render node")
		}
		outs, ok := proxySingbox(e)
		if !ok || len(outs) != 2 {
			t.Fatalf("sing-box face must render node + shadowtls detour, ok=%v outs=%v", ok, outs)
		}
		tlsMap, _ := outs[1]["tls"].(map[string]interface{})
		if tlsMap == nil || tlsMap["server_name"] != "example.com" {
			t.Fatalf("sing-box shadowtls detour server_name must be example.com, got %#v", outs[1]["tls"])
		}
		line, ok := proxySurge(e)
		if !ok || !strings.Contains(line, "shadow-tls-sni=example.com") {
			t.Fatalf("Surge face must carry shadow-tls-sni=example.com, got %q ok=%v", line, ok)
		}
		loon, ok := loonLine(n.Proxy.forEndpoint(e))
		if !ok || !strings.Contains(loon, "shadow-tls-sni=example.com") {
			t.Fatalf("Loon face must carry shadow-tls-sni=example.com, got %q ok=%v", loon, ok)
		}
	})
	t.Run("snell-obfs-capital-Host", func(t *testing.T) {
		yamlText := "proxies:\n" +
			"  - name: snell-obfs-capital-Host\n" +
			"    type: snell\n" +
			"    server: 1.2.3.4\n" +
			"    port: 8388\n" +
			"    psk: testpassword123456\n" +
			"    version: 4\n" +
			"    obfs-opts:\n" +
			"      mode: http\n" +
			"      Host: example.com\n"
		n := mustImportOne(t, yamlText)
		if got := n.Proxy.sub("obfs-opts").str("Host"); got != "example.com" {
			t.Fatalf("capital Host must survive import, got %q", got)
		}
		line, ok := proxySurge(r3L01Endpoint(n))
		if !ok || !strings.Contains(line, "obfs-host=example.com") {
			t.Fatalf("Surge face must carry obfs-host=example.com, got %q ok=%v", line, ok)
		}
	})
	t.Run("ss-obfs-capital-Host-loon-qx", func(t *testing.T) {
		n := mustImportOne(t, r3L01ClashSS("ss-obfs-capital-Host-loon-qx", "obfs",
			"      mode: http\n      Host: example.com\n"))
		loon, ok := loonLine(n.Proxy.forEndpoint(r3L01Endpoint(n)))
		if !ok || !strings.Contains(loon, "obfs-host=example.com") {
			t.Fatalf("Loon face must carry obfs-host=example.com, got %q ok=%v", loon, ok)
		}
		qx, ok := qxLine(n.Proxy.forEndpoint(r3L01Endpoint(n)))
		if !ok || !strings.Contains(qx, "obfs-host=example.com") {
			t.Fatalf("QX face must carry obfs-host=example.com, got %q ok=%v", qx, ok)
		}
	})
	t.Run("control-ss-obfs-lowercase-host", func(t *testing.T) {
		// Control: canonical lowercase host renders on every face
		// before and after the fix — the gap is spelling, not the
		// render faces themselves.
		n := mustImportOne(t, r3L01ClashSS("control-ss-obfs-lowercase-host", "obfs",
			"      mode: http\n      host: example.com\n"))
		e := r3L01Endpoint(n)
		if got := ssPluginString(n.Proxy); !strings.Contains(got, "obfs-host=example.com") {
			t.Fatalf("control URI face ssPluginString must carry obfs-host=example.com, got %q", got)
		}
		if _, ok := proxyURI(e); !ok {
			t.Fatalf("control URI face must render node")
		}
		line, ok := proxySurge(e)
		if !ok || !strings.Contains(line, "obfs-host=example.com") {
			t.Fatalf("control Surge face must carry obfs-host=example.com, got %q ok=%v", line, ok)
		}
		loon, ok := loonLine(n.Proxy.forEndpoint(e))
		if !ok || !strings.Contains(loon, "obfs-host=example.com") {
			t.Fatalf("control Loon face must carry obfs-host=example.com, got %q ok=%v", loon, ok)
		}
		qx, ok := qxLine(n.Proxy.forEndpoint(e))
		if !ok || !strings.Contains(qx, "obfs-host=example.com") {
			t.Fatalf("control QX face must carry obfs-host=example.com, got %q ok=%v", qx, ok)
		}
	})
	t.Run("bad-label-capital-Host-still-rejected", func(t *testing.T) {
		// Negative control (S-04): a bad label under the capital
		// spelling must still be rejected at import, naming Host.
		nodes, errs := ParseImport(r3L01ClashSS("bad-label-capital-Host", "obfs",
			"      mode: http\n      Host: a..b\n"))
		if len(nodes) != 0 || len(errs) == 0 {
			t.Fatalf("ParseImport = %d nodes, errs=%v, want 0 nodes with an error", len(nodes), errs)
		}
		if !strings.Contains(errs[0], "Host 格式错误") || !strings.Contains(errs[0], "a..b") {
			t.Fatalf("err = %q, want it to name the bad Host value", errs[0])
		}
	})
}

// R3-L-01: list-valued plugin Host must take the first element, never
// stringify to "[example.com]" (same rule as R2-L-01 ws-list).
func TestR3L01PluginHostListFirstElement(t *testing.T) {
	p := Proxy{
		"type": "ss", "server": "1.2.3.4", "port": 8388,
		"cipher": "aes-128-gcm", "password": "testpassword123",
		"plugin":      "obfs",
		"plugin-opts": Proxy{"mode": "http", "Host": []interface{}{"example.com"}},
	}
	if got := ssPluginString(p); !strings.Contains(got, "obfs-host=example.com") || strings.Contains(got, "[example.com]") {
		t.Fatalf("list Host must render first element, got %q", got)
	}
}

// R3-L-01: when both spellings are present with different values, the
// canonical "Host" wins so the pick is deterministic (same rule as
// firstHeaderHostValue on the transport carriers).
func TestR3L01PluginHostBothSpellingsPreferCanonical(t *testing.T) {
	p := Proxy{
		"type": "ss", "server": "1.2.3.4", "port": 8388,
		"cipher": "aes-128-gcm", "password": "testpassword123",
		"plugin":      "obfs",
		"plugin-opts": Proxy{"mode": "http", "Host": "canonical.example.com", "host": "other.example.com"},
	}
	if got := ssPluginString(p); !strings.Contains(got, "obfs-host=canonical.example.com") {
		t.Fatalf("dual spelling must prefer canonical Host, got %q", got)
	}
}
