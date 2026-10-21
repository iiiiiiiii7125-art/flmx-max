package bypass

import (
	"context"
	"net"

	"github.com/go-gost/core/bypass"
)

// NoPrivateBypassName is the panel/agent shared name of the built-in
// bypass that makes a service refuse connections to non-public target
// addresses (FIX2-C #8, the FIX-I leftover). The panel attaches
// "bypass": "flmx-no-private" to federation exit role services; the
// agent resolves the name to this built-in implementation at parse
// time (see config/parsing/bypass.List), so it works even though no
// bypass definition is shipped in the config — a registry lookup
// alone would return a wrapper whose Contains is false for unknown
// names (fail-open), which is exactly what must not happen here.
const NoPrivateBypassName = "flmx-no-private"

// nonPublicCIDRs are the special-purpose ranges not covered by the
// net.IP predicates used below (IsPrivate covers RFC1918 + ULA;
// loopback/link-local/multicast/unspecified have their own checks).
var nonPublicCIDRs = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"0.0.0.0/8",      // "this host on this network" (RFC 791)
		"100.64.0.0/10",  // shared address space / CGNAT (RFC 6598)
		"192.0.0.0/24",   // IETF protocol assignments (RFC 6890)
		"198.18.0.0/15",  // benchmarking (RFC 2544)
		"240.0.0.0/4",    // reserved, incl. 255.255.255.255 (RFC 1112)
		"100::/64", // discard-only (RFC 6666)
		// NOTE: no ::ffff:0:0/96 entry — IPv4-mapped addresses are
		// normalized via To4 in isNonPublicIP before matching, and a
		// mapped CIDR here would collapse to 0.0.0.0 in ParseCIDR and
		// make Contains match every IPv4 address.
	} {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// noPrivateLookupIP resolves a hostname for the bypass check. It is a
// variable so tests can substitute a deterministic resolver.
var noPrivateLookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

type noPrivateBypass struct{}

// NewNoPrivateBypass creates the built-in flmx-no-private bypass:
// blacklist semantics — Contains reports true (block the connection)
// when the target address is, or resolves to, a non-public address.
func NewNoPrivateBypass() bypass.Bypass {
	return &noPrivateBypass{}
}

// isNonPublicIP reports whether ip is loopback, private, link-local,
// multicast, unspecified, or in one of the special-purpose ranges.
func isNonPublicIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return true
	}
	for _, n := range nonPublicCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (bp *noPrivateBypass) Contains(ctx context.Context, network, addr string, opts ...bypass.Option) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return isNonPublicIP(ip)
	}
	// Hostname target: the router would resolve it at dial time, so a
	// literal-only check could be evaded by a name pointing at a LAN
	// address. Resolve here and block if ANY answer is non-public.
	// Resolution failure blocks too (fail-closed): a target whose
	// addresses cannot be verified is not provably public.
	ips, err := noPrivateLookupIP(ctx, host)
	if err != nil || len(ips) == 0 {
		return true
	}
	for _, ip := range ips {
		if isNonPublicIP(ip) {
			return true
		}
	}
	return false
}

func (bp *noPrivateBypass) IsWhitelist() bool {
	return false
}
