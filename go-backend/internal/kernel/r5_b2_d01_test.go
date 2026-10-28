package kernel

import (
	"fmt"
	"strings"
	"testing"
)

// R5-D-01 (r5 verdict §1, D+L merged): sing-box import face
// transport.headers read point (proxy_parse.go:1400) used to read only
// the exact spelling "Host" in string form:
//   - non-canonical spellings (host/HOST/hOsT) were silently dropped
//     (node imported with no Host at all);
//   - list-form values were fmt.Sprint-stringified to "[...]" and then
//     rejected as malformed, and the rejection message named the
//     stringified form instead of the original value.
// The fix routes the read through firstHeaderHostValue (canonical Host
// first, then host, then EqualFold fallback, all list-aware); the
// transport-level direct "host" fallback is unchanged.
const r5B2D01UUID = "11111111-1111-4111-8111-111111111111"

func r5B2D01SingboxWS(protocol, transport string) string {
	return fmt.Sprintf(`{"outbounds":[{"type":%q,"tag":"x","server":"1.2.3.4","server_port":443,"uuid":%q,"transport":%s}]}`,
		protocol, r5B2D01UUID, transport)
}

func TestR5B2D01SingboxHeadersSpellingAndListForm(t *testing.T) {
	cases := []struct {
		name          string
		cfg           string
		wantNodes     int
		wantHost      string
		wantErrSub    string
		wantErrNotSub string
	}{
		// D-domain spelling grid (string form).
		{name: "canonical-Host-string", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"Host":"example.com"}}`), wantNodes: 1, wantHost: "example.com"},
		{name: "lowercase-host-string", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"host":"example.com"}}`), wantNodes: 1, wantHost: "example.com"},
		{name: "upper-HOST-string", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"HOST":"example.com"}}`), wantNodes: 1, wantHost: "example.com"},
		{name: "mixed-hOsT-string", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"hOsT":"example.com"}}`), wantNodes: 1, wantHost: "example.com"},
		// L-domain value-form grid.
		{name: "list-Host-single", cfg: r5B2D01SingboxWS("vless", `{"type":"ws","path":"/ws","headers":{"Host":["sb5.example.net"]}}`), wantNodes: 1, wantHost: "sb5.example.net"},
		{name: "list-Host-multi-first-wins", cfg: r5B2D01SingboxWS("vless", `{"type":"ws","path":"/ws","headers":{"Host":["first.example.com","second.example.com"]}}`), wantNodes: 1, wantHost: "first.example.com"},
		{name: "list-Host-bad-rejected-names-original", cfg: r5B2D01SingboxWS("vless", `{"type":"ws","path":"/ws","headers":{"Host":["ww..xx"]}}`), wantNodes: 0, wantErrSub: "ww..xx", wantErrNotSub: "[ww..xx]"},
		// Bad value in a non-canonical spelling must be rejected, not
		// silently dropped (acceptance set does not expand: bad values
		// are still refused once they reach validation).
		{name: "lowercase-host-bad-rejected", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"host":"w..w"}}`), wantNodes: 0, wantErrSub: "w..w"},
		// Canonical priority and fallback guards (must not regress).
		{name: "canonical-beats-lowercase", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","headers":{"Host":"canon.example.com","host":"lower.example.com"}}`), wantNodes: 1, wantHost: "canon.example.com"},
		{name: "headers-beats-transport-direct", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","host":"direct.example.net","headers":{"Host":"hdr.example.com"}}`), wantNodes: 1, wantHost: "hdr.example.com"},
		{name: "transport-direct-list-fallback", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","host":["fallback.example.net"]}`), wantNodes: 1, wantHost: "fallback.example.net"},
		{name: "transport-direct-string-fallback", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws","host":"direct.example.net"}`), wantNodes: 1, wantHost: "direct.example.net"},
		{name: "no-host-at-all", cfg: r5B2D01SingboxWS("vmess", `{"type":"ws","path":"/ws"}`), wantNodes: 1, wantHost: ""},
		// Type independence (D used VMess, L used VLESS) and direct-host
		// list guards for h2/xhttp (verdict §8 acceptance grid).
		{name: "vless-lowercase-host-string", cfg: r5B2D01SingboxWS("vless", `{"type":"ws","path":"/ws","headers":{"host":"example.com"}}`), wantNodes: 1, wantHost: "example.com"},
		{name: "h2-transport-direct-list", cfg: r5B2D01SingboxWS("vmess", `{"type":"http","path":"/h2","host":["h2.example.net"]}`), wantNodes: 1, wantHost: "h2.example.net"},
		{name: "xhttp-transport-direct-list", cfg: r5B2D01SingboxWS("vless", `{"type":"xhttp","path":"/x","host":["xh.example.net"]}`), wantNodes: 1, wantHost: "xh.example.net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, errs := parseSingboxProfile(tc.cfg)
			if len(nodes) != tc.wantNodes {
				t.Fatalf("nodes=%d errs=%v, want %d nodes", len(nodes), errs, tc.wantNodes)
			}
			if tc.wantNodes == 0 {
				joined := strings.Join(errs, " | ")
				if tc.wantErrSub != "" && !strings.Contains(joined, tc.wantErrSub) {
					t.Fatalf("errs=%v, want error naming %q", errs, tc.wantErrSub)
				}
				if tc.wantErrNotSub != "" && strings.Contains(joined, tc.wantErrNotSub) {
					t.Fatalf("errs=%v, must not name stringified form %q", errs, tc.wantErrNotSub)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("errs: %v", errs)
			}
			host, _, _, _ := transportFields(nodes[0].Proxy)
			if host != tc.wantHost {
				t.Fatalf("transportFields host=%q, want %q (proxy=%v)", host, tc.wantHost, nodes[0].Proxy)
			}
		})
	}
}
