package handler

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"
)

// R5-B5 D16: 统一安全下载函数。
//
// 所有出站下载/抓取必须走此函数：面板升级 compose、agent 升级、
// 订阅源抓取、导入订阅 URL、校验文件下载。
//
// 安全属性：
//   - 强制 https（测试可用 SecureFetchAllowHTTP 显式放行）
//   - 重定向逐跳校验（每跳都验域名/IP，禁止跳到内网/元数据/回环）
//   - 响应大小上限（含解压后上限，防 zip bomb）
//   - 超时（连接 10s，总 60s）
//   - DNS 解析后校验实际 IP（防 DNS rebinding）
//   - 覆盖 IPv4-mapped IPv6、0.0.0.0、169.254.169.254、私网段
const (
	secureFetchMaxBody       = 50 * 1024 * 1024 // 50MB
	secureFetchMaxRedirects   = 5
	secureFetchConnectTimeout = 10 * time.Second
	secureFetchTotalTimeout   = 60 * time.Second
)

// SecureFetchAllowHTTP 测试用：允许 http（默认 false）。
var SecureFetchAllowHTTP = false

// validateFetchURL 校验 URL 合法性（scheme + host 解析 + IP 检查）。
func validateFetchURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" && !(SecureFetchAllowHTTP && u.Scheme == "http") {
		return nil, fmt.Errorf("only https allowed (got %s)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	// 解析 host 为 IP（可能是域名或 IP 字面量）。
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed: %w", err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("blocked private IP %s for host %s", ip, host)
		}
	}
	return u, nil
}

// secureFetchDialContext 自定义拨号：连接前再次校验 IP（防 DNS rebinding TOCTOU）。
func secureFetchDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// 重新解析（连接时的实际 IP）。
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed at dial: %w", err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("blocked private IP %s at dial (DNS rebinding?)", ip)
		}
	}
	dialer := &net.Dialer{Timeout: secureFetchConnectTimeout}
	return dialer.DialContext(ctx, network, addr)
}

// SecureFetch 安全下载（R5-B5 D16）。
// 返回响应体（已限制大小）。调用方负责处理内容。
func SecureFetch(ctx context.Context, rawURL string) ([]byte, error) {
	body, _, err := SecureFetchWithHeader(ctx, rawURL)
	return body, err
}

// SecureFetchWithHeader 同 SecureFetch，额外返回响应头（用于 Subscription-Userinfo 等）。
func SecureFetchWithHeader(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	// 初始 URL 校验。
	if _, err := validateFetchURL(rawURL); err != nil {
		return nil, nil, err
	}

	// 自定义 client：逐跳校验重定向，自定义拨号防 rebinding。
	client := &http.Client{
		Timeout: secureFetchTotalTimeout,
		Transport: &http.Transport{
			DialContext: secureFetchDialContext,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= secureFetchMaxRedirects {
				return fmt.Errorf("too many redirects")
			}
			// 每跳都校验。
			if _, err := validateFetchURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "flvx-panel/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	// 限制大小（防超大响应 + 解压炸弹）。
	limited := io.LimitReader(resp.Body, secureFetchMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > secureFetchMaxBody {
		return nil, nil, fmt.Errorf("response too large (>%d bytes)", secureFetchMaxBody)
	}

	log.Printf("secure_fetch ok url=%s bytes=%d", rawURL, len(body))
	return body, resp.Header, nil
}
