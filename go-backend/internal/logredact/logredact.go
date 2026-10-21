// Package logredact 提供日志脱敏工具（A8-01 / D2-01）。
//
// 面板有两类凭据会出现在 URL 中：订阅 token 在路径里
// （/api/v1/open_api/sub/{token}），节点 secret 与 JWT 在 query 里
// （/flow/*?secret=、/system-info?secret=）。任何把 URL/Path 写进日志的
// 位置都必须先过这里脱敏，禁止原文落盘。
package logredact

import (
	"net/url"
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

// URL 把完整 URL 脱敏为「scheme://host + 路径模板」：
// query 整体丢弃（凭据常驻于此），userinfo 丢弃，路径走 Path 脱敏。
// 解析失败时返回占位符，绝不回退原文。
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<invalid-url>"
	}
	return u.Scheme + "://" + u.Host + Path(u.Path)
}
