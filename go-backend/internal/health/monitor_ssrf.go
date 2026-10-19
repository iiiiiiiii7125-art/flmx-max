package health

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// F1X-001: 服务监控 TCP 目标 SSRF 防护。
//
// 面板本机执行的 TCP 探测（nodeId<=0）由面板进程直接拨号。
// 若目标可由低权限监控用户任意指定，面板即成为内网探测跳板。
// 本文件提供创建/修改时与拨号时的双层校验（防 DNS rebinding）。

// monitorBlockedCIDRs 面板本机拨号拒绝的地址段。
var monitorBlockedCIDRs = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16", // 链路本地 + 云元数据服务
	"100.64.0.0/10",  // CGNAT
	"fc00::/7",       // IPv6 私有
	"fe80::/10",      // IPv6 链路本地
}

func isMonitorBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, cidr := range monitorBlockedCIDRs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// isMonitorIPAllowlisted 与 H-15 SecureFetch 同源的运维放行通道：
// FLVX_FETCH_ALLOW_PRIVATE_NETWORKS=true 全放行，
// FLVX_FETCH_PRIVATE_ALLOWLIST="cidr,ip,..." 按条目放行。
func isMonitorIPAllowlisted(ip net.IP) bool {
	if os.Getenv("FLVX_FETCH_ALLOW_PRIVATE_NETWORKS") == "true" {
		return true
	}
	raw := os.Getenv("FLVX_FETCH_PRIVATE_ALLOWLIST")
	if raw == "" {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if pip := net.ParseIP(s); pip != nil {
			if pip.Equal(ip) {
				return true
			}
			continue
		}
		_, cidr, err := net.ParseCIDR(s)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateMonitorTCPTarget 校验面板本机执行的 TCP 监控目标。
// target 形如 host:port。返回 nil 表示允许面板本机拨号。
// 域名会做 DNS 解析并逐 IP 判定（任一命中即拒绝，fail-closed）。
func ValidateMonitorTCPTarget(target string) error {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return fmt.Errorf("目标地址格式错误（应为 host:port）")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("目标端口无效: %s", port)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("目标主机为空")
	}
	_, err = resolveValidatedMonitorIPs(host)
	return err
}

// resolveValidatedMonitorIPs 解析主机名并逐 IP 做 SSRF 判定，
// 返回全部通过校验的 IP（fail-closed：任一命中即整体拒绝）。
// R5R2-001: 调用方应直接拨号返回的 IP 字面量，不再二次解析
// （防 validate 与 dial 之间 DNS 变化的 TOCTOU）。
func resolveValidatedMonitorIPs(host string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		ips, err = net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("目标主机无法解析: %s", host)
		}
	}
	for _, ip := range ips {
		if isMonitorBlockedIP(ip) && !isMonitorIPAllowlisted(ip) {
			return nil, fmt.Errorf("目标地址不允许（内网/保留地址）: %s", host)
		}
	}
	return ips, nil
}
