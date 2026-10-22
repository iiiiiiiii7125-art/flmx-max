package handler

import (
	"context"
	"crypto/tls"
	"fmt"
	"go-backend/internal/config"
	"go-backend/internal/logredact"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// R5-B5B D17: SecureFetch 分级策略（修正 D16 的 http 订阅被打挂）。
//
// 策略矩阵（默认全部拒绝私网）：
//   - 公网：强制 https，http 一律拒绝
//   - 私网/回环/链路本地：需 FLMX_FETCH_ALLOW_PRIVATE_NETWORKS=true
//     或 FLMX_FETCH_PRIVATE_ALLOWLIST 含对应 CIDR
//   - 元数据地址（169.254.169.254、fd00:ec2::254）：永远拒绝
//
// 检查顺序（写死）：
//  1. scheme 白名单（仅 http/https）
//  2. 元数据黑名单（硬编码，先于 allowlist）
//  3. 逐地址判定：每个 IP 单独走完整链（私网→allowlist→协议）
//  4. 公网必须 https（allowlist 不提升公网协议）
const (
	secureFetchMaxRedirects   = 5
	secureFetchConnectTimeout = 10 * time.Second
	secureFetchTotalTimeout   = 60 * time.Second
)

// secureFetchMaxBody 是 SecureFetch 的响应体上限。
// 为 var 而非 const 是为了让测试能临时调小做快速的超限回归（U1-004）；
// 生产代码不得改动它。
var secureFetchMaxBody = int64(50 * 1024 * 1024) // 50MB

// metadataBlacklist: 云元数据地址，任何配置都不能放行。
// R1 X-P3-32（X-CS1，V-04）：补阿里云元数据 100.100.100.200——此前
// 在 ALLOW_PRIVATE=true / allowlist 含 100.64/10 时可达，与本表
// 「任何配置都不能放行」的自述口径不符。
var metadataBlacklist = []net.IP{
	net.ParseIP("169.254.169.254"),
	net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"),
}

// nat64WellKnownPrefix 是 RFC 6052 公共 NAT64 前缀 64:ff9b::/96，
// 低 32 位内嵌一个 IPv4 地址（R1 X-P3-32）。
var nat64WellKnownPrefix = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("64:ff9b::/96")
	return n
}()

// extractNAT64IPv4 提取 64:ff9b::/96 地址内嵌的 IPv4。
func extractNAT64IPv4(ip net.IP) (net.IP, bool) {
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return nil, false
	}
	if !nat64WellKnownPrefix.Contains(ip16) {
		return nil, false
	}
	return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]), true
}

// effectiveFetchIP 返回判定口径上的有效地址：IPv4-mapped 展开后，
// NAT64 地址取其内嵌 IPv4，使内嵌的元数据/私网地址走同一判定，
// 不再被当作公网 IPv6 放行（R1 X-P3-32）。
func effectiveFetchIP(ip net.IP) net.IP {
	ip = expandIPv4Mapped(ip)
	if embedded, ok := extractNAT64IPv4(ip); ok {
		return embedded
	}
	return ip
}

// expandIPv4Mapped 展开 IPv4-mapped IPv6（::ffff:169.254.169.254 → 169.254.169.254）。
func expandIPv4Mapped(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip
}

// isMetadataAddress 检查是否为元数据地址（展开后）。
// R1 X-P3-32：NAT64 地址按内嵌 IPv4 一并判定。
func isMetadataAddress(ip net.IP) bool {
	ip = expandIPv4Mapped(ip)
	for _, blocked := range metadataBlacklist {
		if ip.Equal(blocked) {
			return true
		}
	}
	if embedded, ok := extractNAT64IPv4(ip); ok {
		for _, blocked := range metadataBlacklist {
			if embedded.Equal(blocked) {
				return true
			}
		}
	}
	return false
}

// getPrivateAllowlist 解析 FLMX_FETCH_PRIVATE_ALLOWLIST（逗号分隔的 CIDR）。
func getPrivateAllowlist() []*net.IPNet {
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
		// 支持单 IP（转为 /32 或 /128）。
		if ip := net.ParseIP(s); ip != nil {
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			_, cidr, _ := net.ParseCIDR(fmt.Sprintf("%s/%d", s, bits))
			if cidr != nil {
				out = append(out, cidr)
			}
			continue
		}
		_, cidr, err := net.ParseCIDR(s)
		if err == nil {
			out = append(out, cidr)
		}
	}
	return out
}

