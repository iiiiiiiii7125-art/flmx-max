package kernel

import (
	"net/url"
	"strings"
	"testing"
)

// R31-L4-E01（修复批改写，断言更新 1 项，修法点名驱动）：Clash 进口
// plugin: obfs-local 须归一为面板内规范名 obfs（与 applySSPlugin 口径
// 一致：obfs-local / simple-obfs / obfs 三名同归 obfs），四面全带插件。
// 原探针 PASS 记录现状（进口零告警原值落库，sing-box / Surge 面整节点
// 丢弃、URI 面静默丢插件参数，仅 Clash 面透传）；本用例改写为断言四面
// 全带插件，修复前失败（先红）、修复后通过（后绿）。反向验证用例见
// R31-L4-matrices.md §1.6；附则 A R2×C8 格复核即本用例。
func TestR31L4E01ObfsLocalPluginDropped(t *testing.T) {
	t.Run("obfs-local", func(t *testing.T) {
		checkObfsAliasFaces(t, "obfs-local")
	})
	t.Run("simple-obfs", func(t *testing.T) {
		checkObfsAliasFaces(t, "simple-obfs")
	})
	t.Run("control-obfs", func(t *testing.T) {
		checkObfsAliasFaces(t, "obfs")
	})
	t.Run("bogus-mode-rejected", func(t *testing.T) {
		yamlText := "proxies:\n" +
			"  - name: r31-obfs-bogus\n" +
			"    type: ss\n" +
			"    server: 203.0.113.50\n" +
			"    port: 8388\n" +
			"    cipher: aes-128-gcm\n" +
			"    password: r31obfs\n" +
			"    plugin: obfs-local\n" +
			"    plugin-opts:\n" +
			"      mode: bogus\n" +
			"      host: ob.example.net\n"
		nodes, errs := ParseImport(yamlText)
		if len(nodes) != 0 {
			t.Fatalf("CELL E01 bogus mode with obfs-local must be rejected, got %d node(s)", len(nodes))
		}
		if len(errs) == 0 {
			t.Fatal("CELL E01 bogus mode with obfs-local must be rejected with an explicit error")
		}
		if !strings.Contains(errs[0], "obfs mode") {
			t.Fatalf("CELL E01 bogus mode rejection must name the obfs mode gate, got %q", errs[0])
		}
		t.Logf("CELL E01 bogus mode with obfs-local rejected: %s", errs[0])
	})
}

// checkObfsAliasFaces 断言：Clash 进口给定 plugin 拼写后，落库归一为
// obfs、mode: TLS 愈合为 tls，且 sing-box / URI / Surge / Clash 四面
// 全带插件。
func checkObfsAliasFaces(t *testing.T, plugin string) {
	t.Helper()
	yamlText := "proxies:\n" +
		"  - name: r31-obfs-alias\n" +
		"    type: ss\n" +
		"    server: 203.0.113.50\n" +
		"    port: 8388\n" +
		"    cipher: aes-128-gcm\n" +
		"    password: r31obfs\n" +
		"    plugin: " + plugin + "\n" +
		"    plugin-opts:\n" +
		"      mode: TLS\n" +
		"      host: ob.example.net\n"
	nodes, errs := ParseImport(yamlText)
	if len(errs) != 0 {
		t.Fatalf("CELL E01 plugin=%s import errs=%v", plugin, errs)
	}
	if len(nodes) != 1 {
		t.Fatalf("CELL E01 plugin=%s expected 1 node, got %d", plugin, len(nodes))
	}
	n := nodes[0]
	if got := n.Proxy.str("plugin"); got != "obfs" {
		t.Fatalf("CELL E01 plugin=%s stored plugin must normalize to obfs, got %q", plugin, got)
	}
	t.Logf("CELL E01 plugin=%s stored plugin=obfs (normalized)", plugin)
	if got := n.Proxy.sub("plugin-opts").str("mode"); got != "tls" {
		t.Fatalf("CELL E01 plugin=%s stored plugin-opts.mode must heal TLS->tls, got %q", plugin, got)
	}
	t.Logf("CELL E01 plugin=%s stored plugin-opts.mode=tls (A2-03 gate healed)", plugin)

	e := Endpoint{Name: n.Name, Host: n.Host, Port: n.Port, Direct: true, Proxy: n.Proxy}

	outs, ok := proxySingbox(e)
	if !ok {
		t.Fatalf("CELL E01 plugin=%s sing-box face: whole node dropped (ok=false)", plugin)
	}
	if outs[0]["plugin"] != "obfs-local" {
		t.Fatalf("CELL E01 plugin=%s sing-box face: outbound plugin must be obfs-local, got %v", plugin, outs[0]["plugin"])
	}
	if outs[0]["plugin_opts"] != "obfs=tls;obfs-host=ob.example.net" {
		t.Fatalf("CELL E01 plugin=%s sing-box face: plugin_opts must be obfs=tls;obfs-host=ob.example.net, got %v", plugin, outs[0]["plugin_opts"])
	}
	t.Logf("CELL E01 plugin=%s sing-box face: outbound carries plugin obfs-local", plugin)

	link, ok := proxyURI(e)
	if !ok {
		t.Fatalf("CELL E01 plugin=%s uri face: no link emitted", plugin)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("CELL E01 plugin=%s uri face: link unparsable: %v", plugin, err)
	}
	if got := u.Query().Get("plugin"); got != "obfs-local;obfs=tls;obfs-host=ob.example.net" {
		t.Fatalf("CELL E01 plugin=%s uri face: plugin param must be obfs-local;obfs=tls;obfs-host=ob.example.net, got %q", plugin, got)
	}
	t.Logf("CELL E01 plugin=%s uri face: link carries plugin=obfs-local;obfs=tls;obfs-host=ob.example.net", plugin)

	line, ok := proxySurge(e)
	if !ok {
		t.Fatalf("CELL E01 plugin=%s surge face: whole node dropped (ok=false)", plugin)
	}
	if !strings.Contains(line, "obfs=tls") || !strings.Contains(line, "obfs-host=ob.example.net") {
		t.Fatalf("CELL E01 plugin=%s surge face: line must carry obfs=tls + obfs-host=ob.example.net, got %q", plugin, line)
	}
	t.Logf("CELL E01 plugin=%s surge face: line carries obfs=tls obfs-host=ob.example.net", plugin)

	clashNode, ok := proxyClash(e)
	if !ok {
		t.Fatalf("CELL E01 plugin=%s clash face: whole node dropped (ok=false)", plugin)
	}
	var clashPlugin string
	var clashMode, clashHost string
	for _, f := range clashNode {
		if f.Key == "plugin" {
			clashPlugin, _ = f.Value.(string)
		}
		if f.Key == "plugin-opts" {
			if po, isMap := f.Value.(map[string]interface{}); isMap {
				clashMode, _ = po["mode"].(string)
				clashHost, _ = po["host"].(string)
			}
		}
	}
	if clashPlugin != "obfs" || clashMode != "tls" || clashHost != "ob.example.net" {
		t.Fatalf("CELL E01 plugin=%s clash face: plugin=obfs mode=tls host=ob.example.net expected, got plugin=%q mode=%q host=%q", plugin, clashPlugin, clashMode, clashHost)
	}
	t.Logf("CELL E01 plugin=%s clash face: plugin=obfs plugin-opts.mode=tls host=ob.example.net", plugin)
}
