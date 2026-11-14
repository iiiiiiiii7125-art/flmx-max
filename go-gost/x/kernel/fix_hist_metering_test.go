package kernel

import (
	"strings"
	"testing"
)

// FIX-031 (AUDIT-HIST): nft counter rules for a bound inbound must match
// only its BindIP. The audit's live proof: an SS inbound bound to
// 127.0.0.1:25317 still counted a TCP connect and a UDP packet sent to
// 172.26.15.152:25317 (nothing listens there, both refused) because the
// rules were port-only.
func TestFix031BoundCounterRulesMatchBindIP(t *testing.T) {
	ports := []Port{{InboundID: 133, Port: 25317, Network: "tcp,udp", Bind: "127.0.0.1"}}
	script := nftScript(nftTable, expandPorts(ports), expandGuards(ports), nil, nil)
	for _, want := range []string{
		"ip daddr 127.0.0.1 tcp dport 25317 counter name in_133_tcp",
		"ip daddr 127.0.0.1 udp dport 25317 counter name in_133_udp",
		"ip saddr 127.0.0.1 tcp sport 25317 counter name out_133_tcp",
		"ip saddr 127.0.0.1 udp sport 25317 counter name out_133_udp",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bound rule missing %q:\n%s", want, script)
		}
	}
	// Mutation guard: the old bare port-only rule must be gone for this
	// port — a bare rule would keep counting foreign-address traffic.
	if strings.Contains(script, "\n\t\ttcp dport 25317 counter name in_133_tcp") ||
		strings.Contains(script, "\n\t\ttcp sport 25317 counter name out_133_tcp") {
		t.Fatalf("bound inbound keeps a bare port-only rule:\n%s", script)
	}
}

func TestFix031WildcardBindKeepsPortOnlyRules(t *testing.T) {
	for _, bind := range []string{"", "0.0.0.0", "::"} {
		ports := []Port{{InboundID: 134, Port: 25318, Network: "tcp", Bind: bind}}
		script := nftScript(nftTable, expandPorts(ports), expandGuards(ports), nil, nil)
		if !strings.Contains(script, "\t\ttcp dport 25318 counter name in_134_tcp\n") {
			t.Fatalf("bind %q must keep the bare port-only in rule:\n%s", bind, script)
		}
		if !strings.Contains(script, "\t\ttcp sport 25318 counter name out_134_tcp\n") {
			t.Fatalf("bind %q must keep the bare port-only out rule:\n%s", bind, script)
		}
		if strings.Contains(script, "daddr") || strings.Contains(script, "saddr") {
			t.Fatalf("bind %q must not add an address match:\n%s", bind, script)
		}
	}
}

func TestFix031IPv6BindMatchesIP6(t *testing.T) {
	ports := []Port{{InboundID: 135, Port: 25319, Network: "tcp", Bind: "::1"}}
	script := nftScript(nftTable, expandPorts(ports), expandGuards(ports), nil, nil)
	for _, want := range []string{
		"ip6 daddr ::1 tcp dport 25319 counter name in_135_tcp",
		"ip6 saddr ::1 tcp sport 25319 counter name out_135_tcp",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("IPv6 bound rule missing %q:\n%s", want, script)
		}
	}
}

// FIX-029 (AUDIT-HIST): the agent must report packet counts alongside
// bytes — the panel strips per-packet headers off realm nft counters
// with them, and a missing packets field must read back as zero (old
// nft output / pre-fix agents).
func TestFix029ParseCountersCarriesPackets(t *testing.T) {
	raw := []byte(`{"nftables":[{"metainfo":{}},` +
		`{"counter":{"family":"inet","name":"in_42_tcp","table":"flmx_kernel","packets":6,"bytes":2320}},` +
		`{"counter":{"family":"inet","name":"out_42_tcp","table":"flmx_kernel","packets":4,"bytes":2216}},` +
		`{"counter":{"family":"inet","name":"in_43_udp","table":"flmx_kernel","bytes":56}}]}`)
	counters, _, err := parseCounters(raw, nftTable)
	if err != nil {
		t.Fatal(err)
	}
	if len(counters) != 3 {
		t.Fatalf("counters = %+v, want 3", counters)
	}
	if counters[0].Packets != 6 || counters[0].Bytes != 2320 {
		t.Fatalf("in_42_tcp = %+v, want packets=6 bytes=2320", counters[0])
	}
	if counters[1].Packets != 4 || counters[1].Bytes != 2216 {
		t.Fatalf("out_42_tcp = %+v, want packets=4 bytes=2216", counters[1])
	}
	if counters[2].Packets != 0 || counters[2].Bytes != 56 {
		t.Fatalf("in_43_udp = %+v, want packets=0 bytes=56", counters[2])
	}
}
