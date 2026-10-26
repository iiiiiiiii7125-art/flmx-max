package health

import (
	"fmt"
	"go-backend/internal/config"
	"go-backend/internal/netguard"
	"net"
	"strconv"
	"strings"
)

// F1X-001: 服务监控 TCP 目标 SSRF 防护。
//
// 面板本机执行的 TCP 探测（nodeId<=0）由面板进程直接拨号。
// 若目标可由低权限监控用户任意指定，面板即成为内网探测跳板。
// 本文件提供创建/修改时与拨号时的双层校验（防 DNS rebinding）。

// isMonitorBlockedIP 报告 ip 是否非公网。R2 批1起判定正本收口
// netguard（段集含 0.0.0.0/8 整段与 bypass 基底、NAT64/6to4 按内嵌
// 同判、本地 NAT64 整段非公网）；本函数保留为薄封装。
func isMonitorBlockedIP(ip net.IP) bool {
	return netguard.IsNonPublicIP(ip)
}

// isMonitorIPAllowlisted 与 H-15 SecureFetch 同源的运维放行通道：
// FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true 全放行，
// FLMX_FETCH_PRIVATE_ALLOWLIST="cidr,ip,..." 按条目放行。
func isMonitorIPAllowlisted(ip net.IP) bool {
	if config.GetEnvAny("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "FLVX_FETCH_ALLOW_PRIVATE_NETWORKS") == "true" {
		return true
	}
	raw := config.GetEnvAny("FLMX_FETCH_PRIVATE_ALLOWLIST", "FLVX_FETCH_PRIVATE_ALLOWLIST")
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

// ValidateNodeMonitorTarget 校验节点执行的服务监控目标（R4X-008）。
// 与面板本机面 ValidateMonitorTCPTarget 的分工：本机面按 NonPublic
// 全集 + allowlist 把关（面板进程直接拨号）；节点面只补硬拒子集
// 底线——目标字面 IP 或解析结果任一命中 netguard 硬拒层（元数据
// 精确集 ∪ 169.254/16 ∪ 未指定 ∪ 本地 NAT64）即拒绝。私网/回环/
// CGNAT/保留/组播/链路本地一律放行：节点监控其所在 LAN 服务是该
// 功能的正当用途，严禁顺手禁（防过修口径，verdicts §5 写死）。
// 主机名解析失败不拒存——面板与节点 DNS 分视图，节点侧可能可解析；
// 执行端（agent）另有字面量 + 解析落点双重复检兜底。typ 为 "tcp"
// 时 target 须为 host:port；"icmp" 时为裸主机（可带 [] 的 IPv6）。
func ValidateNodeMonitorTarget(typ, target string) error {
	host := strings.TrimSpace(target)
	if strings.EqualFold(strings.TrimSpace(typ), "tcp") {
		h, port, err := net.SplitHostPort(host)
		if err != nil {
			return fmt.Errorf("目标地址格式错误（应为 host:port）")
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("目标端口无效: %s", port)
		}
		host = h
	} else {
		// ICMP 裸主机形态：容忍 [v6] 包裹，与 agent icmpPing 同口径。
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		}
		// PC-09：icmp 目标含端口即拒——"host:port"（如 "203.0.113.7:80"）
		// 此前经 ParseIP/LookupIP 双失败后不拒存而容忍落库，agent
		// icmpPing ResolveIPAddr 失败致 probe 失败（loud）。裸 IPv6
		// 字面量含冒号但 ParseIP 成功，不在此限；域名目标不含冒号，
		// 不受影响（R4X-008「解析失败不拒存」的容忍原由只针对域名
		// Lookup，见函数头注）。与 tcp 面口径对齐（tcp 面 SplitHostPort
		// 失败即拒）。
		if strings.Contains(host, ":") && net.ParseIP(host) == nil {
			return fmt.Errorf("目标地址格式错误（icmp 目标应为裸主机，不含端口）")
		}
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("目标主机为空")
	}
	if ip := net.ParseIP(host); ip != nil {
		if netguard.Classify(ip).HardDeny {
			return fmt.Errorf("目标地址不允许（元数据/硬拒绝地址）: %s", target)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		// 解析失败不拒存（见函数头注）。
		return nil
	}
	for _, ip := range ips {
		if netguard.Classify(ip).HardDeny {
			return fmt.Errorf("目标地址不允许（元数据/硬拒绝地址）: %s", target)
		}
	}
	return nil
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
		// R2X-027：判定改两层——netguard 硬拒（元数据精确集 ∪
		// 169.254/16 ∪ 未指定）先于 allowlist，任何放行配置不得
		// 覆盖；loopback/私网/CGNAT 的 allow_private 放行维持
		// （按判定口径地址查 allowlist，翻译内嵌同判）。
		// fe80::/10 不在硬拒层（R3X-010 政策分层），属 NonPublic，
		// 同样过下方 allowlist 门。
		r := netguard.Classify(ip)
		if r.HardDeny {
			return nil, fmt.Errorf("目标地址不允许（元数据/硬拒绝地址）: %s", host)
		}
		if r.NonPublic() && !isMonitorIPAllowlisted(netguard.EffectiveIP(ip)) {
			return nil, fmt.Errorf("目标地址不允许（内网/保留地址）: %s", host)
		}
	}
	return ips, nil
}
