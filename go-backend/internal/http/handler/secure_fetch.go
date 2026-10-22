package handler

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"go-backend/internal/config"
	"go-backend/internal/logredact"
	"go-backend/internal/netguard"
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
//   - 硬拒层（netguard：元数据精确集 ∪ 169.254/16 整段 ∪ 未指定）：永远拒绝
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

// 硬拒层正本在 internal/netguard（R2 批1收口）：元数据精确集
// {169.254.169.254, 100.100.100.200, fd00:ec2::254} ∪ 169.254.0.0/16
// 整段（R2X-026，verdicts §3.5——不再按厂商逐个枚举补）∪ 未指定 ∪
// 本地 NAT64 整段，翻译内嵌同判。任何配置都不能放行。
// 下列三个名字保留为薄封装，供存量调用点与回归测试使用。

// effectiveFetchIP 返回判定口径上的有效地址（netguard.EffectiveIP：
// mapped 展开、NAT64/6to4 取内嵌 IPv4）。
func effectiveFetchIP(ip net.IP) net.IP {
	return netguard.EffectiveIP(ip)
}

// expandIPv4Mapped 展开 IPv4-mapped IPv6（::ffff:169.254.169.254 → 169.254.169.254）。
func expandIPv4Mapped(ip net.IP) net.IP {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip
}

// isMetadataAddress 检查 ip 是否命中硬拒层（名字沿用 H-15 时期，
// 语义已按 R2X-026 扩为整个硬拒集，不再只是元数据精确名单）。
func isMetadataAddress(ip net.IP) bool {
	return netguard.HardDenyIP(ip)
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
// R2 批1：判定走 netguard——硬拒层（R2X-026 整段口径）先于
// allowlist，非公网判定与翻译提取以 netguard 分类为准。
func validateFetchIP(ip net.IP, rawURL, scheme string) error {
	ip = expandIPv4Mapped(ip)
	ipStr := ip.String()
	r := netguard.Classify(ip)

	// ② 硬拒层（先于 allowlist）。
	if r.HardDeny {
		auditFetchLog("reject", rawURL, ipStr, "metadata_address")
		return fmt.Errorf("metadata address blocked: %s", ipStr)
	}

	// ③ 非公网判定：翻译地址按内嵌 IPv4（judgeIP）查放行策略，
	// 内嵌私网不得绕过 allowlist。
	if r.NonPublic() {
		judgeIP := effectiveFetchIP(ip)
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
		// R2X-028：错误文案不得拼入完整 rawURL（常带 ?token=）；
		// 只带 logredact 口径的脱敏 URL（host+路径模板、query 丢弃）。
		return fmt.Errorf("public http not allowed (use https): %s", logredact.URL(rawURL))
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
		// R2 批1：拨号侧与校验侧同走 netguard（硬拒先行，翻译同判）。
		r := netguard.Classify(ip)
		if r.HardDeny {
			return nil, fmt.Errorf("metadata address blocked at dial: %s", ip)
		}
		if r.NonPublic() && !isPrivateAllowed(effectiveFetchIP(ip)) {
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

// redactedFetchError 是 redactFetchError 的产物：文案已脱敏，
// Unwrap 保留（同样已脱敏的）内层错误链，Timeout 透传原 *url.Error
// 的超时判定，避免改变调用方的错误分类口径。
type redactedFetchError struct {
	msg     string
	cause   error
	timeout bool
}

func (e *redactedFetchError) Error() string   { return e.msg }
func (e *redactedFetchError) Unwrap() error   { return e.cause }
func (e *redactedFetchError) Timeout() bool   { return e.timeout }
func (e *redactedFetchError) Temporary() bool { return e.timeout }

// redactFetchError 在抓取错误离开 secure_fetch 层前统一脱敏
// （R2X-028）：*url.Error 内嵌完整请求 URL（含 ?token= 等查询串），
// 而抓取的全部调用方都默认 err 可安全落盘/回显（remote_source 的
// 定时同步 log.Printf 与 last_error 落库、kernel_external 的导入
// 失败回显），token 曾随之明文外泄。本函数递归走错误链：
//   - *url.Error：URL 换成 logredact.URL 口径（query/userinfo 丢弃、
//     路径疑似 token 段掩码），Op 与内层分类文案（连接拒绝/TLS/
//     超时等）原样保留；
//   - 其余 %w 包装层：前缀文案原样保留，只替换内层已脱敏的部分。
//
// 本文件的主动日志点（fetch_audit / secure_fetch ok）早已走
// logredact；这里补的是错误传播通道，与 notify 侧 AUDIT-R26/R27
// 的 Do 错误脱敏同口径。
func redactFetchError(err error) error {
	out, _ := redactFetchErrorChanged(err)
	return out
}

func redactFetchErrorChanged(err error) (error, bool) {
	if err == nil {
		return nil, false
	}
	if ue, ok := err.(*url.Error); ok {
		inner, _ := redactFetchErrorChanged(ue.Err)
		msg := ue.Op
		if ue.URL != "" {
			msg += " " + logredact.URL(ue.URL)
		}
		if inner != nil {
			msg += ": " + inner.Error()
		}
		return &redactedFetchError{msg: msg, cause: inner, timeout: ue.Timeout()}, true
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		inner, changed := redactFetchErrorChanged(unwrapped)
		if !changed {
			return err, false
		}
		orig, full := unwrapped.Error(), err.Error()
		msg := full
		if strings.HasSuffix(full, orig) {
			msg = full[:len(full)-len(orig)] + inner.Error()
		} else if idx := strings.Index(full, orig); idx >= 0 {
			msg = full[:idx] + inner.Error() + full[idx+len(orig):]
		}
		return &redactedFetchError{msg: msg, cause: inner}, true
	}
	return err, false
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
	// R2X-028：本函数的全部错误出口都经 redactFetchError 收口
	// （校验、建请求、Do、读体四处），保证离开本层的错误不含
	// 请求 URL 的查询串/token。
	if _, err := validateFetchURL(rawURL); err != nil {
		return nil, nil, redactFetchError(err)
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
		return nil, nil, redactFetchError(err)
	}
	req.Header.Set("User-Agent", "flmx-panel/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, redactFetchError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, redactFetchError(err)
	}
	if int64(len(body)) > maxBody {
		return nil, nil, fmt.Errorf("response too large (>%d bytes)", maxBody)
	}

	log.Printf("secure_fetch ok url=%s bytes=%d", logredact.URL(rawURL), len(body))
	return body, resp.Header, nil
}
