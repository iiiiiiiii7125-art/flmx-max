package kernel

import (
	"fmt"
	"net"
	"net/netip"
)

// Canonical non-public destination ranges: the product's single
// definition of which targets a node-side egress guard must block
// (the flmx-no-private definition). Two kernels enforce it — Xray via
// freedom's built-in rule (version-gated, see handler/kernel.go) and
// sing-box via the rendered ip_cidr guard below. (A third copy, the
// agent's nft drop sets for the Snell server, was deleted with the
// built Snell inbound feature.) The set is pinned against the gold
// segment list embedded in scripts/check-private-ranges.sh, wired
// into CI through check-doc-consistency.sh; changing a segment
// requires a ruling and a matching gold-list update.
//
// The sing-box guard spells the set out as explicit ip_cidr ranges
// instead of sing-box's ip_is_private predicate (R8-004 / R8-022):
// that predicate delegates to Go netip's notion of "public", which
// does not cover the CGNAT shared address space 100.64.0.0/10, the
// benchmarking range 198.18.0.0/15 and several other special-purpose
// ranges below — a sing-box node silently let those destinations
// through while the Xray guard blocked them.
//
// Registered divergence from the netguard classifier lineage
// (R3-C2-01 adjudication): this canonical set blocks the four
// documentation ranges (192.0.2.0/24, 198.51.100.0/24,
// 203.0.113.0/24, 2001:db8::/32), while the netguard lineage
// (internal/netguard plus its mirrors in go-gost/x/bypass and
// go-gost/x/socket) deliberately classifies them as public — its
// import/share/probe faces accept literal documentation addresses
// as placeholder targets. The difference is exactly those four
// ranges, in that direction only, and
// scripts/check-private-ranges.sh pins it mechanically; do not
// "align" one side without revisiting the adjudication.

// guardPrivateCIDRsV4 is the IPv4 half of the canonical set.
var guardPrivateCIDRsV4 = []string{
	"0.0.0.0/8",       // "this host on this network" (RFC 791)
	"10.0.0.0/8",      // RFC 1918
	"100.64.0.0/10",   // shared address space / CGNAT (RFC 6598)
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, incl. cloud metadata (RFC 3927)
	"172.16.0.0/12",   // RFC 1918
	"192.0.0.0/24",    // IETF protocol assignments (RFC 6890)
	"192.0.2.0/24",    // documentation TEST-NET-1 (RFC 5737)
	"192.168.0.0/16",  // RFC 1918
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // documentation TEST-NET-2 (RFC 5737)
	"203.0.113.0/24",  // documentation TEST-NET-3 (RFC 5737)
	"224.0.0.0/4",     // multicast (RFC 1112)
	"240.0.0.0/4",     // reserved, incl. 255.255.255.255 (RFC 1112)
}

// guardPrivateCIDRsV6 is the native IPv6 half of the canonical set.
var guardPrivateCIDRsV6 = []string{
	"::/128",        // unspecified
	"::1/128",       // loopback
	"100::/64",      // discard-only (RFC 6666)
	"2001:db8::/32", // documentation (RFC 3849)
	"fc00::/7",      // unique local (RFC 4193)
	"fe80::/10",     // link-local (RFC 4291)
	"ff00::/8",      // multicast (RFC 4291)
}

// guardPrivateCIDRs returns the full canonical set: both native
// halves plus the IPv4-mapped form (::ffff:a.b.c.d) of every v4
// range, derived from guardPrivateCIDRsV4 so the forms can never
// drift apart — a destination that reaches the guard in mapped form
// (a resolver or DNS64 path handing back ::ffff:100.64.0.1) must not
// slip past the v4 set.
//
// Registered residual (R8-049, shared with the Xray
// guard): translated forms (NAT64 / 6to4 with an embedded IPv4
// address) are not extracted — the translation prefixes themselves
// are not blocked, because dropping them wholesale would kill
// legitimate NAT64 public traffic. The embedded target is only
// reachable at all on a node that actually runs such a translator.
func guardPrivateCIDRs() []string {
	out := make([]string, 0, len(guardPrivateCIDRsV4)+len(guardPrivateCIDRsV6)+len(guardPrivateCIDRsV4))
	out = append(out, guardPrivateCIDRsV4...)
	out = append(out, guardPrivateCIDRsV6...)
	for _, c := range guardPrivateCIDRsV4 {
		ip, ipnet, err := net.ParseCIDR(c)
		if err != nil || ip.To4() == nil {
			continue
		}
		ones, _ := ipnet.Mask.Size()
		out = append(out, fmt.Sprintf("::ffff:%s/%d", ip.String(), 96+ones))
	}
	return out
}

// guardCIDRIntersection returns the overlap of the given CIDRs with
// the canonical guard set. CIDR blocks either nest or are disjoint,
// so the intersection is the narrower block of each overlapping pair.
func guardCIDRIntersection(cidrs []string) []string {
	var canon []netip.Prefix
	for _, g := range guardPrivateCIDRs() {
		if p, err := netip.ParsePrefix(g); err == nil {
			canon = append(canon, p.Masked())
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue
		}
		p = p.Masked()
		for _, g := range canon {
			var inter netip.Prefix
			switch {
			case g.Bits() <= p.Bits() && g.Contains(p.Addr()):
				inter = p
			case p.Bits() <= g.Bits() && p.Contains(g.Addr()):
				inter = g
			default:
				continue
			}
			if s := inter.String(); !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}
