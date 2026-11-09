package kernel

import (
	"strings"
	"testing"
)

func namedEndpoints(names ...string) []Endpoint {
	out := make([]Endpoint, len(names))
	for i, n := range names {
		out[i] = Endpoint{Name: n, Host: "203.0.113.1", Port: 8388 + i, Inbound: Inbound{
			ID: int64(i + 1), Protocol: ProtocolShadowsocks,
			Settings: Settings{Method: "aes-256-gcm", Password: "pw"},
		}}
	}
	return out
}

// chainEndpoints 是模板等测试共用的四端点夹具（名字沿用历史：一台
// ShadowTLS 落地 + 名称带「中转」字样的端点；订阅链式删除后它只是
// 普通端点集，不再产生任何 ChainRole 标记）。
func chainEndpoints() []Endpoint {
	eps := namedEndpoints("美国 落地 01", "美国 落地 02", "香港 中转", "日本 直连")
	// A ShadowTLS landing node: only its shadowtls outbound dials.
	eps[1].Inbound.Security = Security{Type: SecurityShadowTLS, SNI: "www.example.com", ShadowTLSPassword: "stlspassword",
		Fingerprint: "chrome"}
	return eps
}

func endpointNames(eps []Endpoint) []string {
	out := make([]string, len(eps))
	for i, e := range eps {
		out[i] = e.Name
	}
	return out
}

// 中转范围三档（2.1.2）：Lines 是唯一的订阅级过滤——"" 全部、
// "direct" 只直连、"relay" 只经中转（分类口径＝Endpoint.Direct）。
func TestSubOptionsLinesScope(t *testing.T) {
	eps := namedEndpoints("香港 01", "日本 01", "美国 01", "Plain")
	eps[1].Direct = true

	names := func(opts SubOptions) string { return strings.Join(endpointNames(ApplySubOptions(eps, opts)), ",") }

	if got := names(SubOptions{}); got != "香港 01,日本 01,美国 01,Plain" {
		t.Fatalf("all: %s", got)
	}
	if got := names(SubOptions{Lines: "direct"}); got != "日本 01" {
		t.Fatalf("direct only: %s", got)
	}
	if got := names(SubOptions{Lines: "relay"}); got != "香港 01,美国 01,Plain" {
		t.Fatalf("relay only: %s", got)
	}
	// 未知档位值不收紧（存量杂值不得把订阅静默过滤为空）。
	if got := names(SubOptions{Lines: "bogus"}); got != "香港 01,日本 01,美国 01,Plain" {
		t.Fatalf("unknown scope keeps all: %s", got)
	}
}
