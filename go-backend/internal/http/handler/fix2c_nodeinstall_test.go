package handler

import (
	"crypto/tls"
	"net/http"
	"testing"
)

// FIX2-C #2（V6 §4）：nodeInstall 生成的安装命令把站点配置的面板
// 地址原样写进节点 config.json 的 addr。FIX-H 后 agent 对裸地址
// 默认 wss 且禁止自动降级——无 TLS 面板的新装节点首次重启即离线
// 且只能人工改 config。生成侧归一：地址自带 scheme 时映射到 ws
// 家族显式化（http→ws://、https→wss://）；裸地址按面板实际服务
// 方式（本次管理请求的 scheme）补显式 scheme。

func TestFix2CNormalizeNodeInstallAddr(t *testing.T) {
	cases := []struct {
		name   string
		addr   string
		scheme string // 面板实际服务方式（请求 scheme）
		want   string
	}{
		{"bare IPv4, plain panel", "192.0.2.10:6366", "http", "ws://192.0.2.10:6366"},
		{"bare domain, plain panel", "panel.example.com:6366", "http", "ws://panel.example.com:6366"},
		{"bare domain, TLS panel", "panel.example.com:443", "https", "wss://panel.example.com:443"},
		{"bare IPv6 gets brackets", "2001:db8::10", "http", "ws://[2001:db8::10]"},
		{"explicit http wins over TLS request", "http://panel.example.com:8080", "https", "ws://panel.example.com:8080"},
		{"explicit https wins over plain request", "https://panel.example.com:8443", "http", "wss://panel.example.com:8443"},
		{"explicit ws kept", "ws://panel.example.com:8080", "https", "ws://panel.example.com:8080"},
		{"explicit wss kept", "wss://panel.example.com:8443", "http", "wss://panel.example.com:8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeNodeInstallAddr(tc.addr, tc.scheme); got != tc.want {
				t.Fatalf("normalizeNodeInstallAddr(%q, %q) = %q, want %q", tc.addr, tc.scheme, got, tc.want)
			}
		})
	}
}

func TestFix2CPanelRequestScheme(t *testing.T) {
	mkReq := func(remoteAddr string, tlsOn bool, xfp string) *http.Request {
		r := &http.Request{RemoteAddr: remoteAddr, Header: http.Header{}}
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want string
	}{
		{"direct plain", mkReq("203.0.113.5:51000", false, ""), "http"},
		{"direct TLS", mkReq("203.0.113.5:51000", true, ""), "https"},
		{"XFP from trusted local proxy", mkReq("172.30.0.2:51000", false, "https"), "https"},
		{"XFP from loopback proxy", mkReq("127.0.0.1:51000", false, "https"), "https"},
		{"XFP from untrusted client ignored", mkReq("203.0.113.5:51000", false, "https"), "http"},
		{"garbage XFP ignored", mkReq("127.0.0.1:51000", false, "gopher"), "http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := panelRequestScheme(tc.req); got != tc.want {
				t.Fatalf("panelRequestScheme = %q, want %q", got, tc.want)
			}
		})
	}
}
