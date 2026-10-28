package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// R6-B-01 red probe (fix batch 1): Snell + ShadowTLS stored shadow-tls
// submap host under a non-canonical spelling must render on the Clash
// obfs-opts face and the sing-box detour face, not silently drop to "".
// Canonical spelling, bad-value rejection, and the Surge face are controls.
// R6-B-01 permanent regression gate (user rule 2026-10-29): this test must
// stay committed. Any render-side exact read of a host-carrying submap key
// that bypasses firstHeaderHostValue fails here across all case variants.
func r6b01ClashText(name, key, val string) string {
	return fmt.Sprintf(`proxies:
  - name: %q
    type: snell
    server: 203.0.113.10
    port: 443
    psk: r6psk123456
    version: 4
    shadow-tls:
      password: r6tlspw
      version: 3
      %s: %s
`, name, key, val)
}

func r6b01RenderFaces(t *testing.T, name string, proxy Proxy) (clashHost, singboxSNI, surgeLine string, clashOK, singboxOK, surgeOK bool) {
	t.Helper()
	e := Endpoint{Name: name, Host: "203.0.113.10", Port: 443, Proxy: proxy}
	if node, ok := proxyClash(e); ok {
		clashOK = true
		m := yamlToMap(node)
		if oo, _ := m["obfs-opts"].(map[string]interface{}); oo != nil {
			clashHost, _ = oo["host"].(string)
		}
	}
	if outs, ok := proxySingbox(e); ok {
		for _, o := range outs {
			if o["type"] == "shadowtls" {
				singboxOK = true
				if tls, _ := o["tls"].(map[string]interface{}); tls != nil {
					singboxSNI, _ = tls["server_name"].(string)
				}
			}
		}
	}
	if line, ok := proxySurge(Endpoint{Name: name, Host: "203.0.113.10", Port: 443, Proxy: proxy, Direct: true}); ok {
		surgeOK = true
		surgeLine = line
	}
	return
}

func TestR6B01_SnellShadowTLSSpelling(t *testing.T) {
	const legal = "r6b01.example.net"
	for _, spelling := range []string{"host", "Host", "HOST", "hOsT"} {
		name := "r6-b01-" + spelling
		nodes, errs := ParseImport(r6b01ClashText(name, spelling, legal))
		if len(nodes) != 1 || len(errs) != 0 {
			t.Fatalf("%s: import must pass: nodes=%d errs=%v", spelling, len(nodes), errs)
		}
		st := nodes[0].Proxy.sub("shadow-tls")
		if st.str(spelling) == "" && len(st.strs(spelling)) == 0 {
			t.Fatalf("%s: stored shadow-tls must keep spelling key: %v", spelling, st)
		}
		clashHost, singboxSNI, surgeLine, clashOK, singboxOK, surgeOK := r6b01RenderFaces(t, name, nodes[0].Proxy)
		if !clashOK {
			t.Fatalf("%s: clash face must render", spelling)
		}
		if clashHost != legal {
			t.Errorf("%s: clash obfs-opts host = %q, want %q", spelling, clashHost, legal)
		}
		if !singboxOK {
			t.Fatalf("%s: sing-box shadowtls detour must render", spelling)
		}
		if singboxSNI != legal {
			t.Errorf("%s: sing-box detour server_name = %q, want %q", spelling, singboxSNI, legal)
		}
		if !surgeOK || !strings.Contains(surgeLine, "shadow-tls-sni="+legal) {
			t.Errorf("%s: surge face must carry shadow-tls-sni=%s: ok=%v line=%q", spelling, legal, surgeOK, surgeLine)
		}
	}

	// List value shape: first element wins on every face.
	listText := `proxies:
  - name: "r6-b01-list"
    type: snell
    server: 203.0.113.10
    port: 443
    psk: r6psk123456
    version: 4
    shadow-tls:
      password: r6tlspw
      version: 3
      HOST:
        - r6b01-first.example.net
        - r6b01-second.example.net
`
	nodes, errs := ParseImport(listText)
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("list shape: import must pass: nodes=%d errs=%v", len(nodes), errs)
	}
	clashHost, singboxSNI, surgeLine, _, _, surgeOK := r6b01RenderFaces(t, "r6-b01-list", nodes[0].Proxy)
	if clashHost != "r6b01-first.example.net" {
		t.Errorf("list shape: clash host = %q, want first element", clashHost)
	}
	if singboxSNI != "r6b01-first.example.net" {
		t.Errorf("list shape: sing-box server_name = %q, want first element", singboxSNI)
	}
	if !surgeOK || !strings.Contains(surgeLine, "shadow-tls-sni=r6b01-first.example.net") {
		t.Errorf("list shape: surge line = %q", surgeLine)
	}

	// Stored-row source (backup-restore equivalent: the render read points
	// see the same stored Proxy whatever the source): direct Proxy with a
	// non-canonical shadow-tls key must render on both defect faces.
	direct := Proxy{"type": "snell", "psk": "r6psk123456", "version": 4,
		"shadow-tls": map[string]interface{}{"password": "r6tlspw", "version": 3, "HOST": legal}}
	clashHost, singboxSNI, _, _, _, _ = r6b01RenderFaces(t, "r6-b01-direct", direct)
	if clashHost != legal {
		t.Errorf("direct stored row: clash host = %q, want %q", clashHost, legal)
	}
	if singboxSNI != legal {
		t.Errorf("direct stored row: sing-box server_name = %q, want %q", singboxSNI, legal)
	}
}

