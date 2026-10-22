package handler

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	"go-backend/internal/http/client"
	"go-backend/internal/netguard"
)

// DisableSafeRemoteAddrCheckForTesting allows bypassing the safety check during integration tests.
var DisableSafeRemoteAddrCheckForTesting = false

// D3-01: 联邦出站客户端的拨号期 SSRF 复检与本包的入口校验共用同一
// 测试旁路开关（契约测试以 127.0.0.1 的 httptest 充当对端面板）。
func init() {
	client.SetSafetyBypassFunc(func() bool { return DisableSafeRemoteAddrCheckForTesting })
}

// IsSafeRemoteAddr checks if a given address is safe to connect to (prevents SSRF/Open Proxy).
// It resolves domains to IPs to prevent DNS rebinding attacks pointing to internal networks.
// Supports multiple addresses separated by commas or newlines (one per line).
func IsSafeRemoteAddr(addr string) error {
	if DisableSafeRemoteAddrCheckForTesting {
		return nil
	}

	for _, part := range splitRemoteParts(addr) {
		if err := checkSingleRemoteAddr(part); err != nil {
			return err
		}
	}
	return nil
}

// splitRemoteParts splits a multi-address string by commas and newlines.
func splitRemoteParts(addr string) []string {
	addr = strings.ReplaceAll(addr, "\n", ",")
	addr = strings.ReplaceAll(addr, "\r", ",")
	parts := strings.Split(addr, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// checkSingleRemoteAddr validates a single address.
func checkSingleRemoteAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.Contains(err.Error(), "missing port in address") {
			host = addr
		} else {
			return fmt.Errorf("invalid address format: %v", err)
		}
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve address %q: %v", addr, err)
	}

	for _, ip := range ips {
		// R2X-001：改走 netguard 普通转发策略——非公网全拒、硬拒
		// 先行（旧实现仅拦 loopback+RFC1918，链路本地/元数据/CGNAT/
		// 240-4/组播/NAT64·6to4 内嵌形态全部漏过，入口节点 IMDS
		// 可被普通用户直读）。管理员与 allow_local_remote_addr
		// 例外在调用方，本函数口径不变。
		if d := netguard.Judge(ip, netguard.PolicyPublicOnly, false); !d.Allowed {
			return fmt.Errorf("address %q resolves to internal IP: %s", addr, ip.String())
		}
	}

	return nil
}

// IsValidNodeAddress ensures the address is strictly a host or host:port.
// It explicitly denies schemes (http://, https://), paths (/...), and query params (?).
func IsValidNodeAddress(addr string) error {
	addr = strings.TrimSpace(addr)
	if strings.Contains(addr, "://") {
		return fmt.Errorf("address must not contain scheme (e.g. http://)")
	}
	if strings.ContainsAny(addr, "/?") {
		return fmt.Errorf("address must not contain path or query parameters")
	}
	// A bare IPv6 literal contains multiple colons, so net.SplitHostPort treats
	// it as a malformed host:port pair. Accept IP literals before attempting
	// host:port parsing; netip also handles scoped IPv6 addresses.
	if _, err := netip.ParseAddr(addr); err == nil {
		return nil
	}

	_, _, err := net.SplitHostPort(addr)
	if err != nil {
		if !strings.Contains(err.Error(), "missing port in address") {
			return fmt.Errorf("invalid address format")
		}
	}
	return nil
}
