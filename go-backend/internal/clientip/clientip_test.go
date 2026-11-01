package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// R5-FC（L2-09 / L8-F-02）：限流三面共用的客户端 IP 解析回归门。
// 模型与历史登录实现一致：公网直连忽略 XFF、单跳反代右锚解析、
// 跳数超配 fail-closed、不可解析返回 nil/false 供调用方记账。

func reqWith(remoteAddr, xff string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remoteAddr
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func TestFromRequestPublicPeerIgnoresXFF(t *testing.T) {
	got := FromRequest(reqWith("203.0.113.5:1234", "1.2.3.4"))
	if got == nil || got.String() != "203.0.113.5" {
		t.Fatalf("公网直连须忽略 XFF，got %v", got)
	}
}

func TestFromRequestSingleHopProxyResolvesClient(t *testing.T) {
	got := FromRequest(reqWith("127.0.0.1:6365", "203.0.113.7"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("单跳反代应解析出 XFF 客户端，got %v", got)
	}
}

func TestFromRequestRightAnchoredAgainstPrependedEntries(t *testing.T) {
	// 攻击者在左侧预置条目不得移动限流键：hops=1 时取最右条目
	// （代理实际追加的那个），而非预置的 1.1.1.1。
	got := FromRequest(reqWith("127.0.0.1:6365", "1.1.1.1, 203.0.113.7"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("右锚解析应取代理追加条目，got %v", got)
	}
}

func TestFromRequestOverConfiguredHopsFailClosed(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "3")
	// 实际链长 1（仅代理对端），配置 3 跳 → fail-closed 回 RemoteAddr。
	got := FromRequest(reqWith("127.0.0.1:6365", "203.0.113.7"))
	if got == nil || got.String() != "127.0.0.1" {
		t.Fatalf("跳数超配须 fail-closed 回 RemoteAddr，got %v", got)
	}
}

func TestRateLimitKeyUnresolved(t *testing.T) {
	if ip := FromRequest(reqWith("not-an-addr", "")); ip != nil {
		t.Fatalf("非法 RemoteAddr 应解析不出 IP，got %v", ip)
	}
	key, ok := RateLimitKey(reqWith("not-an-addr", ""))
	if ok || key != "" {
		t.Fatalf("非法 RemoteAddr 的限流键应为未解析，got (%q, %v)", key, ok)
	}
	// host:port 正常形态可解析且剥掉端口（防端口轮换绕过）。
	key, ok = RateLimitKey(reqWith("203.0.113.5:1234", ""))
	if !ok || key != "203.0.113.5" {
		t.Fatalf("host:port 应解析为纯 IP 键，got (%q, %v)", key, ok)
	}
}
