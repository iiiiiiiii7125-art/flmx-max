// R4-C-01 (P3, batch 2 of the FLMX 1.7.1 post-release full audit,
// re-audit round 4, baseline main @ 583c949): SS plugin name / option-key
// case variants were rejected or silently dropped parameters on the URI
// and sing-box import faces (B-03 coverage gap). applySSPlugin matched
// the plugin name exactly and built the option map with the raw key
// spelling, while the B-03 healing in finishProxy runs after parsing
// and is unreachable for these two call sites. Written red first.
package kernel

import (
	"strings"
	"testing"
)

func r4c01SSURI(plugin string) string {
	// aes-128-gcm:pw, RawURLEncoding (same shape as r10b1SSUserinfo).
	return "ss://YWVzLTEyOC1nY206cHc@203.0.113.10:8388/?plugin=" + plugin + "#x"
}

func r4c01SingboxSS(plugin, pluginOpts string) string {
	return `{"outbounds":[{"type":"shadowsocks","tag":"x","server":"203.0.113.10","server_port":8388,` +
		`"method":"aes-128-gcm","password":"pw","plugin":"` + plugin + `","plugin_opts":"` + pluginOpts + `"}]}`
}

// Face (a): an uppercase plugin name must be accepted (folded to the
// canonical lowercase name) instead of rejected as unsupported.
func TestR4C01SSPluginNameCaseFolded(t *testing.T) {
	variants := []struct {
		spec   string // plugin= value (URL-escaped)
		plugin string // canonical stored plugin name
		mode   string
		host   string
	}{
		{"OBFS-LOCAL%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com", "obfs", "http", "bing.com"},
		{"Obfs-Local%3Bobfs%3Dtls%3Bobfs-host%3Dbing.com", "obfs", "tls", "bing.com"},
		{"SIMPLE-OBFS%3Bobfs%3Dhttp%3Bobfs-host%3Dbing.com", "obfs", "http", "bing.com"},
		{"V2RAY-PLUGIN%3Bhost%3Dbing.com%3Bpath%3D%2Fws%3Btls", "v2ray-plugin", "websocket", "bing.com"},
		{"SHADOW-TLS%3Bhost%3Dbing.com%3Bpassword%3Dpw", "shadow-tls", "", "bing.com"},
	}
	for _, v := range variants {
		n := mustImportOne(t, r4c01SSURI(v.spec))
		if got := n.Proxy.str("plugin"); got != v.plugin {
			t.Errorf("plugin spec %q: stored plugin = %q, want %q", v.spec, got, v.plugin)
		}
		po := n.Proxy.sub("plugin-opts")
		if v.mode != "" {
			if got := po.str("mode"); got != v.mode {
				t.Errorf("plugin spec %q: plugin-opts mode = %q, want %q", v.spec, got, v.mode)
			}
		}
		if got := po.str("host"); got != v.host {
			t.Errorf("plugin spec %q: plugin-opts host = %q, want %q", v.spec, got, v.host)
		}
	}
}

// Face (b): uppercase option keys must be folded, not silently dropped.
func TestR4C01SSPluginOptionKeysCaseFolded(t *testing.T) {
	cases := []struct {
		spec   string
		plugin string
		mode   string
		host   string
		path   string
	}{
		{"obfs-local%3BOBFS%3Dhttp%3BOBFS-HOST%3Dbing.com", "obfs", "http", "bing.com", ""},
		{"obfs-local%3BObfs%3Dtls%3BObfs-Host%3Dbing.com", "obfs", "tls", "bing.com", ""},
		{"v2ray-plugin%3BHOST%3Dbing.com%3BPATH%3D%2Fws%3BTLS", "v2ray-plugin", "websocket", "bing.com", "/ws"},
		{"shadow-tls%3BHOST%3Dbing.com%3BPASSWORD%3Dpw%3BVERSION%3D2", "shadow-tls", "", "bing.com", ""},
	}
	for _, c := range cases {
		n := mustImportOne(t, r4c01SSURI(c.spec))
		if got := n.Proxy.str("plugin"); got != c.plugin {
			t.Errorf("plugin spec %q: stored plugin = %q, want %q", c.spec, got, c.plugin)
		}
		po := n.Proxy.sub("plugin-opts")
		if c.mode != "" {
			if got := po.str("mode"); got != c.mode {
				t.Errorf("plugin spec %q: plugin-opts mode = %q, want %q", c.spec, got, c.mode)
			}
		}
		if got := po.str("host"); got != c.host {
			t.Errorf("plugin spec %q: plugin-opts host = %q, want %q", c.spec, got, c.host)
		}
		if c.path != "" {
			if got := po.str("path"); got != c.path {
				t.Errorf("plugin spec %q: plugin-opts path = %q, want %q", c.spec, got, c.path)
			}
		}
		if c.plugin == "shadow-tls" {
			if got := po.num("version"); got != 2 {
				t.Errorf("plugin spec %q: plugin-opts version = %v, want 2", c.spec, got)
			}
			if got := po.str("password"); got != "pw" {
				t.Errorf("plugin spec %q: plugin-opts password = %q, want %q", c.spec, got, "pw")
			}
		}
	}
	// The obfs URI face must render the recovered parameters back:
	// before the fix it emitted "obfs-local;obfs=" (empty mode).
	n := mustImportOne(t, r4c01SSURI("obfs-local%3BOBFS%3Dhttp%3BOBFS-HOST%3Dbing.com"))
	e := Endpoint{Name: "r4c01", Host: "203.0.113.10", Port: 8388, Direct: true, Proxy: n.Proxy}
	u, ok := proxyURI(e)
	if !ok {
		t.Fatal("recovered obfs node must render on the URI face")
	}
	if !strings.Contains(u, "obfs%3Dhttp") || !strings.Contains(u, "obfs-host%3Dbing.com") {
		t.Errorf("URI face must carry obfs=http and obfs-host=bing.com, got %q", u)
	}
}