// isPrivateAllowed 检查 IP 是否被 allowlist 放行。
func isPrivateAllowed(ip net.IP) bool {
	// 全局开关。
	if config.GetEnvAny("FLMX_FETCH_ALLOW_PRIVATE_NETWORKS", "FLVX_FETCH_ALLOW_PRIVATE_NETWORKS") == "true" {
		return true
	}
	// CIDR allowlist。
	ip = expandIPv4Mapped(ip)
	for _, cidr := range getPrivateAllowlist() {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// auditFetchLog 审计日志（放行与拒绝都记）。
// A8-01：源 URL 常带 ?token= 类凭据，只记 host+路径模板，禁止原文落盘。
func auditFetchLog(action, rawURL, ip, reason string) {
	log.Printf("fetch_audit action=%s url=%s ip=%s reason=%s", action, logredact.URL(rawURL), ip, reason)
}

// validateFetchIP 逐地址判定（D17 2.1）：每个 IP 单独走完整链。
// 返回 nil 表示通过，error 表示拒绝（已记审计）。
func validateFetchIP(ip net.IP, rawURL, scheme string) error {
	ip = expandIPv4Mapped(ip)
	ipStr := ip.String()

	// ② 元数据黑名单（先于 allowlist）。
	if isMetadataAddress(ip) {
		auditFetchLog("reject", rawURL, ipStr, "metadata_address")
		return fmt.Errorf("metadata address blocked: %s", ipStr)
	}

	// ③ 私网/特殊地址判定。R1 X-P3-32：NAT64 地址按内嵌 IPv4
	// 判定（judgeIP），内嵌私网不得绕过 allowlist。
	judgeIP := effectiveFetchIP(ip)
	if isPrivateIP(judgeIP) {
		if !isPrivateAllowed(judgeIP) {
			auditFetchLog("reject", rawURL, ipStr, "private_not_allowlisted")
			return fmt.Errorf("private network not allowlisted: %s", ipStr)
		}
		// 放行：记审计。
		auditFetchLog("allow_private", rawURL, ipStr, "allowlisted")
		return nil
	}

	// ⑤ 公网必须 https（allowlist 不提升公网协议）。
	if scheme != "https" {
		auditFetchLog("reject", rawURL, ipStr, "public_http_not_allowed")
		return fmt.Errorf("public http not allowed (use https): %s", rawURL)
	}
	return nil
}

// validateFetchURL 校验 URL（D17 检查顺序）。
func validateFetchURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	// ① scheme 白名单。
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed: %w", err)
	}
	// ③ 逐地址判定：任一不合法即整体拒绝。
	for _, ip := range ips {
		if err := validateFetchIP(ip, rawURL, u.Scheme); err != nil {
			return nil, err
		}
	}
	return u, nil
}

// secureFetchDialContext 自定义拨号（D17 2.2）：
// 先校验全部 IP，再按序尝试拨号；保留 Host 头与 TLS SNI。
func secureFetchDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// 拨号时重解析（防 DNS rebinding TOCTOU）。
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed at dial: %w", err)
	}
	// D17 2.2: 先校验全部，再尝试拨号。不能拨未校验的。
	// 注意：dial 时不知道原 scheme，用最严格的 https 要求公网。
	// （实际 scheme 已在 validateFetchURL 校验，此处只做 IP 安全校验。）
	for _, ip := range ips {
		ip = expandIPv4Mapped(ip)
		if isMetadataAddress(ip) {
			return nil, fmt.Errorf("metadata address blocked at dial: %s", ip)
		}
		// R1 X-P3-32：拨号侧同口径，NAT64 按内嵌 IPv4 判定。
		if judgeIP := effectiveFetchIP(ip); isPrivateIP(judgeIP) && !isPrivateAllowed(judgeIP) {
			return nil, fmt.Errorf("private IP blocked at dial: %s", ip)
		}
	}
	// 按序尝试拨号（全部已校验）。
	dialer := &net.Dialer{Timeout: secureFetchConnectTimeout}
	var lastErr error
	for _, ip := range ips {
		ip = expandIPv4Mapped(ip)
		target := net.JoinHostPort(ip.String(), port)
		conn, err := dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("all dial attempts failed: %w", lastErr)
}

// SecureFetch 安全下载（R5-B5B D17）。
func SecureFetch(ctx context.Context, rawURL string) ([]byte, error) {
	body, _, err := SecureFetchWithHeader(ctx, rawURL)
	return body, err
}

// SecureFetchWithHeader 同 SecureFetch，额外返回响应头。
func SecureFetchWithHeader(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return secureFetchWithHeader(ctx, rawURL, secureFetchMaxBody)
}

// secureFetchWithHeaderLimit 同 SecureFetchWithHeader，但响应体上限由
// 调用点指定。R1 顺手项（C 观察 1，V-23）：订阅抓取的领域上限
// （maxSubscriptionBytes = 5MB）远小于全局 50MB，此前无从在抓取层
// 生效、只能整包下载后再判定，恶意/异常源可逼面板先吞 50MB。
func secureFetchWithHeaderLimit(ctx context.Context, rawURL string, maxBody int64) ([]byte, http.Header, error) {
	return secureFetchWithHeader(ctx, rawURL, maxBody)
}

func secureFetchWithHeader(ctx context.Context, rawURL string, maxBody int64) ([]byte, http.Header, error) {
	if _, err := validateFetchURL(rawURL); err != nil {
		return nil, nil, err
	}

	client := &http.Client{
		Timeout: secureFetchTotalTimeout,
		Transport: &http.Transport{
			DialContext: secureFetchDialContext,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				// SNI 由 http.Transport 根据 URL host 自动设置（拨 IP 时保留）。
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= secureFetchMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			// D17 2.3: 每跳重跑全链。
			if _, err := validateFetchURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			// D17 2.3: 跨源剥离凭据。
			if len(via) > 0 && req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "flmx-panel/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > maxBody {
		return nil, nil, fmt.Errorf("response too large (>%d bytes)", maxBody)
	}

	log.Printf("secure_fetch ok url=%s bytes=%d", logredact.URL(rawURL), len(body))
	return body, resp.Header, nil
}
