package handler

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// R24-X-01 回归测试：XFF 信任边界不可由左侧预置移动。
//
// 缺陷：loginClientIP / federationClientIP 以 idx = len(entries) - hops
// 取客户端 IP；hops 超配（大于实际可信代理链长）时 idx 落在攻击者可控的
// 左侧预置 XFF 条目上，IP 维登录限流可规避、联邦 AllowedIPs 可欺骗。
// 修复：实际可信代理链长由右侧连续可信代理条目数 + 直接对端推导；
// hops 超过该链长即 fail-closed 回 RemoteAddr，不得取预置条目。
// 后续变更（B01-001 收窄口径）：判别点改为直接对端类型——公网直连
// 对端的 XFF 完全忽略（轮换自填在该面被杀）；回环/私网代理对端
// 沿用本文件的索引解析与超配 fail-closed，本文件的超配红 repro
// 语义不变。v1 的 run==0 一律 fail-closed 已被取代（误伤默认单跳
// 反代拓扑），hops=1 单条条目的采信期望已恢复，见
// TestR24A_XFFCorrectHopsUnaffected 的复查说明。

// 红 repro 1（L3 探针名）：超配 hops + 预置 XFF，不得取到被骗条目。
func TestR24A_XFFOverconfiguredHopsSpoofedBucket(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "203.0.113.7, 198.51.100.9"))
	if got == nil || got.String() != "127.0.0.1" {
		t.Fatalf("over-configured hops: loginClientIP = %v, want 127.0.0.1 (fail-closed to RemoteAddr)", got)
	}
}

// 红 repro 2（L3 探针名）：逐次轮换预置首条目发登录，限流桶不得被绕过。
func TestR24A_LoginRateLimitEvadedBySeededXFF(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	resetLoginLimitersForTest()
	h := &Handler{}

	blocked := false
	for i := 0; i < 20; i++ {
		xff := fmt.Sprintf("203.0.113.%d, 198.51.100.9", i%250+1)
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", xff)) {
			blocked = true
			if i < 10 {
				t.Fatalf("blocked too early at attempt %d", i+1)
			}
			break
		}
	}
	if !blocked {
		t.Fatal("seeded XFF evaded the IP rate limit: 20 attempts all allowed (want 429 on 11th)")
	}
}

// 红 repro 3（L3 探针名）：超配 hops + 预置 XFF，联邦侧不得取到被骗条目。
func TestR24A_FederationClientIPSpoofedBySeededXFF(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	t.Setenv("FLMX_FEDERATION_TRUSTED_PROXIES", "10.0.0.0/8")
	got := federationClientIP(loginReqWithXFF("10.0.0.5:1", "203.0.113.7, 198.51.100.9"))
	if got == nil || got.String() != "10.0.0.5" {
		t.Fatalf("over-configured hops: federationClientIP = %v, want 10.0.0.5 (fail-closed to RemoteAddr)", got)
	}
}

// 对照 2（新增）：正确 2 跳链不受影响——客户端公网 IP + 代理条目
// 落在可信范围时，仍按 hops 取客户端（合法多跳部署不得被 fail-closed 破坏）。
func TestR24A_XFFCorrectTwoHopChainUnaffected(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	// Caddy(127.0.0.1)→nginx(172.30.0.x)→backend：XFF="client, nginx"
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "203.0.113.7, 172.30.0.5"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("correct 2-hop chain: loginClientIP = %v, want 203.0.113.7", got)
	}
}

// 对照 3（新增）：联邦侧正确 2 跳链不受影响。
func TestR24A_FederationCorrectTwoHopChainUnaffected(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "2")
	t.Setenv("FLMX_FEDERATION_TRUSTED_PROXIES", "10.0.0.0/8")
	got := federationClientIP(loginReqWithXFF("10.0.0.5:1", "203.0.113.7, 10.0.0.9"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("correct 2-hop chain: federationClientIP = %v, want 203.0.113.7", got)
	}
}

