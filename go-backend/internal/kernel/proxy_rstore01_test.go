package kernel

import (
	"sort"
	"strings"
	"testing"
)

// R-STORE-01 (P3, storage-face mechanism gap): non-canonical host key
// spellings still land verbatim in stored rows. Rendering is already
// deterministic (firstHeaderHostValue) and bad values are already
// rejected at import, so there is no user-side poisoning — but storage
// is the last mechanism gap of the hostname family: a stored row may
// still carry loser host keys, and any future exact read would break
// again. Written red first: dual-key rows must store ONLY the winner
// spelling; before the fix the loser key is still present (FAIL).
// R-STORE-01 permanent regression gate (user rule 2026-10-29): this
// test must stay committed.
func rstore01ClashVMessDualHeaders() string {
	return "proxies:\n" +
		"  - name: RSTORE01-dual\n" +
		"    type: vmess\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    uuid: 55555555-5555-5555-5555-555555555555\n" +
		"    tls: true\n" +
		"    servername: example.com\n" +
		"    network: ws\n" +
		"    ws-opts:\n" +
		"      path: /ws\n" +
		"      headers:\n" +
		"        hOsT: aaa.example.com\n" +
		"        HOst: bbb.example.com\n"
}

func rstore01ClashVMessHeaders(keys map[string]string) string {
	var b strings.Builder
	b.WriteString("proxies:\n" +
		"  - name: RSTORE01-h\n" +
		"    type: vmess\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    uuid: 55555555-5555-5555-5555-555555555555\n" +
		"    tls: true\n" +
		"    servername: example.com\n" +
		"    network: ws\n" +
		"    ws-opts:\n" +
		"      path: /ws\n" +
		"      headers:\n")
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		b.WriteString("        " + k + ": " + keys[k] + "\n")
	}
	return b.String()
}

func rstore01ClashSnellDualShadowTLS() string {
	return "proxies:\n" +
		"  - name: RSTORE01-snell\n" +
		"    type: snell\n" +
		"    server: 203.0.113.10\n" +
		"    port: 443\n" +
		"    psk: rstorepsk123456\n" +
		"    version: 4\n" +
		"    shadow-tls:\n" +
		"      password: rstoretlspw1\n" +
		"      version: 3\n" +
		"      hOsT: aaa.example.com\n" +
		"      HOst: bbb.example.com\n"
}

