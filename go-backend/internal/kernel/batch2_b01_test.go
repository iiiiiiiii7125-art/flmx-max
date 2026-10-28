// Batch 2 of the FLMX 1.7.1 post-release full audit (baseline main @
// c2f45a6): regression tests for B-01 — the import transport Host
// check only recognised the exact key spelling, so lowercase `host`
// (ws/http-opts headers) and uppercase `Host` (h2/xhttp-opts) escaped
// validation and landed verbatim in subscriptions. Written red first.
package kernel

import (
	"strings"
	"testing"
)

// b2ClashVMess builds a Clash VMess node whose transport Host is carried
// under the given carrier map and key spelling, holding the given value.
func b2ClashVMess(carrier, key, value string) string {
	var opts string
	switch carrier {
	case "ws":
		opts = "    network: ws\n    ws-opts:\n      path: /ws\n      headers:\n        " + key + ": " + value + "\n"
	case "http":
		opts = "    network: http\n    http-opts:\n      path: [/]\n      headers:\n        " + key + ": [" + value + "]\n"
	case "h2":
		opts = "    network: h2\n    h2-opts:\n      " + key + ": [" + value + "]\n      path: /h2\n"
	case "xhttp":
		opts = "    network: xhttp\n    xhttp-opts:\n      " + key + ": " + value + "\n      path: /x\n      mode: auto\n"
	}
	return "proxies:\n" +
		"  - name: B01-" + carrier + "-" + key + "\n" +
		"    type: vmess\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    uuid: " + manualUUID + "\n" +
		"    tls: true\n" +
		"    servername: example.com\n" +
		opts
}

func TestBatch2B01HostSpellingsRejected(t *testing.T) {
	// 4 carriers x 4 spellings, all carrying the empty-label host a..b.
	// Every cell must be rejected at import with the Host wording.
	for _, carrier := range []string{"ws", "http", "h2", "xhttp"} {
		for _, key := range []string{"Host", "host", "HOST", "hOsT"} {
			nodes, errs := ParseImport(b2ClashVMess(carrier, key, "a..b"))
			if len(nodes) != 0 || len(errs) == 0 {
				t.Errorf("carrier=%s key=%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error",
					carrier, key, len(nodes), errs)
				continue
			}
			if !strings.Contains(errs[0], "Host 格式错误") || !strings.Contains(errs[0], "a..b") {
				t.Errorf("carrier=%s key=%s: err = %q, want it to name the bad Host value", carrier, key, errs[0])
			}
		}
	}
}

func TestBatch2B01LegalHostStillImports(t *testing.T) {
	// Legal hosts under every spelling and carrier must still import.
	for _, carrier := range []string{"ws", "http", "h2", "xhttp"} {
		for _, key := range []string{"Host", "host"} {
			n := mustImportOne(t, b2ClashVMess(carrier, key, "example.com"))
			if got := n.Proxy.network(); got != carrier {
				t.Errorf("carrier=%s key=%s: network = %q", carrier, key, got)
			}
		}
	}
}

// TestBatch2B01TransportHostsEnumeration pins the check-side
// enumeration itself: importTransportHosts must return the bad value
// for every spelling x every carrier (the cross-enumeration the
// CLOSED-LIST C-05 registration rests on).
func TestBatch2B01TransportHostsEnumeration(t *testing.T) {
	cases := []struct {
		name string
		p    Proxy
	}{
		{"ws-opts Host", Proxy{"ws-opts": map[string]interface{}{"headers": map[string]interface{}{"Host": "a..b"}}}},
		{"ws-opts host", Proxy{"ws-opts": map[string]interface{}{"headers": map[string]interface{}{"host": "a..b"}}}},
		{"ws-opts HOST", Proxy{"ws-opts": map[string]interface{}{"headers": map[string]interface{}{"HOST": "a..b"}}}},
		{"http-opts Host", Proxy{"http-opts": map[string]interface{}{"headers": map[string]interface{}{"Host": []interface{}{"a..b"}}}}},
		{"http-opts host", Proxy{"http-opts": map[string]interface{}{"headers": map[string]interface{}{"host": []interface{}{"a..b"}}}}},
		{"http-opts HOST", Proxy{"http-opts": map[string]interface{}{"headers": map[string]interface{}{"HOST": []interface{}{"a..b"}}}}},
		{"h2-opts host", Proxy{"h2-opts": map[string]interface{}{"host": []interface{}{"a..b"}}}},
		{"h2-opts Host", Proxy{"h2-opts": map[string]interface{}{"Host": []interface{}{"a..b"}}}},
		{"h2-opts HOST", Proxy{"h2-opts": map[string]interface{}{"HOST": []interface{}{"a..b"}}}},
		{"xhttp-opts host", Proxy{"xhttp-opts": map[string]interface{}{"host": "a..b"}}},
		{"xhttp-opts Host", Proxy{"xhttp-opts": map[string]interface{}{"Host": "a..b"}}},
		{"xhttp-opts HOST", Proxy{"xhttp-opts": map[string]interface{}{"HOST": "a..b"}}},
	}
	for _, c := range cases {
		found := false
		for _, h := range importTransportHosts(c.p) {
			if h == "a..b" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: importTransportHosts = %v, want it to contain %q", c.name, importTransportHosts(c.p), "a..b")
		}
		if err := validateImportHostnames(c.p); err == nil {
			t.Errorf("%s: validateImportHostnames = nil, want rejection", c.name)
		}
	}
}

// TestBatch2B01PluginHostSpellings: the same case-escape mechanism in
// the adjacent plugin carriers (plugin-opts / obfs-opts): a capitalised
// Host key must be validated just like the transport carriers.
func TestBatch2B01PluginHostSpellings(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Proxy
	}{
		{"plugin-opts Host", Proxy{"plugin-opts": map[string]interface{}{"Host": "a..b"}}},
		{"plugin-opts HOST", Proxy{"plugin-opts": map[string]interface{}{"HOST": "a..b"}}},
		{"obfs-opts Host", Proxy{"obfs-opts": map[string]interface{}{"Host": "a..b"}}},
		{"shadow-tls Host", Proxy{"shadow-tls": map[string]interface{}{"Host": "a..b"}}},
	} {
		found := false
		for _, h := range importPluginHosts(tc.p) {
			if h == "a..b" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: importPluginHosts = %v, want it to contain %q", tc.name, importPluginHosts(tc.p), "a..b")
		}
	}
	// End to end: Clash SS with plugin-opts capital Host carrying a..b.
	yamlText := "proxies:\n" +
		"  - name: B01-plugin\n" +
		"    type: ss\n" +
		"    server: 203.0.113.50\n" +
		"    port: 8388\n" +
		"    cipher: aes-128-gcm\n" +
		"    password: b01plugin\n" +
		"    plugin: obfs\n" +
		"    plugin-opts:\n" +
		"      mode: http\n" +
		"      Host: a..b\n"
	nodes, errs := ParseImport(yamlText)
	if len(nodes) != 0 || len(errs) == 0 {
		t.Fatalf("plugin-opts Host a..b: nodes=%d errs=%v, want 0 nodes with an error", len(nodes), errs)
	}
	if !strings.Contains(errs[0], "Host 格式错误") {
		t.Errorf("plugin-opts Host a..b: err = %q, want Host wording", errs[0])
	}
}