func TestR6B01_SnellShadowTLSBadValueRejected(t *testing.T) {
	for _, spelling := range []string{"host", "Host", "HOST", "hOsT"} {
		nodes, _ := ParseImport(r6b01ClashText("r6-b01-bad-"+spelling, spelling, "x..x"))
		if len(nodes) != 0 {
			t.Errorf("%s: bad shadow-tls host must be rejected at import, got %d node(s)", spelling, len(nodes))
		}
	}
}

// TestR6B01_AllCaseVariants is the mechanism-level gate for R6-B-01: every
// case permutation of the key "host" (2^4 = 16 variants) x value shape
// (string, list-first-element) x render face (Clash, sing-box, Surge) must
// yield the legal host. A future render-side exact read that bypasses
// firstHeaderHostValue fails here for the non-canonical variants, instead
// of waiting for the next audit round to rediscover it.
func TestR6B01_AllCaseVariants(t *testing.T) {
	const legal = "r6b01-var.example.net"
	letters := []byte("host")
	var variants []string
	for mask := 0; mask < 16; mask++ {
		b := make([]byte, 4)
		for i, c := range letters {
			if mask&(1<<i) != 0 {
				b[i] = c - 'a' + 'A'
			} else {
				b[i] = c
			}
		}
		variants = append(variants, string(b))
	}
	if len(variants) != 16 {
		t.Fatalf("variant set must cover all 16 case permutations, got %d", len(variants))
	}
	for _, spelling := range variants {
		nodes, errs := ParseImport(r6b01ClashText("r6-b01-var-"+spelling, spelling, legal))
		if len(nodes) != 1 || len(errs) != 0 {
			t.Fatalf("%s: import must pass: nodes=%d errs=%v", spelling, len(nodes), errs)
		}
		clashHost, singboxSNI, surgeLine, _, _, surgeOK := r6b01RenderFaces(t, "r6-b01-var-"+spelling, nodes[0].Proxy)
		if clashHost != legal {
			t.Errorf("%s: clash host = %q, want %q", spelling, clashHost, legal)
		}
		if singboxSNI != legal {
			t.Errorf("%s: sing-box server_name = %q, want %q", spelling, singboxSNI, legal)
		}
		if !surgeOK || !strings.Contains(surgeLine, "shadow-tls-sni="+legal) {
			t.Errorf("%s: surge line = %q", spelling, surgeLine)
		}
		// List shape for the same spelling: first element wins.
		listText := fmt.Sprintf(`proxies:
  - name: "r6-b01-varlist-%s"
    type: snell
    server: 203.0.113.10
    port: 443
    psk: r6psk123456
    version: 4
    shadow-tls:
      password: r6tlspw
      version: 3
      %s:
        - r6b01-first.example.net
        - r6b01-second.example.net
`, spelling, spelling)
		nodes, errs = ParseImport(listText)
		if len(nodes) != 1 || len(errs) != 0 {
			t.Fatalf("%s list: import must pass: nodes=%d errs=%v", spelling, len(nodes), errs)
		}
		clashHost, singboxSNI, _, _, _, _ = r6b01RenderFaces(t, "r6-b01-varlist-"+spelling, nodes[0].Proxy)
		if clashHost != "r6b01-first.example.net" || singboxSNI != "r6b01-first.example.net" {
			t.Errorf("%s list: clash=%q sing-box=%q, want first element", spelling, clashHost, singboxSNI)
		}
	}
}