// R2 B01-001 回归：XFF 条目数超过 maxXFFEntries 时截断保右弃左。
// 可信代理追加的真实条目在最右侧，永不被截断丢弃；攻击者在左侧
// 塞满假条目、轮换第 64 条不得再移动限流键。
func xff65(rotating string) string {
	parts := make([]string, 0, 65)
	for i := 0; i < 63; i++ {
		parts = append(parts, "203.0.113.1")
	}
	parts = append(parts, rotating)       // 第 64 条（攻击者轮换）
	parts = append(parts, "198.51.100.1") // 第 65 条（模拟代理追加的真实条目）
	return strings.Join(parts, ", ")
}

func TestR2B01_SplitXFFEntriesRightAnchoredTruncation(t *testing.T) {
	entries := splitXFFEntries(xff65("192.0.2.55"))
	if len(entries) != maxXFFEntries {
		t.Fatalf("len(entries) = %d, want %d", len(entries), maxXFFEntries)
	}
	if entries[len(entries)-1] != "198.51.100.1" {
		t.Fatalf("rightmost entry = %q, want 198.51.100.1 (proxy-appended entry must survive truncation)", entries[len(entries)-1])
	}
	if entries[len(entries)-2] != "192.0.2.55" {
		t.Fatalf("second-from-right entry = %q, want 192.0.2.55", entries[len(entries)-2])
	}
}

func TestR2B01_XFFTruncationKeepsRateLimitKeyOnRealClient(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "1")
	// 解析面：第 64 条任意轮换，键恒落在代理追加的真实条目上。
	for _, rot := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		got := loginClientIP(loginReqWithXFF("127.0.0.1:1", xff65(rot)))
		if got == nil || got.String() != "198.51.100.1" {
			t.Fatalf("loginClientIP with 65-entry XFF (rot=%s) = %v, want 198.51.100.1", rot, got)
		}
	}

	// 限流面：轮换第 64 条连打，第 11 次起必须 429。
	resetLoginLimitersForTest()
	h := &Handler{}
	blocked := false
	for i := 0; i < 20; i++ {
		xff := xff65(fmt.Sprintf("192.0.2.%d", i%250+1))
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", xff)) {
			blocked = true
			if i < 10 {
				t.Fatalf("blocked too early at attempt %d", i+1)
			}
			break
		}
	}
	if !blocked {
		t.Fatal("65-entry XFF with rotating 64th entry evaded the IP rate limit (want 429 on 11th)")
	}
}

// 对照（L3 探针名）：正确配置 hops=1 时行为不变——解析真实客户端，
// 第 11 次 429。
// 复查结论（B01-001 收窄）：此处原采信期望编码的是合法单跳形态
// （回环对端 + 代理追加的单条真实 IP），v1 的 fail-closed 期望已
// 按对端类型判别恢复为采信；本对照守护的实质性质（同一来源的
// 失败尝试必落同一桶、第 11 次起 429）不变。自填轮换的封堵面是
// 公网直连对端，见 TestC02_PublicPeerRotatingXFFRateLimited。
func TestR24A_XFFCorrectHopsUnaffected(t *testing.T) {
	t.Setenv("FLMX_TRUSTED_PROXY_HOPS", "1")
	got := loginClientIP(loginReqWithXFF("127.0.0.1:1", "203.0.113.7"))
	if got == nil || got.String() != "203.0.113.7" {
		t.Fatalf("correct hops: loginClientIP = %v, want 203.0.113.7", got)
	}

	resetLoginLimitersForTest()
	h := &Handler{}
	blocked := false
	for i := 0; i < 20; i++ {
		w := httptest.NewRecorder()
		if !h.checkLoginRateLimit(w, loginReqWithXFF("127.0.0.1:54321", "203.0.113.7")) {
			blocked = true
			if i < 10 {
				t.Fatalf("blocked too early at attempt %d", i+1)
			}
			break
		}
	}
	if !blocked {
		t.Fatal("correct hops: 20 attempts all allowed (want 429 on 11th)")
	}
}
