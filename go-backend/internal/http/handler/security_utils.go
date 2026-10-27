package handler

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"go-backend/internal/http/client"
	"go-backend/internal/kernel"
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

// validateNoEmptyLabelHost 是空标签主机名的语法层校验（终审 L1-F-06/07/08）：
// 只判语法、不做 DNS 解析。空串不是空标签，由调用方另行处理空值；IP
// 字面量无空标签，可直接传入。
func validateNoEmptyLabelHost(host string) error {
	if kernel.HasEmptyLabel(host) {
		return fmt.Errorf("主机名 %q 包含空标签", host)
	}
	return nil
}

// validateNoEmptyLabelAddr 对 host:port 形态（多目标以逗号/换行分隔）逐个取
// host 做空标签校验；无端口时整段即 host。格式错误由既有校验处理，此处
// 只补空标签判，不新增格式拒绝。
func validateNoEmptyLabelAddr(addr string) error {
	for _, part := range splitRemoteParts(addr) {
		host := part
		if h, _, err := net.SplitHostPort(part); err == nil {
			host = h
		}
		if err := validateNoEmptyLabelHost(host); err != nil {
			return err
		}
	}
	return nil
}

// validateNoEmptyLabelURLHost 解析 URL 取 hostname 做空标签校验；解析
// 失败时不报错，由既有校验处理。
func validateNoEmptyLabelURLHost(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil
	}
	return validateNoEmptyLabelHost(u.Hostname())
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
	// R3-E-01：结构字符集补齐（@、#、逗号、空白与控制字符）。此前
	// good.com@evil.com、内嵌换行等值全部放行落库，而节点地址是
	// 订阅 auto 模式与中转入口的地址源——拉取期裸拼进订阅权威段
	// 即改写全部订阅者的目标主机。仅加结构字符集：主机名 /
	// IPv4 / IPv6 字面量（含 host:port 与括号 IPv6）及 RFC1918 /
	// 回环的既有可存性一概不变（回环在订阅面的拒绝是拉取期的
	// 口径，不在此处收紧——本地测试节点仍可存）。
	if kernel.HostHasStructuralChars(addr) {
		return fmt.Errorf("address must not contain @, #, commas, whitespace or control characters")
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
