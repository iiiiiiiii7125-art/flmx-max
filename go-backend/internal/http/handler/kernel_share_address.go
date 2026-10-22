package handler

import (
	"net/netip"
	"strings"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
)

// Share address resolution (DB-2, ported from upstream 3.3.33): an
// inbound's share/subscription address comes from its ShareAddressMode —
// auto (the node's default address), the node's IPv4, the node's IPv6,
// or a custom host (ShareHost). The mode only picks the address source;
// it never changes which inbounds are served or to whom.

func inboundShareMode(row *model.NodeInbound) string {
	mode := strings.TrimSpace(row.ShareAddressMode)
	if (mode == "" || mode == "auto") && strings.TrimSpace(row.ShareHost) != "" {
		return "custom" // Addresses saved before this setting keep their override.
	}
	if mode == "" {
		return "auto"
	}
	return mode
}

func nodeShareIP(node *model.Node, ipv6 bool) string {
	if node == nil {
		return ""
	}
	value := node.ServerIPV4.String
	if ipv6 {
		value = node.ServerIPV6.String
	}
	for _, host := range []string{value, node.ServerIP} {
		addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
		if err == nil && addr.Is6() == ipv6 && !addr.Is4In6() {
			return addr.String()
		}
	}
	return ""
}

// normalizeShareHost returns the stored form of a share host value:
// whitespace-trimmed, with a single trailing dot removed (B2-01 — the
// FQDN root dot is a legitimate way to write a hostname, and stored
// values carrying it must normalize instead of failing the import
// surface's shape check). Brackets are kept as written; the value is
// canonicalized for validation and rendering by shareHostValue.
func normalizeShareHost(host string) string {
	return strings.TrimSuffix(strings.TrimSpace(host), ".")
}

// shareHostValue is the canonical form a share host is validated and
// rendered with: brackets stripped and a single trailing dot removed
// (B2-01). Two trailing dots still fail validation, as do underscore
// labels, Unicode IDN and loopback/link-local literals — only the
// single root dot is normalized.
func shareHostValue(host string) string {
	return strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), ".")
}

func inboundShareAddress(row *model.NodeInbound, node *model.Node) string {
	switch inboundShareMode(row) {
	case "custom":
		return shareHostValue(row.ShareHost)
	case "ipv4":
		return nodeShareIP(node, false)
	case "ipv6":
		return nodeShareIP(node, true)
	default:
		if node == nil {
			return ""
		}
		return firstNonEmpty(strings.Trim(strings.TrimSpace(node.ServerIP), "[]"), nodeShareIP(node, false), nodeShareIP(node, true))
	}
}

// validateShareHostValue applies the import surface's host standard
// (kernel.ValidateImportHost) to a share host value: an IP literal or a
// hostname, and nothing carrying URI or line structure characters
// (@ / ? #, whitespace, control characters). The share renderers embed
// the host verbatim — ShareURI splices it into the authority segment
// and SurgeLine into a comma-separated line — so a malformed value
// would rewrite every subscriber's target host (F-A-01/C-01). Every
// ShareHost write surface and the pull-time guard share this check.
// The value is canonicalized first (shareHostValue): a single
// trailing dot is normalized away (B2-01); everything else the import
// standard rejects keeps being rejected.
func validateShareHostValue(host string) error {
	host = shareHostValue(host)
	if host == "" {
		return errText("请填写自定义分享地址")
	}
	return kernel.ValidateImportHost(host)
}

func validateShareAddress(mode, host string, node *model.Node) error {
	switch mode {
	case "auto":
		return nil
	case "custom":
		if err := validateShareHostValue(host); err != nil {
			return err
		}
	case "ipv4", "ipv6":
		if nodeShareIP(node, mode == "ipv6") == "" {
			return errText("节点未设置可用的 " + map[string]string{"ipv4": "IPv4", "ipv6": "IPv6"}[mode] + " 地址")
		}
	default:
		return errText("分享地址类型无效")
	}
	return nil
}
