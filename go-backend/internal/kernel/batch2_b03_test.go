// Batch 2 of the FLMX 1.7.1 post-release full audit (baseline main @
// c2f45a6): regression tests for B-03 — Clash-imported SS plugin names
// kept their original case (OBFS, V2Ray-Plugin, ...) while the four
// render switches match exactly, so the node was silently dropped on
// sing-box / Surge / Loon / QX. Written red first.
package kernel

import (
	"testing"
)

func b2ClashSS(plugin string) string {
	return "proxies:\n" +
		"  - name: B03-" + plugin + "\n" +
		"    type: ss\n" +
		"    server: 203.0.113.50\n" +
		"    port: 8388\n" +
		"    cipher: aes-128-gcm\n" +
		"    password: b03plugin\n" +
		"    plugin: " + plugin + "\n" +
		"    plugin-opts:\n" +
		"      mode: http\n" +
		"      host: ob.example.net\n"
}

// b2FaceResults renders one endpoint on the four case-sensitive faces
// plus the URI face; each entry reports whether the node was emitted.
func b2FaceResults(e Endpoint) (singbox, surge, loon, qx, uri bool) {
	if _, ok := proxySingbox(e); ok {
		singbox = true
	}
	if _, ok := proxySurge(e); ok {
		surge = true
	}
	if _, ok := loonLine(e.Proxy.forEndpoint(e)); ok {
		loon = true
	}
	if _, ok := qxLine(e.Proxy.forEndpoint(e)); ok {
		qx = true
	}
	if _, ok := proxyURI(e); ok {
		uri = true
	}
	return
}

func TestBatch2B03PluginCaseNormalized(t *testing.T) {
	// Every spelling variant must behave exactly like its lowercase
	// control on all four case-sensitive faces (and the URI face).
	variants := []struct {
		plugin  string
		control string // lowercase canonical name
	}{
		{"OBFS", "obfs"},
		{"Obfs", "obfs"},
		{"obfs-local", "obfs"},
		{"simple-obfs", "obfs"},
		{"OBFS-LOCAL", "obfs"},
		{"V2Ray-Plugin", "v2ray-plugin"},
		{"V2RAY-PLUGIN", "v2ray-plugin"},
		{"v2ray-plugin", "v2ray-plugin"},
		{"Shadow-TLS", "shadow-tls"},
		{"SHADOW-TLS", "shadow-tls"},
		{"shadow-tls", "shadow-tls"},
	}
	for _, v := range variants {
		n := mustImportOne(t, b2ClashSS(v.plugin))
		if got := n.Proxy.str("plugin"); got != v.control {
			t.Errorf("plugin=%q: stored plugin = %q, want %q", v.plugin, got, v.control)
		}
		e := Endpoint{Name: "b03", Host: "203.0.113.50", Port: 8388, Direct: true, Proxy: n.Proxy}
		cn := mustImportOne(t, b2ClashSS(v.control))
		ce := Endpoint{Name: "b03", Host: "203.0.113.50", Port: 8388, Direct: true, Proxy: cn.Proxy}
		ws, wg, wl, wq, wu := b2FaceResults(e)
		cs, cg, cl, cq, cu := b2FaceResults(ce)
		if ws != cs || wg != cg || wl != cl || wq != cq || wu != cu {
			t.Errorf("plugin=%q: faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, control %q = %v/%v/%v/%v/%v",
				v.plugin, ws, wg, wl, wq, wu, v.control, cs, cg, cl, cq, cu)
		}
		// obfs variants must render on all four faces (the H A2-03 cell).
		if v.control == "obfs" && !(ws && wg && wl && wq && wu) {
			t.Errorf("plugin=%q: obfs node must render on all faces, got singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v",
				v.plugin, ws, wg, wl, wq, wu)
		}
	}
}

// TestBatch2B03StoredPluginCaseHealedAtRender pins the render-side
// healing: a row stored before the import fix (plugin "OBFS") must
// still render on every face that renders its lowercase control.
func TestBatch2B03StoredPluginCaseHealedAtRender(t *testing.T) {
	stored := Proxy{
		"type": "ss", "server": "203.0.113.50", "port": 8388,
		"cipher": "aes-128-gcm", "password": "b03stored", "plugin": "OBFS",
		"plugin-opts": map[string]interface{}{"mode": "http", "host": "ob.example.net"},
	}
	e := Endpoint{Name: "b03-stored", Host: "203.0.113.50", Port: 8388, Direct: true, Proxy: stored}
	if got := e.Proxy.forEndpoint(e).str("plugin"); got != "obfs" {
		t.Fatalf("forEndpoint must lowercase the stored plugin, got %q", got)
	}
	sb, sg, ln, qx, uri := b2FaceResults(e)
	if !(sb && sg && ln && qx && uri) {
		t.Errorf("stored OBFS node faces singbox/surge/loon/qx/uri = %v/%v/%v/%v/%v, want all true", sb, sg, ln, qx, uri)
	}
}