// rstore01HostKeys returns the host-spelling keys present in m, sorted.
func rstore01HostKeys(m Proxy) []string {
	var out []string
	for k := range m {
		if strings.EqualFold(k, "host") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func rstore01MustImportOne(t *testing.T, text string) Proxy {
	t.Helper()
	nodes, errs := ParseImport(text)
	if len(nodes) != 1 || len(errs) != 0 {
		t.Fatalf("import must pass: nodes=%d errs=%v", len(nodes), errs)
	}
	return nodes[0].Proxy
}

func TestRStore01DualNonCanonicalKeysNormalized(t *testing.T) {
	// Byte order: "HOst" < "hOsT" ('H' < 'h'), so HOst wins.
	p := rstore01MustImportOne(t, rstore01ClashVMessDualHeaders())
	hdr := p.sub("ws-opts").sub("headers")
	if got := rstore01HostKeys(hdr); len(got) != 1 || got[0] != "HOst" {
		t.Fatalf("stored headers host keys = %v, want [HOst]", got)
	}
	if got := hdr.str("HOst"); got != "bbb.example.com" {
		t.Fatalf("stored winner value = %q, want bbb.example.com", got)
	}
	if got := firstHeaderHostValue(hdr); got != "bbb.example.com" {
		t.Fatalf("rendered winner = %q, want bbb.example.com", got)
	}
}

func TestRStore01CanonicalPriorityKept(t *testing.T) {
	// Host present beats host, whatever the values.
	p := rstore01MustImportOne(t, rstore01ClashVMessHeaders(map[string]string{
		"Host": "aaa.example.com", "host": "bbb.example.com",
	}))
	hdr := p.sub("ws-opts").sub("headers")
	if got := rstore01HostKeys(hdr); len(got) != 1 || got[0] != "Host" {
		t.Fatalf("stored headers host keys = %v, want [Host]", got)
	}
	if got := firstHeaderHostValue(hdr); got != "aaa.example.com" {
		t.Fatalf("rendered winner = %q, want aaa.example.com", got)
	}
}

func TestRStore01HostBeatsNonCanonical(t *testing.T) {
	p := rstore01MustImportOne(t, rstore01ClashVMessHeaders(map[string]string{
		"host": "bbb.example.com", "hOsT": "aaa.example.com",
	}))
	hdr := p.sub("ws-opts").sub("headers")
	if got := rstore01HostKeys(hdr); len(got) != 1 || got[0] != "host" {
		t.Fatalf("stored headers host keys = %v, want [host]", got)
	}
	if got := firstHeaderHostValue(hdr); got != "bbb.example.com" {
		t.Fatalf("rendered winner = %q, want bbb.example.com", got)
	}
}

func TestRStore01SingleKeyUnchanged(t *testing.T) {
	// A lone non-canonical key IS the winner spelling; normalization
	// is a no-op for it (control: passes before and after the fix).
	p := rstore01MustImportOne(t, rstore01ClashVMessHeaders(map[string]string{
		"hOsT": "aaa.example.com",
	}))
	hdr := p.sub("ws-opts").sub("headers")
	if got := rstore01HostKeys(hdr); len(got) != 1 || got[0] != "hOsT" {
		t.Fatalf("stored headers host keys = %v, want [hOsT]", got)
	}
}

func TestRStore01ListValueShapeKept(t *testing.T) {
	// Winner list value stays a list, verbatim, first element wins.
	p := rstore01MustImportOne(t, "proxies:\n"+
		"  - name: RSTORE01-list\n"+
		"    type: vmess\n"+
		"    server: 203.0.113.10\n"+
		"    port: 443\n"+
		"    uuid: 55555555-5555-5555-5555-555555555555\n"+
		"    tls: true\n"+
		"    servername: example.com\n"+
		"    network: ws\n"+
		"    ws-opts:\n"+
		"      path: /ws\n"+
		"      headers:\n"+
		"        HOst: [bbb1.example.com, bbb2.example.com]\n"+
		"        hOsT: aaa.example.com\n")
	hdr := p.sub("ws-opts").sub("headers")
	if got := rstore01HostKeys(hdr); len(got) != 1 || got[0] != "HOst" {
		t.Fatalf("stored headers host keys = %v, want [HOst]", got)
	}
	lst, ok := hdr["HOst"].([]interface{})
	if !ok || len(lst) != 2 {
		t.Fatalf("stored winner value = %#v, want 2-element list verbatim", hdr["HOst"])
	}
	if got := firstHeaderHostValue(hdr); got != "bbb1.example.com" {
		t.Fatalf("rendered winner = %q, want bbb1.example.com", got)
	}
}

func TestRStore01PluginShadowTLSNormalized(t *testing.T) {
	p := rstore01MustImportOne(t, rstore01ClashSnellDualShadowTLS())
	st := p.sub("shadow-tls")
	if got := rstore01HostKeys(st); len(got) != 1 || got[0] != "HOst" {
		t.Fatalf("stored shadow-tls host keys = %v, want [HOst]", got)
	}
	clashHost, singboxSNI, surgeLine, clashOK, singboxOK, surgeOK := r6b01RenderFaces(t, "RSTORE01-snell", p)
	if !clashOK || clashHost != "bbb.example.com" {
		t.Fatalf("clash face host = %q (ok=%v), want bbb.example.com", clashHost, clashOK)
	}
	if !singboxOK || singboxSNI != "bbb.example.com" {
		t.Fatalf("sing-box face server_name = %q (ok=%v), want bbb.example.com", singboxSNI, singboxOK)
	}
	if !surgeOK || !strings.Contains(surgeLine, "shadow-tls-sni=bbb.example.com") {
		t.Fatalf("surge face must carry shadow-tls-sni=bbb.example.com: ok=%v line=%q", surgeOK, surgeLine)
	}
}

func TestRStore01BadValueStillRejected(t *testing.T) {
	// Normalization runs only AFTER validation passes: bad values
	// must still be rejected at import, naming the value.
	nodes, errs := ParseImport(rstore01ClashVMessHeaders(map[string]string{
		"hOsT": "x..x",
	}))
	if len(nodes) != 0 || len(errs) == 0 {
		t.Fatalf("bad value must be rejected: nodes=%d errs=%v", len(nodes), errs)
	}
	if !strings.Contains(errs[0], "x..x") {
		t.Fatalf("rejection must name the bad value: %q", errs[0])
	}
}
