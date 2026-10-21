// Package logredact 提供日志脱敏工具（A8-01 / D2-01）。
//
// 面板有两类凭据会出现在 URL 中：订阅 token 在路径里
// （/api/v1/open_api/sub/{token}），节点 secret 与 JWT 在 query 里
// （/flow/*?secret=、/system-info?secret=）。任何把 URL/Path 写进日志的
// 位置都必须先过这里脱敏，禁止原文落盘。
package logredact

import (
	"net/url"
	"os"
	"strings"
)

// sensitivePathPrefixes 是路径参数本身即凭据的路由前缀：
// 前缀之后的部分一律掩码，只保留路由模板供排障。
var sensitivePathPrefixes = []string{
	"/api/v1/open_api/sub/",
}

// Path 脱敏请求路径中的凭据段；普通路径原样返回。
func Path(p string) string {
	for _, prefix := range sensitivePathPrefixes {
		if strings.HasPrefix(p, prefix) {
			return prefix + "***"
		}
	}
	return p
}

// exemptPrefixEnv 是 path 段掩码的豁免配置：逗号分隔的路径前缀，
// 命中前缀的 URL 不做段掩码（例如确知某 CDN 路径段是公开资源 ID
// 且排障需要原文时）。双读新旧名与全仓改名约定一致。
const exemptPrefixEnv = "LOG_REDACT_EXEMPT_PREFIXES"

func exemptPathPrefixes() []string {
	raw := os.Getenv("FLMX_" + exemptPrefixEnv)
	if raw == "" {
		raw = os.Getenv("FLVX_" + exemptPrefixEnv)
	}
	if raw == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// looksLikeTokenSegment 判断一个 path 段是否疑似凭据 token：
// 长度 ≥16、仅由 token 字符集（字母/数字/-/_/./~）组成、且字母
// 数字混合。纯单词 slug（含连字符但无数字）与短段（版本号、资源
// 名）不掩——误掩只损失排障信息，故阈值从严；漏掩才是事故。
func looksLikeTokenSegment(seg string) bool {
	if len(seg) < 16 {
		return false
	}
	hasLetter, hasDigit := false, false
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '-' || r == '_' || r == '.' || r == '~':
		default:
			return false
		}
	}
	return hasLetter && hasDigit
}

// maskTokenSegments 把路径中疑似 token 的段逐段掩码。
func maskTokenSegments(p string) string {
	if !strings.Contains(p, "/") {
		return p
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if looksLikeTokenSegment(seg) {
			segs[i] = "***"
		}
	}
	return strings.Join(segs, "/")
}

// URL 把完整 URL 脱敏为「scheme://host + 脱敏路径」：
// query 整体丢弃（凭据常驻于此），userinfo 丢弃，路径先走 Path 的
// 已知前缀规则、再逐段掩码疑似 token 的长随机段。
// FIX2-C #5（V1-N1）：第三方订阅源 URL 的 path 型 token
// （https://provider/link/{token}，机场订阅链接的常见形态）此前
// 不在面板自身前缀名单里，经 secure_fetch/fetch_audit 日志原样
// 落盘；现以通用段规则覆盖，前缀规则继续对面板自身路由生效。
// 解析失败时返回占位符，绝不回退原文。
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<invalid-url>"
	}
	p := Path(u.Path)
	for _, prefix := range exemptPathPrefixes() {
		if strings.HasPrefix(p, prefix) {
			return u.Scheme + "://" + u.Host + p
		}
	}
	return u.Scheme + "://" + u.Host + maskTokenSegments(p)
}
