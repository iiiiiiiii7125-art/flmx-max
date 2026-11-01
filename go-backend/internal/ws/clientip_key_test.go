package ws

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/clientip"
)

// L8-F-02 / R5-FC：WS 鉴权失败限流键走可信代理链解析，与登录、
// gh 镜像三面同口径；解析不出时记入共享 fail-closed 桶。
func TestWSClientIPUsesTrustedProxyChain(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/system-info", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := wsClientIP(r); got != "203.0.113.9" {
		t.Fatalf("反代后应按 XFF 真实客户端分桶，got %q", got)
	}

	// 公网直连：XFF 不可信，键恒为对端 IP。
	r2 := httptest.NewRequest(http.MethodGet, "/system-info", nil)
	r2.RemoteAddr = "203.0.113.5:5555"
	r2.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := wsClientIP(r2); got != "203.0.113.5" {
		t.Fatalf("公网直连须忽略 XFF，got %q", got)
	}

	// 不可解析的 RemoteAddr：共享桶（fail-closed 记账），
	// 不再以含端口的原始串为键。
	r3 := httptest.NewRequest(http.MethodGet, "/system-info", nil)
	r3.RemoteAddr = "garbage"
	if got := wsClientIP(r3); got != clientip.UnresolvedKey {
		t.Fatalf("不可解析地址应记入共享桶，got %q", got)
	}
}
