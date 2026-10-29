package bypass

// 同步义务（镜像）：本文件是 go-backend/internal/netguard 分类器的
// agent 侧镜像实现（独立 module 无法 import 后端包）。修改本文件的
// 段集、硬拒集或翻译规则（NAT64/6to4/本地 NAT64）时，必须同批
// 同步 go-backend/internal/netguard/netguard.go，反之亦然。

import (
	"context"
	"net"
	"net/netip"

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

var (
	// 翻译前缀与元数据精确集，与 netguard 正本同值（见文件头同步义务）。
	mirrorNAT64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	mirrorNAT64Local     = netip.MustParsePrefix("64:ff9b:1::/48")
	mirrorSixToFour      = netip.MustParsePrefix("2002::/16")
	mirrorLinkLocalV4    = netip.MustParsePrefix("169.254.0.0/16")
	mirrorMetadataExact  = []netip.Addr{
		netip.MustParseAddr("169.254.169.254"),
		netip.MustParseAddr("100.100.100.200"),
		netip.MustParseAddr("fd00:ec2::254"),
	}
)

// IsHardDenyIP reports whether ip hits the hard-deny layer only
// (the netguard ClassifyAddr hard set, mirrored): metadata exact
// set, the whole 169.254.0.0/16 link-local segment, unspecified,
// the local-NAT64 segment, and NAT64/6to4 forms judged on their
// embedded IPv4. Unlike IsNonPublicIP it does NOT cover loopback or
// RFC1918 — callers like the kernel download primitive (R7-C-03)
// layer it separately: hard-deny at the entry URL, full non-public
// judgment on redirect hops.
func IsHardDenyIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	a, err := netip.ParseAddr(ip.String())
	if err != nil {
		return true
	}
	a = a.Unmap()
	if mirrorNAT64Local.Contains(a) {
		return true
	}
	eff := a
	if mirrorNAT64WellKnown.Contains(a) {
		b := a.As16()
		eff = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	} else if mirrorSixToFour.Contains(a) {
		b := a.As16()
		eff = netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
	}
	if eff.IsUnspecified() {
		return true
	}
	for _, m := range mirrorMetadataExact {
		if eff == m {
			return true
		}
	}
	return mirrorLinkLocalV4.Contains(eff)
}

// IsNonPublicIP is the exported form of isNonPublicIP for the
// agent's own outbound fetch policy faces (R7-C-02 speedtest probe,
// R7-C-03 kernel download primitive): those call sites live in other
// packages of this module but must judge targets with exactly this
// mirrored classifier, not a second copy of the range set.
func IsNonPublicIP(ip net.IP) bool { return isNonPublicIP(ip) }

// isNonPublicIP reports whether ip is loopback, private, link-local,
// multicast, unspecified, in one of the special-purpose ranges, a
// hard-denied metadata address, or a translated address (NAT64 /96,
// NAT64 local /48, 6to4) whose embedded IPv4 is any of those.
func isNonPublicIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if a, err := netip.ParseAddr(ip.String()); err == nil {
		a = a.Unmap()
		// 本地 NAT64 整段非公网、不做内嵌提取（布局含 u 字节穿插）。
		if mirrorNAT64Local.Contains(a) {
			return true
		}
		if mirrorNAT64WellKnown.Contains(a) {
			b := a.As16()
			return isNonPublicPlain(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if mirrorSixToFour.Contains(a) {
			b := a.As16()
			return isNonPublicPlain(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
		for _, m := range mirrorMetadataExact {
			if a == m {
				return true
			}
		}
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

// isNonPublicPlain 对翻译提取出的内嵌 IPv4 做同一判定。
func isNonPublicPlain(a netip.Addr) bool {
	for _, m := range mirrorMetadataExact {
		if a == m {
			return true
		}
	}
	return isNonPublicIP(net.IP(a.AsSlice()))
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
