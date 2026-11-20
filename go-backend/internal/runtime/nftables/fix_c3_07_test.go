package nftables

import (
	"strings"
	"testing"
)

// R18-C3-07 (a): the R-B01-01 order rule — L3 match (ip daddr)
// before L4 match (tcp/udp dport) — must be asserted on the
// rendered string itself, in every environment. The real `nft -c`
// leg in renderer_nftcheck_test.go skips entirely where there is
// no netlink access (sandbox, CI containers), so a string-only
// suite is the only half of that gate that can ever run there; a
// regression that only the string assertions catch must fail here,
// not pass silently as a skip.
func TestRenderTableBindMatchOrder(t *testing.T) {
	cases := []struct {
		name       string
		plan       NodePlan
		wantDNAT   []string
		wantCount  []string
	}{
		{
			name: "with-bind-ipv4",
			plan: NodePlan{NodeID: 10, Rules: []Rule{{
				ForwardID: 42, InPort: 24000, BindIP: "192.0.2.10",
				TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp", "udp"},
			}}},
			wantDNAT: []string{
				"ip daddr 192.0.2.10 tcp dport 24000 counter dnat ip to 198.51.100.20:443",
				"ip daddr 192.0.2.10 udp dport 24000 counter dnat ip to 198.51.100.20:443",
			},
			wantCount: []string{
				"ct original ip daddr 192.0.2.10 ip daddr 198.51.100.20 tcp dport 443 counter",
				"ct original ip daddr 192.0.2.10 ip daddr 198.51.100.20 udp dport 443 counter",
			},
		},
		{
			name: "with-bind-ipv6",
			plan: NodePlan{NodeID: 10, Rules: []Rule{{
				ForwardID: 43, InPort: 24001, BindIP: "2001:db8::10",
				TargetHost: "2001:db8::20", TargetPort: 8443, Protocols: []string{"tcp"},
			}}},
			wantDNAT: []string{
				"ip6 daddr 2001:db8::10 tcp dport 24001 counter dnat ip6 to [2001:db8::20]:8443",
			},
			wantCount: []string{
				"ct original ip6 daddr 2001:db8::10 ip6 daddr 2001:db8::20 tcp dport 8443 counter",
			},
		},
		{
			name: "without-bind",
			plan: NodePlan{NodeID: 10, Rules: []Rule{{
				ForwardID: 44, InPort: 24002,
				TargetHost: "198.51.100.20", TargetPort: 443, Protocols: []string{"tcp"},
			}}},
			wantDNAT: []string{
				"tcp dport 24002 counter dnat ip to 198.51.100.20:443",
			},
			wantCount: []string{
				"ip daddr 198.51.100.20 tcp dport 443 counter",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := RenderTable(tc.plan)
			for _, want := range tc.wantDNAT {
				if !strings.Contains(script, want) {
					t.Fatalf("rendered ruleset must contain DNAT line %q:\n%s", want, script)
				}
			}
			for _, want := range tc.wantCount {
				if !strings.Contains(script, want) {
					t.Fatalf("rendered ruleset must contain counting line %q:\n%s", want, script)
				}
			}
			// R-B01-01: the L4 token must never precede the L3
			// match — `tcp ip daddr …` is the exact shape nft
			// rejects with `syntax error, unexpected ip`.
			for _, bad := range []string{"tcp ip daddr", "udp ip daddr", "tcp ip6 daddr", "udp ip6 daddr"} {
				if strings.Contains(script, bad) {
					t.Fatalf("rendered ruleset must not contain reversed match order %q:\n%s", bad, script)
				}
			}
		})
	}
}
