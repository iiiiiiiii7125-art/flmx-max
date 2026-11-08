// Package clientip 是限流面客户端 IP 解析的单一收口（R5-FC，
// L2-09 / L8-F-02 同族修复）：登录限流、gh 镜像限流、WS 鉴权失败
// 限流三面共用本包的可信代理链解析，不再各以裸 RemoteAddr 为键——
// 标准反代部署（Caddy → 前端 nginx → 后端）下 RemoteAddr 恒为代理机地址，
// 裸键意味着全站共用一桶（一人刷满、全体 429；批量升级走面板源时
// 节点群还会自触发 429）。
//
// 信任模型（R5-B2 A-001 / B01-001 收窄口径 / R24-X-01 超配
// fail-closed；C4-01 再收窄）：只有直接对端是回环地址，或落在
// 显式配置 FLMX_TRUSTED_PROXIES（逗号分隔 CIDR/裸 IP）的对端，
// 才采信 XFF 并按
// FLMX_TRUSTED_PROXY_HOPS 从右锚定客户端条目。此前的「私网/链
// 路本地一律可信」使任何能路由到后端端口的内网主机（被攻陷的
// 容器、同网段租户）都能伪造 XFF 轮换限流键、逐 IP 绕过登录/
// WS/gh 三面限流；私网身份本身不再构成信任，信任只来自显式配
// 置。公网直连同样忽略 XFF；跳数超过实际可信链长时 fail-closed
// 回 RemoteAddr，信任边界不可由攻击者预置的左侧条目移动。
package clientip

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"go-backend/internal/config"
)

const (
	// MaxXFFHeaderBytes 是 XFF 头总长上限，超长整体视为不可信。
	MaxXFFHeaderBytes = 4096
	// MaxXFFEntries 是 XFF 条目数上限，超限截断保右弃左（右锚）。
	MaxXFFEntries = 64
)

// UnresolvedKey 是无法从请求解析出任何客户端地址时，限流调用方
// 应记入的共享桶键：所有不可归属请求共用一桶（fail-closed 记账），
// 而不是跳过限流（fail-open）或以含端口的原始串为键（端口轮换
// 即可绕过）。调用方使用本键时应打日志显式记账。
const UnresolvedKey = "unresolved"

// TrustedProxyHops 读取 FLMX_TRUSTED_PROXY_HOPS，默认 1。
// 代码默认保持 1：裸进程/自定义部署的拓扑对二进制不可知，默认
// 只按最保守的单跳假设解析。脚本默认拓扑是 Caddy → 前端 → 后端
// （panel_install.sh 生成的 Caddy 配置反代的是前端端口，再由前端
// 容器 nginx 把 API 请求转给后端），链上共两跳——该拓扑由交付面
// 保证取 2（R6-L8-F-01：随版 compose 模板自带
// FLMX_TRUSTED_PROXY_HOPS: ${FLMX_TRUSTED_PROXY_HOPS:-2} 传递行，
// 安装脚本落盘前补行，面板自升级覆写 compose 时保留/再生该行），
// 部署亦可在 .env 显式覆盖。非标准拓扑（直连、单跳反代）按实际
// 跳数设置，见部署文档（doc/guide/install.md 登录限流一节）。
func TrustedProxyHops() int {
	raw := strings.TrimSpace(config.Getenv("FLMX_TRUSTED_PROXY_HOPS"))
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("warning: invalid FLMX_TRUSTED_PROXY_HOPS=%q, using 1", raw)
		return 1
	}
	if n > 16 {
		log.Printf("warning: FLMX_TRUSTED_PROXY_HOPS=%d too large, capped at 16", n)
		return 16
	}
	return n
}

// FromRequest 按可信代理链口径解析请求的客户端 IP（限流键来源）。
// 解析不出（RemoteAddr 本身非法）时返回 nil，调用方须自行
// fail-closed 处理（见 RateLimitKey / UnresolvedKey）。
func FromRequest(r *http.Request) net.IP {
	remoteIP := ParseIPLiteral(r.RemoteAddr)
	if !isTrustedProxyIP(remoteIP) {
		return remoteIP // 直连：忽略 XFF
	}
	entries := SplitXFFEntries(r.Header.Get("X-Forwarded-For"))
	if len(entries) == 0 {
		return remoteIP
	}
	hops := TrustedProxyHops()
	if hops <= 0 {
		return remoteIP
	}
	// 至此直接对端已判别为可信代理对端（回环或显式配置，见
	// isTrustedProxyIP；不可信对端在首行已忽略 XFF 返回），以
	// 下按索引解析。
	run := TrustedXFFRun(entries, isTrustedProxyIP)
	// R24-X-01：hops 超配（大于实际可信代理链长）时，idx = len-hops
	// 会落在攻击者可控的左侧预置条目上。先夹紧：超配即 fail-closed。
	if chainLen := run + 1; hops > chainLen {
		log.Printf("warning: XFF has %d entries but trusted proxy chain is %d hops (FLMX_TRUSTED_PROXY_HOPS=%d); ignoring XFF (fail-closed), using RemoteAddr", len(entries), chainLen, hops)
		return remoteIP
	}
	idx := len(entries) - hops
	if idx < 0 {
		// K-004: 链长 < 配置跳数：实际拓扑少于配置，可信条目不足。
		// 取最左条目是 fail-open（攻击者可控），改为 fail-closed：
		// 不信任 XFF 任何条目，回退 RemoteAddr，并告警提示检查配置。
		log.Printf("warning: XFF has %d entries but FLMX_TRUSTED_PROXY_HOPS=%d; ignoring XFF (fail-closed), using RemoteAddr", len(entries), hops)
		return remoteIP
	}
	if ip := ParseIPLiteral(entries[idx]); ip != nil {
		return ip
	}
	return remoteIP
}

