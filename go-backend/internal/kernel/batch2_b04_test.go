// Batch 2 of the FLMX 1.7.1 post-release full audit (baseline main @
// c2f45a6): regression tests for B-04 — splitURI discarded the
// url.ParseQuery error, so a link with illegal percent-encoding
// (security=%zz, sni=%zz) silently lost the security/SNI pair and was
// imported as a downgraded node. Written red first.
package kernel

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBatch2B04SplitURIQueryError(t *testing.T) {
	for _, link := range []string{
		// security=%zz: the security pair must not silently drop.
		"vless://" + manualUUID + "@example.com:443?security=%zz#BadQ",
		// sni=%zz: the SNI pair must not silently drop.
		"vless://" + manualUUID + "@example.com:443?security=tls&sni=%zz#BadQ2",
		// trojan side of the same shape.
		"trojan://pw@example.com:443?security=tls&sni=%zz#BadQ3",
	} {
		nodes, errs := ParseImport(link)
		if len(nodes) != 0 || len(errs) == 0 {
			t.Errorf("%s: ParseImport = %d nodes, errs=%v, want 0 nodes with an error", link, len(nodes), errs)
			continue
		}
		if !strings.Contains(errs[0], "参数解析失败") {
			t.Errorf("%s: err = %q, want it to name the query parse failure", link, errs[0])
		}
	}
	// Legal percent-encoding must still parse.
	n := mustImportOne(t, "vless://"+manualUUID+"@example.com:443?security=tls&sni=example.com&type=ws&path=%2Fws&host=example.com#Good")
	if !n.Proxy.flag("tls") || n.Proxy.serverName() != "example.com" {
		t.Errorf("legal percent-encoded link lost TLS/SNI: %v", n.Proxy)
	}
	n = mustImportOne(t, "vless://"+manualUUID+"@example.com:443?security=tls&sni=example.com&type=tcp#Good%20Name")
	if n.Name != "Good Name" {
		t.Errorf("legal percent-encoded name = %q, want %q", n.Name, "Good Name")
	}
	// Legacy ss://base64(...) form with a malformed query: the query
	// error must win over the legacy fallback, not be dropped by it.
	legacy := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:b04legacy@203.0.113.50:8388"))
	nodes, errs := ParseImport("ss://" + legacy + "?plugin=%zz#BadLegacy")
	if len(nodes) != 0 || len(errs) == 0 {
		t.Fatalf("legacy ss with bad query: nodes=%d errs=%v, want 0 nodes with an error", len(nodes), errs)
	}
	if !strings.Contains(errs[0], "参数解析失败") {
		t.Errorf("legacy ss with bad query: err = %q, want query parse failure", errs[0])
	}
	// The same legacy form with a well-formed query still imports.
	n = mustImportOne(t, "ss://"+legacy+"#GoodLegacy")
	if n.Proxy.str("cipher") != "aes-128-gcm" {
		t.Errorf("legacy ss cipher = %q, want aes-128-gcm", n.Proxy.str("cipher"))
	}
}
