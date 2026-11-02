package kernel

import (
	"net/netip"
	"reflect"
	"testing"
)

// R8-004 / R8-022: the sing-box guard must match the product's
// canonical non-public range set (private_ranges.go) exactly — the
// ip_is_private predicate it replaced silently judged the CGNAT and
// benchmarking ranges public. These tests pin the set semantically
// (every canonical range is covered, public space is not), pin that
// the rendered guard carries the set verbatim in both its literal
// and domain forms, and pin the regression samples the predicate
// used to cover so the replacement loses nothing.

func guardPrefixes(t *testing.T) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range guardPrivateCIDRs() {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatalf("canonical range %q must parse: %v", c, err)
		}
		out = append(out, p)
	}
	return out
}

func addrCovered(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func TestGuardRangesCoverCanonicalSamples(t *testing.T) {
	prefixes := guardPrefixes(t)
	// One or more sample addresses inside every canonical range,
	// including the two ranges the ip_is_private predicate missed
	// (100.64.0.0/10 and 198.18.0.0/15) and the mapped forms.
	blocked := []string{
		"0.0.0.0", "0.1.2.3",
		"10.0.0.1", "10.255.255.254",
		"100.64.0.1", "100.100.2.1", "100.127.255.254", // CGNAT (R8-004)
		"127.0.0.1", "127.0.0.2",
		"169.254.169.254", "169.254.0.1",
		"172.16.0.1", "172.31.255.254",
		"192.0.0.1", "192.0.2.1",
		"192.168.0.1", "192.168.255.254",
		"198.18.0.1", "198.19.0.2", "198.19.255.254", // benchmarking (R8-022)
		"198.51.100.1", "203.0.113.1",
		"224.0.0.1", "239.255.255.255",
		"240.0.0.1", "255.255.255.255",
		"::", "::1",
		"100::1", "100::ffff:ffff",
		"2001:db8::1",
		"fc00::1", "fd8b:1234::2",
		"fe80::1", "febf::ffff",
		"ff00::1", "ff02::1",
		// IPv4-mapped forms of blocked v4 ranges.
		"::ffff:10.0.0.1", "::ffff:100.64.0.1", "::ffff:127.0.0.2",
		"::ffff:169.254.169.254", "::ffff:198.19.0.2",
	}
	for _, s := range blocked {
		addr := netip.MustParseAddr(s)
		if !addrCovered(prefixes, addr) {
			t.Errorf("canonical guard set must cover %s", s)
		}
	}
	// Public space must stay reachable: the guard is a private-range
	// block, not a default deny.
	allowed := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34", "140.82.121.3",
		"100.63.255.255", "100.128.0.1", // just outside CGNAT /10
		"198.17.255.255", "198.20.0.1", // just outside 198.18/15
		"11.0.0.1", "172.32.0.1", "192.169.0.1", "223.255.255.255",
		"2606:4700:4700::1111", "2001:4860:4860::8888", "2a00:1450:4000::1",
		"::ffff:8.8.8.8", "::ffff:100.128.0.1",
	}
	for _, s := range allowed {
		addr := netip.MustParseAddr(s)
		if addrCovered(prefixes, addr) {
			t.Errorf("canonical guard set must not cover public address %s", s)
		}
	}
}

func TestGuardRangesContainEveryCanonicalSegment(t *testing.T) {
	// 面 × 段 matrix: every segment of the two canonical halves must
	// be a subset of the rendered guard set (so no segment can be
	// dropped from guardPrivateCIDRs without this test noticing),
	// and the guard set must contain nothing beyond the halves plus
	// their mapped derivations.
	prefixes := guardPrefixes(t)
	segments := append(append([]string{}, guardPrivateCIDRsV4...), guardPrivateCIDRsV6...)
	for _, seg := range segments {
		sp := netip.MustParsePrefix(seg)
		contained := false
		for _, p := range prefixes {
			if p.Bits() <= sp.Bits() && p.Contains(sp.Addr()) {
				contained = true
				break
			}
		}
		if !contained {
			t.Errorf("guard set lost canonical segment %s", seg)
		}
	}
	if want := 2*len(guardPrivateCIDRsV4) + len(guardPrivateCIDRsV6); len(prefixes) != want {
		t.Fatalf("guard set must be v4 + v6 + mapped(v4) = %d ranges, got %d", want, len(prefixes))
	}
}

func TestRenderedGuardCarriesCanonicalSetInBothForms(t *testing.T) {
	in := Inbound{ID: 1, Protocol: ProtocolVLESS, Port: 443,
		Settings: Settings{UUID: "00000000-0000-4000-8000-000000000000"}}
	cfg := renderJSON(t, TypeSingbox, []Inbound{in})
	rules := g1RouteRules(t, cfg)

	want := guardPrivateCIDRs()
	wantSet := map[string]bool{}
	for _, c := range want {
		wantSet[c] = true
	}
	// Literal form (head guard) and domain form (fallthrough reject)
	// must carry the identical full set.
	for _, idx := range []int{0, len(rules) - 1} {
		guard := rules[idx].(map[string]interface{})
		raw, ok := guard["ip_cidr"].([]interface{})
		if !ok {
			t.Fatalf("rule %d: guard must carry ip_cidr, got %v", idx, guard)
		}
		got := map[string]bool{}
		for _, v := range raw {
			s, ok := v.(string)
			if !ok {
				t.Fatalf("rule %d: ip_cidr entries must be strings, got %v", idx, raw)
			}
			got[s] = true
		}
		if !reflect.DeepEqual(got, wantSet) {
			t.Fatalf("rule %d: guard ip_cidr set differs from the canonical set", idx)
		}
		if _, hasPredicate := guard["ip_is_private"]; hasPredicate {
			t.Fatalf("rule %d: guard must not fall back to the ip_is_private predicate", idx)
		}
	}
}