// RateLimitKey 返回限流桶键：优先 FromRequest 的解析结果；
// 其次 RemoteAddr 的纯 IP 形态（防端口轮换绕过）；两者皆无时
// 返回 ("", false)，调用方必须 fail-closed 记账（如记入
// UnresolvedKey 共享桶并打日志），不得跳过限流。
func RateLimitKey(r *http.Request) (string, bool) {
	if ip := FromRequest(r); ip != nil {
		return ip.String(), true
	}
	if ip := ParseIPLiteral(r.RemoteAddr); ip != nil {
		return ip.String(), true
	}
	return "", false
}

// ParseIPLiteral 解析 IP 字面量或 host:port 形态，返回规范化 IP
// （IPv4-mapped 展开为 4 字节）；解析不出返回 nil。
func ParseIPLiteral(raw string) net.IP {
	value := strings.Trim(strings.TrimSpace(raw), "\"")
	if value == "" {
		return nil
	}
	if ip := net.ParseIP(value); ip != nil {
		return normalizeIPAddress(ip)
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return nil
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil
	}
	return normalizeIPAddress(net.ParseIP(host))
}

func normalizeIPAddress(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip.To16()
}

// trustedProxyCIDRs 解析 FLMX_TRUSTED_PROXIES（逗号分隔 CIDR，
// 裸 IP 视为 /32 或 /128）：未配置或条目非法时该条目不产生信任
// （非法条目记日志跳过），不回退到任何默认私网信任。
func trustedProxyCIDRs() []*net.IPNet {
	raw := strings.TrimSpace(config.Getenv("FLMX_TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if ip := net.ParseIP(part); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
			log.Printf("warning: invalid FLMX_TRUSTED_PROXIES entry %q, skipped", part)
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			log.Printf("warning: invalid FLMX_TRUSTED_PROXIES entry %q, skipped", part)
			continue
		}
		out = append(out, cidr)
	}
	return out
}

// isTrustedProxyIP 报告对端是否是可信代理：回环恒可信（本机反
// 代），其余只认 FLMX_TRUSTED_PROXIES 显式配置（C4-01）。CGNAT
// 100.64/10 此前经 stdlib IsPrivate 本就不在信任内（§11 收窄口
// 径），现随私网整体退出默认信任、口径不变。
func isTrustedProxyIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, cidr := range trustedProxyCIDRs() {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// SplitXFFEntries 健壮切分 XFF：去空白/引号，跳过空条目，
// 超长头整体视为不可信（返回 nil），条目数封顶。
// 注意：只做切分，不做单个条目的合法性判定，留给 ParseIPLiteral。
//
// R2 B01-001：条目数超上限时截断必须保右弃左（右锚）。可信代理把
// 真实客户端条目追加在 XFF 最右侧，消费方一律从右按跳数索引；
// 保留最左会让攻击者塞满条目把代理追加的真实条目挤出保留集。
// 头部总长已由 MaxXFFHeaderBytes 封顶，全量收集后再截尾无无界
// 分配风险。
func SplitXFFEntries(raw string) []string {
	if len(raw) > MaxXFFHeaderBytes {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), "\"")
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) > MaxXFFEntries {
		out = out[len(out)-MaxXFFEntries:]
	}
	return out
}

// TrustedXFFRun 从右向左数 XFF 内连续的可信代理条目数（不含直接
// 对端）。左侧预置条目（攻击者可控）不在此列——信任边界不可由
// 左侧预置移动。判定谓词由调用方给。
func TrustedXFFRun(entries []string, isTrusted func(net.IP) bool) int {
	run := 0
	for i := len(entries) - 1; i >= 0; i-- {
		ip := ParseIPLiteral(entries[i])
		if ip == nil || !isTrusted(ip) {
			break
		}
		run++
	}
	return run
}

// TrustedXFFChainLen 推导实际可信代理链长 P：从右连续的可信代理
// 条目数 + 直接对端（RemoteAddr 本身就是一跳可信代理）。
// 调用方以 hops <= P 为采信 XFF 的前提；hops > P 即超配，须
// fail-closed 回 RemoteAddr，不得取到预置条目（R24-X-01）。
func TrustedXFFChainLen(entries []string, isTrusted func(net.IP) bool) int {
	return TrustedXFFRun(entries, isTrusted) + 1 // +1：直接对端本身就是一跳可信代理
}
