package handler

import (
	"database/sql"
	"strings"
	"testing"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R25-X-01（产品 P2）：节点 ServerIP 含端口后缀（如测试机实值
// 16.146.199.68:6366）时，写侧 IsValidNodeAddress 明文放行
// host:port，读侧 auto 模式把 ServerIP 原样当分享 host，
// ValidateImportHost 拒之，inboundEndpoints 逐次跳过该节点全部
// 端点——默认订阅全格式拉取为空，且 200 空档完全静默。
// 修复 (a)：分享地址派生时剥离端口；守卫本身不动。

// 修复 (a) 口径下的写侧锚点：IsValidNodeAddress 仍放行 host:port
// （写侧不收紧，无 BC）。
func TestR25L3BNodeAddressAcceptsHostPort(t *testing.T) {
	for _, addr := range []string{
		"16.146.199.68:6366",
		"[2001:db8::1]:6366",
		"node.example.com:6366",
		"16.146.199.68",
		"2001:db8::1",
	} {
		if err := IsValidNodeAddress(addr); err != nil {
			t.Fatalf("IsValidNodeAddress(%q) = %v, want nil", addr, err)
		}
	}
}

// 分享地址派生剥离端口：auto/ipv4/ipv6 三档均以裸 host 为结果；
// 裸 host、裸 IP、括号 IPv6（无端口）原样通过。
func TestR25ShareHostPortStrippedAtDerivation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		node *model.Node
		want string
	}{
		{"auto host:port", "auto", &model.Node{ServerIP: "16.146.199.68:6366"}, "16.146.199.68"},
		{"auto bracketed v6:port", "auto", &model.Node{ServerIP: "[2001:db8::1]:6366"}, "2001:db8::1"},
		{"auto hostname:port", "auto", &model.Node{ServerIP: "node.example.com:6366"}, "node.example.com"},
		{"auto bare ip", "auto", &model.Node{ServerIP: "16.146.199.68"}, "16.146.199.68"},
		{"auto bracketed v6", "auto", &model.Node{ServerIP: "[2001:db8::1]"}, "2001:db8::1"},
		{"auto bare v6", "auto", &model.Node{ServerIP: "2001:db8::1"}, "2001:db8::1"},
		{"ipv4 from host:port ServerIP", "ipv4", &model.Node{ServerIP: "16.146.199.68:6366"}, "16.146.199.68"},
		{"ipv6 from bracketed v6:port ServerIP", "ipv6", &model.Node{ServerIP: "[2001:db8::1]:6366"}, "2001:db8::1"},
		{"ipv4 explicit v4 wins", "ipv4", &model.Node{ServerIP: "16.146.199.68:6366", ServerIPV4: sql.NullString{String: "192.0.2.1", Valid: true}}, "192.0.2.1"},
		{"auto fallback to v4", "auto", &model.Node{ServerIPV4: sql.NullString{String: "192.0.2.1", Valid: true}}, "192.0.2.1"},
	} {
		row := &model.NodeInbound{ShareAddressMode: tc.mode}
		if got := inboundShareAddress(row, tc.node); got != tc.want {
			t.Errorf("%s: inboundShareAddress = %q, want %q", tc.name, got, tc.want)
		}
	}
	// 剥离后仍须过守卫标准：回环 host:port 剥出回环，仍被拒。
	if err := validateShareHostValue(inboundShareAddress(&model.NodeInbound{}, &model.Node{ServerIP: "127.0.0.1:6366"})); err == nil {
		t.Fatal("stripped loopback must still be rejected by the guard")
	}
}

// 全链红 repro：ServerIP 带端口的节点经 inboundEndpoints 必须产出
// 端点（修复前：0 端点 + "invalid share host" 日志）。
func TestR25InboundEndpointsHostPortServerIP(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/share-hostport.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "hostport", "16.146.199.68:6366")
	row := seedShareInbound(t, r, nodeID, "hp-in")
	logs := captureHandlerLogs(t)
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	if len(endpoints) != 1 || len(kinds) != 1 || kinds[0] != "direct" {
		t.Fatalf("host:port ServerIP must yield 1 direct endpoint, got %+v kinds=%v", endpoints, kinds)
	}
	if endpoints[0].Host != "16.146.199.68" {
		t.Fatalf("endpoint host = %q, want %q", endpoints[0].Host, "16.146.199.68")
	}
	if strings.Contains(logs.String(), "invalid share host") {
		t.Fatalf("the endpoint must not be skipped, logs: %q", logs.String())
	}
}
