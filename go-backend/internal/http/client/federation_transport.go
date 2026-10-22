package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/config"
	"go-backend/internal/netguard"
)

// 联邦出站客户端的 SSRF 防护（D3-01）。此前 NewFederationClient 是裸
// http.Client：无 CheckRedirect、无拨号期复检——import 期的一次性
// IsSafeRemoteAddr 校验可被 302 跳转与 DNS rebinding 绕过，且存量
// RemoteURL 被状态同步/用量预取长期无复检地使用（第 16 轮记录在
// 1.3.2 原样存在）。
//
// 本实现按 SecureFetch 的 pinned-dial 模式（handler/secure_fetch.go）：
//   - 每次拨号先全量解析主机名，逐地址判定，任一不安全整体拒绝；
//   - 校验与拨号共用同一次解析结果并拨已钉死的 IP（TLS SNI 与 Host
//     头仍按原主机名），rebinding 在校验与使用之间没有窗口；
//   - 元数据地址（169.254.169.254 / fd00:ec2::254）永远拒绝，allowlist
//     不能放行；
//   - 其余私网/回环/链路本地/组播/未指定地址默认拒绝，但与 SecureFetch
//     共用同一放行策略（FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true 或
//     FLMX_FETCH_PRIVATE_ALLOWLIST 含对应 CIDR）——内网/Tailscale 联邦
//     是真实部署形态，运维可显式放行，与订阅抓取一处口径；
//   - 重定向逐跳复检（上限 5 跳），跨主机剥离 Authorization 与
//     X-Panel-Domain（Go 默认只剥 Authorization 的子集情形）。
//
// IP 判定与 handler 共用 internal/netguard 正本（R2 批1收口，此前
// 本文件是 handler.isPrivateIP 的物理镜像副本，NAT64 与阿里云
// 元数据两处曾漏同步——R2X-006 本体）。

const federationMaxRedirects = 5

// federationLookupIP 是联邦客户端的解析入口（默认 net.LookupIP），
// 抽成 var 以便测试注入假解析（含 rebinding 场景）。
var federationLookupIP = net.LookupIP

// safetyBypassFunc 由 handler 包在 init 时注入，桥接既有的
// DisableSafeRemoteAddrCheckForTesting（契约测试以 127.0.0.1 的
// httptest 充当对端面板）。生产恒为 nil 或恒返回 false。
var safetyBypassFunc func() bool

// SetSafetyBypassFunc 注册测试旁路判定；传 nil 清除。
func SetSafetyBypassFunc(f func() bool) {
	safetyBypassFunc = f
}

func federationSafetyBypassed() bool {
	return safetyBypassFunc != nil && safetyBypassFunc()
}

// federationIsMetadataIP 现语义为 netguard 硬拒层（R2 批1收口）：
// 元数据精确集（含阿里云 100.100.100.200，旧镜像名单缺此条）∪
// 169.254/16 整段 ∪ 未指定 ∪ 本地 NAT64，翻译内嵌同判。
// fe80::/10 不在此列——归 ClassLinkLocal，走 NonPublic + allowlist
// 门（R3X-010 政策分层）。
func federationIsMetadataIP(ip net.IP) bool {
	return netguard.HardDenyIP(ip)
}

// federationIsPrivateIP 现语义为 netguard 非公网判定（段集含
// bypass 基底，NAT64/6to4 按内嵌同判）。
func federationIsPrivateIP(ip net.IP) bool {
	return netguard.IsNonPublicIP(ip)
}

// federationPrivateAllowlist 解析与 SecureFetch 相同的放行配置
// （FLMX_FETCH_PRIVATE_ALLOWLIST，逗号分隔 CIDR 或单 IP）。
func federationPrivateAllowlist() []*net.IPNet {
	raw := config.GetEnvAny("FLMX_FETCH_PRIVATE_ALLOWLIST", "FLVX_FETCH_PRIVATE_ALLOWLIST")
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			_, cidr, err := net.ParseCIDR(fmt.Sprintf("%s/%d", s, bits))
			if err == nil && cidr != nil {
				out = append(out, cidr)
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(s); err == nil {
			out = append(out, cidr)
		}
	}
	return out
}

func federationPrivateAllowed(ip net.IP) bool {
	if config.GetEnvAny("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "FLVX_FETCH_ALLOW_PRIVATE_NETWORKS") == "true" {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	for _, cidr := range federationPrivateAllowlist() {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// validateFederationIPs 逐地址判定：硬拒恒拒（先于放行策略，
// R2X-006）；非公网地址未被放行策略命中即拒（放行按判定口径的
// 内嵌地址查 allowlist）。任一地址不合法整体拒绝（与
// validateFetchURL 的口径一致，防止多 A 记录里混入内网地址的
// 选择性命中）。
func validateFederationIPs(host string, ips []net.IP) error {
	if len(ips) == 0 {
		return fmt.Errorf("federation target %q did not resolve", host)
	}
	for _, ip := range ips {
		if federationIsMetadataIP(ip) {
			return fmt.Errorf("federation target %q resolves to a metadata address, blocked", host)
		}
		if federationIsPrivateIP(ip) && !federationPrivateAllowed(netguard.EffectiveIP(ip)) {
			return fmt.Errorf("federation target %q resolves to private/reserved address %s, blocked", host, ip.String())
		}
	}
	return nil
}

// resolveFederationHost 解析并校验主机，返回已校验的 IP 列表（拨号钉死用）。
func resolveFederationHost(host string) ([]net.IP, error) {
	ips, err := federationLookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("federation target %q lookup failed: %w", host, err)
	}
	if err := validateFederationIPs(host, ips); err != nil {
		return nil, err
	}
	return ips, nil
}

// federationDialContext 是钉死拨号：解析→全量校验→按序拨已校验 IP。
func federationDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if federationSafetyBypassed() {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := resolveFederationHost(host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("federation dial %s: all validated addresses failed: %w", addr, lastErr)
}

// checkFederationRedirect 逐跳复检重定向目标并跨主机剥离凭据头。
func checkFederationRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= federationMaxRedirects {
		return fmt.Errorf("too many redirects")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("redirect blocked: unsupported scheme %q", req.URL.Scheme)
	}
	if !federationSafetyBypassed() {
		host := req.URL.Hostname()
		if host == "" {
			return fmt.Errorf("redirect blocked: empty host")
		}
		if _, err := resolveFederationHost(host); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		req.Header.Del("Authorization")
		req.Header.Del("X-Panel-Domain")
	}
	return nil
}

// newFederationHTTPClient 构造带 SSRF 防护的联邦客户端。
func newFederationHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Proxy 必须关闭：经代理出站时目标主机由代理解析，本层的解析校验
	// 与 IP 钉死全部失效（与 SecureFetch 不设 Proxy 同口径）。这也意味
	// 联邦客户端不再响应 HTTP(S)_PROXY 环境变量——属有意的 SSRF 姿态。
	transport.Proxy = nil
	transport.DialContext = federationDialContext
	// 禁用连接复用：联邦调用是低频控制面请求，逐次新建连接意味着逐次
	// 重新解析+校验+钉死（存量 URL 在每次状态同步/用量预取时都被复检，
	// 这是 D3-01 的核心要求）；复用连接会让 rebinding 在连接池存活期
	// 内绕过复检。
	transport.DisableKeepAlives = true
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: checkFederationRedirect,
	}
}