// sing-box face: an uppercase plugin name is rejected the same way as
// the URI face before the fix; both call sites share applySSPlugin.
func TestR4C01SSPluginSingboxNameCaseFolded(t *testing.T) {
	n := mustImportOne(t, r4c01SingboxSS("OBFS-LOCAL", "obfs=http;obfs-host=bing.com"))
	if got := n.Proxy.str("plugin"); got != "obfs" {
		t.Errorf("sing-box plugin OBFS-LOCAL: stored plugin = %q, want %q", got, "obfs")
	}
	po := n.Proxy.sub("plugin-opts")
	if got := po.str("mode"); got != "http" {
		t.Errorf("sing-box plugin OBFS-LOCAL: plugin-opts mode = %q, want %q", got, "http")
	}
	if got := po.str("host"); got != "bing.com" {
		t.Errorf("sing-box plugin OBFS-LOCAL: plugin-opts host = %q, want %q", got, "bing.com")
	}
	// Uppercase option keys on the sing-box face fold the same way.
	n = mustImportOne(t, r4c01SingboxSS("obfs-local", "OBFS=http;OBFS-HOST=bing.com"))
	if got := n.Proxy.sub("plugin-opts").str("mode"); got != "http" {
		t.Errorf("sing-box plugin_opts OBFS: plugin-opts mode = %q, want %q", got, "http")
	}
	if got := n.Proxy.sub("plugin-opts").str("host"); got != "bing.com" {
		t.Errorf("sing-box plugin_opts OBFS-HOST: plugin-opts host = %q, want %q", got, "bing.com")
	}
}

// Negative controls: the acceptance set must not expand. An unknown
// plugin name is still rejected by name (uppercase or lowercase), and
// an illegal option value is still disposed of exactly as its
// lowercase control is (the A2-03 mode gate, via finishProxy).
func TestR4C01SSPluginUnknownStillRejected(t *testing.T) {
	for _, spec := range []string{
		"FOO-PLUGIN%3Bx%3Dy",
		"foo-plugin%3Bx%3Dy",
		"Obfs-Unknown%3Bobfs%3Dhttp",
	} {
		_, errs := ParseImport(r4c01SSURI(spec))
		if len(errs) == 0 {
			t.Errorf("plugin spec %q must be rejected, got no error", spec)
			continue
		}
		if !strings.Contains(errs[0], "暂不支持 ss 插件") {
			t.Errorf("plugin spec %q: err = %q, want it to name the unsupported plugin", spec, errs[0])
		}
	}
	// sing-box face: same rejection for an unknown uppercase name.
	_, errs := ParseImport(r4c01SingboxSS("FOO-PLUGIN", "x=y"))
	if len(errs) == 0 || !strings.Contains(errs[0], "暂不支持 ss 插件") {
		t.Errorf("sing-box plugin FOO-PLUGIN must be rejected by name, errs=%v", errs)
	}
	// Illegal obfs mode value: uppercase-key spelling must be disposed
	// of exactly like its lowercase control (rejected, naming mode).
	_, upperErrs := ParseImport(r4c01SSURI("obfs-local%3BOBFS%3Dbogus%3BOBFS-HOST%3Dbing.com"))
	_, lowerErrs := ParseImport(r4c01SSURI("obfs-local%3Bobfs%3Dbogus%3Bobfs-host%3Dbing.com"))
	if len(upperErrs) == 0 || len(lowerErrs) == 0 {
		t.Fatalf("illegal obfs mode must be rejected in both spellings, upper=%v lower=%v", upperErrs, lowerErrs)
	}
	if !strings.Contains(upperErrs[0], "mode") || !strings.Contains(lowerErrs[0], "mode") {
		t.Errorf("illegal obfs mode must be rejected naming mode, upper=%v lower=%v", upperErrs, lowerErrs)
	}
}
