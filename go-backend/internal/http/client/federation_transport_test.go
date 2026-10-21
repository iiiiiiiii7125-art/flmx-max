package client

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// D3-01: 联邦出站客户端此前是裸 http.Client——无 CheckRedirect、无
// 拨号期复检，import 期一次性 IsSafeRemoteAddr 可被 302 跳转与 DNS
// rebinding 绕过。本文件锁定修复后的行为：
//   - 直连私网/元数据目标在拨号期被拒（存量 URL 每次调用都复检）；
//   - 重定向逐跳复检，跳向元数据/私网必拒；
//   - 校验通过后拨已钉死 IP（DNS rebinding 在校验与拨号之间无窗口）；
//   - 测试旁路（SetSafetyBypassFunc）与 SecureFetch 私网放行策略
//     （FLMX_FETCH_PRIVATE_ALLOWLIST）两条既有通道保持可用。

func connectOKServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"msg":  "ok",
			"data": map[string]interface{}{
				"shareId": 7, "nodeName": "peer", "serverIp": "203.0.113.1", "status": 1,
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stubFederationLookup(t *testing.T, table map[string][]net.IP) {
	t.Helper()
	orig := federationLookupIP
	federationLookupIP = func(host string) ([]net.IP, error) {
		if ips, ok := table[host]; ok {
			return ips, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host}
	}
	t.Cleanup(func() { federationLookupIP = orig })
}

func TestFederationClientBlocksDirectPrivateTarget(t *testing.T) {
	srv := connectOKServer(t)
	fc := NewFederationClient()
	if _, err := fc.Connect(srv.URL, "token", ""); err == nil {
		t.Fatal("direct connection to a loopback panel URL must be blocked at dial time")
	}
}

func TestFederationClientBlocksMetadataTarget(t *testing.T) {
	stubFederationLookup(t, map[string][]net.IP{
		"metadata.test": {net.ParseIP("169.254.169.254")},
	})
	// 即便运维误把元数据段放进私网 allowlist，元数据也必须恒拒。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "169.254.0.0/16")
	fc := NewFederationClient()
	if _, err := fc.Connect("http://metadata.test:6365", "token", ""); err == nil {
		t.Fatal("metadata address must always be blocked")
	}
}

func TestFederationClientBlocksRedirectToPrivate(t *testing.T) {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal.test:6399/api/v1/federation/connect", http.StatusFound)
	}))
	t.Cleanup(front.Close)

	stubFederationLookup(t, map[string][]net.IP{
		"front.test":    {net.ParseIP("127.0.0.1")},
		"internal.test": {net.ParseIP("192.168.9.9")},
	})
	frontPort := front.Listener.Addr().(*net.TCPAddr).Port
	fc := NewFederationClient()

	// 首跳 front.test 解析到 127.0.0.1，不在 allowlist（仅 203.0.113.0/24）
	// 内时首跳即应被拒——验证「allowlist 外私网恒拒」。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "203.0.113.0/24")
	if _, err := fc.Connect("http://front.test:"+strconv.Itoa(frontPort), "token", ""); err == nil {
		t.Fatal("private target outside the allowlist must be blocked")
	}

	// 放行首跳后（内网联邦场景），重定向跳向未放行的内网主机必须在
	// 跳转校验处被拦下——这是 D3-01 的 302 绕过向量本体。
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")
	_, err := fc.Connect("http://front.test:"+strconv.Itoa(frontPort), "token", "")
	if err == nil {
		t.Fatal("redirect to a non-allowlisted private target must be blocked")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("expected a redirect block error, got: %v", err)
	}
}

func TestFederationClientPinnedDialReachesValidatedIP(t *testing.T) {
	srv := connectOKServer(t)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	stubFederationLookup(t, map[string][]net.IP{
		"provider.test": {net.ParseIP("127.0.0.1")},
	})
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")

	fc := NewFederationClient()
	info, err := fc.Connect("http://provider.test:"+strconv.Itoa(port), "token", "")
	if err != nil {
		t.Fatalf("pinned dial to validated IP should succeed: %v", err)
	}
	if info == nil || info.ShareID != 7 {
		t.Fatalf("unexpected connect info: %+v", info)
	}
}

func TestFederationClientRebindingBetweenCheckAndDial(t *testing.T) {
	// 模拟 rebinding 的现实形态：首次调用（import 期校验+连接）拿到
	// 合法地址，存量 URL 在后续状态同步时被重解析为内网地址。钉死
	// 拨号每次连接都用同一次解析完成「校验+拨号」，翻转后的私网
	// 地址必须在第二次调用当场被拒——而不是像旧实现那样只在 import
	// 期查一次、之后长期无复检地使用。
	srv := connectOKServer(t)
	flipped := false
	orig := federationLookupIP
	federationLookupIP = func(host string) ([]net.IP, error) {
		if !flipped {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("192.168.9.9")}, nil
	}
	t.Cleanup(func() { federationLookupIP = orig })
	t.Setenv("FLMX_FETCH_PRIVATE_ALLOWLIST", "127.0.0.0/8")

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	fc := NewFederationClient()
	if _, err := fc.Connect("http://rebind.test:"+strconv.Itoa(port), "token", ""); err != nil {
		t.Fatalf("first connect with validated address should succeed: %v", err)
	}
	flipped = true
	if _, err := fc.Connect("http://rebind.test:"+strconv.Itoa(port), "token", ""); err == nil {
		t.Fatal("rebinding to a private address must be rejected on the next call")
	}
}

func TestFederationClientTestingBypass(t *testing.T) {
	srv := connectOKServer(t)
	SetSafetyBypassFunc(func() bool { return true })
	t.Cleanup(func() { SetSafetyBypassFunc(nil) })

	fc := NewFederationClient()
	info, err := fc.Connect(srv.URL, "token", "")
	if err != nil {
		t.Fatalf("testing bypass should restore plain client behavior: %v", err)
	}
	if info == nil || info.NodeName != "peer" {
		t.Fatalf("unexpected connect info: %+v", info)
	}
}
