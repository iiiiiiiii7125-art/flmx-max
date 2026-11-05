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

// applySensitivePrefixes 只走已知前缀规则（凭据路由整段掩码）。
func applySensitivePrefixes(p string) string {
	for _, prefix := range sensitivePathPrefixes {
		if strings.HasPrefix(p, prefix) {
			return prefix + "***"
		}
	}
	return p
}

// Path 脱敏请求路径中的凭据段；普通路径原样返回。
// C4-03：前缀规则之外再走通用段掩码——此前 Path 无通用掩码，
// 凭据-in-path 只靠唯一一条前缀规则兜底（当前路由普查下无第二
// 面，属潜伏差集）；与 URL 同谓词同口径后，新出现的 path 型
// 凭据路由不再默认原文落盘。
func Path(p string) string {
	return maskTokenSegments(applySensitivePrefixes(p))
}

// exemptPrefixEnv 是 path 段掩码的豁免配置：逗号分隔的路径前缀，
// 命中前缀的 URL 不做段掩码（例如确知某 CDN 路径段是公开资源 ID
// 且排障需要原文时）。只读新名 FLMX_ 前缀：旧名 FLVX_ 双读已于
// 1.9.10 退场。
const exemptPrefixEnv = "LOG_REDACT_EXEMPT_PREFIXES"

func exemptPathPrefixes() []string {
	raw := os.Getenv("FLMX_" + exemptPrefixEnv)
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

// looksLikeTokenSegment 判断一个 path 段是否疑似凭据 token。
// 字符集必须是 token 字符集（字母/数字/-/_/./~），在此前提下：
//   - 字母+数字混合：长度 ≥8 即掩（C4-03：短 token 如 abc12345
//     此前因 <16 漏掩；普通路径段极少在 8 字符内字母数字混合）；
//   - 纯字母或纯数字、且无分隔符的整段：长度 ≥16 才掩（C4-03：
//     纯字母/纯数字长 token 此前漏掩）。带分隔符的纯字母 slug
//     （getting-started-guide 形态）与短段（版本号、资源名）不
//     掩——误掩只损失排障信息，漏掩才是事故，区分点在分隔符与
//     长度，不无条件全掩。
func looksLikeTokenSegment(seg string) bool {
	if len(seg) < 8 {
		return false
	}
	hasLetter, hasDigit, hasSep := false, false, false
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '-' || r == '_' || r == '.' || r == '~':
			hasSep = true
		default:
			return false
		}
	}
	if hasLetter && hasDigit {
		return true
	}
	if hasSep {
		return false
	}
	// 无分隔符的纯字母/纯数字整段：长串才疑似 token。
	return len(seg) >= 16
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
	// 豁免判定必须在通用段掩码之前：先只走前缀规则，命中豁免
	// 前缀的路径整段保留（Path() 的通用掩码不适用于本函数）。
	p := applySensitivePrefixes(u.Path)
	for _, prefix := range exemptPathPrefixes() {
		if strings.HasPrefix(p, prefix) {
			return u.Scheme + "://" + u.Host + p
		}
	}
	return u.Scheme + "://" + u.Host + maskTokenSegments(p)
}
