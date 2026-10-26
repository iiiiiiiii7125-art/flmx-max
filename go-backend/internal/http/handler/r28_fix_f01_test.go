package handler

import (
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R28-L3-E01 / R28-FIX-F01（产品 P2）：ServerIP 含端口（如
// 16.146.199.68:6366）时中转面双面失真。写侧 IsValidNodeAddress 显式
// 放行 host:port；R25-X-01 只修了订阅直连面，中转面两处漏修：
// (1) relayTargetWithExits 把 ServerIP 原值进 JoinHostPort，产出
// 不可拨号的 [IP:port]:port；(2) resolveForwardIngress /
// ForwardEntries 的入口 host 含 :port，被订阅渲染守卫静默丢弃。
// 本文件探针先红后绿；断言更新 0 条（新增测试文件，不改既有断言）。

// TestR28L3ERelayTargetStripsServerIPPort 是 L3 复现探针口径重建：
// ServerIP=203.0.113.10:6366 时 relayTarget 得 "[203.0.113.10:6366]:443"
// （R28-L3-e01-repro.log 原样），want 203.0.113.10:443。三腿：
// type-2 出口他节点、type-1 通配监听、括号 IPv6 含端口。
func TestR28L3ERelayTargetStripsServerIPPort(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r28-f01-relay-target.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	front := seedRelayTargetNode(t, r, "front", "198.51.100.1")
	ix := seedRelayTargetNode(t, r, "ix", "198.51.100.2")
	landing := seedRelayTargetNode(t, r, "landing", "203.0.113.10:6366")
	landingV6 := seedRelayTargetNode(t, r, "landing-v6", "[2001:db8::1]:6366")

	viaIX := &tunnelRecord{ID: seedRelayTargetTunnel(t, r, "via-ix", front, 31000, ix), Type: 2}
	now := time.Now().UnixMilli()
	t1 := model.Tunnel{Name: "type1", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&t1).Error; err != nil {
		t.Fatal(err)
	}
	type1 := &tunnelRecord{ID: t1.ID, Type: 1}

	cases := []struct {
		name   string
		row    *model.NodeInbound
		tunnel *tunnelRecord
		want   string
	}{
		{"type-2 exit on other node strips :port", &model.NodeInbound{NodeID: landing, Listen: "::", Port: 443}, viaIX, "203.0.113.10:443"},
		{"type-1 wildcard strips :port", &model.NodeInbound{NodeID: landing, Listen: "::", Port: 443}, type1, "203.0.113.10:443"},
		{"type-2 bracketed v6 with :port", &model.NodeInbound{NodeID: landingV6, Listen: "::", Port: 443}, viaIX, "[2001:db8::1]:443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.relayTarget(tc.row, tc.tunnel)
			if err != nil || got != tc.want {
				t.Fatalf("relayTarget must strip :port from ServerIP, got %q %v (want %s)", got, err, tc.want)
			}
		})
	}
}

// TestR28FixF01RelayEndpointSurvivesInSubscription：面 2 的订阅腿。
// 入口节点 ServerIP=203.0.113.10:6366、forward_port 无 InIP 时，
// inboundEndpoints 须产出中转端点 Host=203.0.113.10（不含 :6366），
// 且不报 "invalid relay entry host"。
func TestR28FixF01RelayEndpointSurvivesInSubscription(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r28-f01-relay-sub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h, row := seedBadEntryRelay(t, r, "203.0.113.10:6366", 45001)
	logs := captureHandlerLogs(t)
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{*row})
	if strings.Contains(logs.String(), "invalid relay entry host") {
		t.Fatalf("relay entry must not be dropped, logs: %q", logs.String())
	}
	found := false
	for i, ep := range endpoints {
		if kinds[i] == "relay" && ep.Host == "203.0.113.10" && ep.Port == 45001 {
			found = true
		}
		if kinds[i] == "relay" && strings.Contains(ep.Host, ":6366") {
			t.Fatalf("relay endpoint host must not carry :port, got %+v", ep)
		}
	}
	if !found {
		t.Fatalf("want a relay endpoint 203.0.113.10:45001, got endpoints=%+v kinds=%v", endpoints, kinds)
	}
}

// TestR28FixF01PickNodeAddressStripsPort：sweep E-04。
// pickNodeAddressV4/V6 的 ServerIP 回退分支是 selectTunnelDialHost
// 的拨号 host 来源；调用方再 fmt.Sprintf("%s:%d", host, port)，
// 原值进即双端口。nodeSupports 的 legacy 分支同根：含端口的
// ServerIP 使 ParseIP 失效、双栈误判为全真。
func TestR28FixF01PickNodeAddressStripsPort(t *testing.T) {
	if got := pickNodeAddressV4(&nodeRecord{ServerIP: "203.0.113.10:6366"}); got != "203.0.113.10" {
		t.Fatalf("pickNodeAddressV4 ServerIP with :port = %q, want %q", got, "203.0.113.10")
	}
	if got := pickNodeAddressV6(&nodeRecord{ServerIP: "[2001:db8::1]:6366"}); got != "2001:db8::1" {
		t.Fatalf("pickNodeAddressV6 ServerIP with :port = %q, want %q", got, "2001:db8::1")
	}
	if got := pickNodeAddressV4(&nodeRecord{ServerIP: "203.0.113.10"}); got != "203.0.113.10" {
		t.Fatalf("pickNodeAddressV4 bare ServerIP = %q, want %q", got, "203.0.113.10")
	}
	from := &nodeRecord{ServerIPv4: "198.51.100.1"}
	to := &nodeRecord{ServerIP: "203.0.113.10:6366"}
	host, err := selectTunnelDialHost(from, to, "", "")
	if err != nil || host != "203.0.113.10" {
		t.Fatalf("selectTunnelDialHost = %q %v, want %q", host, err, "203.0.113.10")
	}
}

func TestR28FixF01NodeSupportsStripsPort(t *testing.T) {
	v4 := &nodeRecord{ServerIP: "203.0.113.10:6366"}
	if !nodeSupportsV4(v4) || nodeSupportsV6(v4) {
		t.Fatalf("ported IPv4 ServerIP must support v4 only, got v4=%t v6=%t", nodeSupportsV4(v4), nodeSupportsV6(v4))
	}
	v6 := &nodeRecord{ServerIP: "[2001:db8::1]:6366"}
	if nodeSupportsV4(v6) || !nodeSupportsV6(v6) {
		t.Fatalf("ported IPv6 ServerIP must support v6 only, got v4=%t v6=%t", nodeSupportsV4(v6), nodeSupportsV6(v6))
	}
}

// TestR28FixF01BuildTunnelInIPStripsPort：sweep —— buildTunnelInIP
// 的 ServerIP 回退分支原值进 tunnel.in_ip，列表/前端按
// formatInAddress 把 "IP:6366" 当完整 IP:Port 展示，端口失真。
func TestR28FixF01BuildTunnelInIPStripsPort(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIP: "203.0.113.10:6366"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, ""); got != "203.0.113.10" {
		t.Fatalf("buildTunnelInIP = %q, want %q", got, "203.0.113.10")
	}
}

// TestR28FixF01ChainProbeTargetStripsPort：sweep ——
// resolveChainProbeTarget 在 selectTunnelDialHost 失败时回退
// ServerIP 原值作拨号 host，含端口即不可拨号；诊断展示面同值。
func TestR28FixF01ChainProbeTargetStripsPort(t *testing.T) {
	// fromNode 为 nil 时 selectTunnelDialHost 必败，走 ServerIP 回退
	// 分支（appendChainHopDiagnosis 的 fromNode 取自缓存、失败时可
	// 为 nil，同分支）。
	to := &nodeRecord{ServerIPv6: "2001:db8::1", ServerIP: "203.0.113.10:6366"}
	host, port, err := resolveChainProbeTarget(nil, to, 443, "", "")
	if err != nil || host != "203.0.113.10" || port != 443 {
		t.Fatalf("resolveChainProbeTarget = %q:%d %v, want 203.0.113.10:443", host, port, err)
	}
}

// TestR28FixF01NodeAddressesStripPort：sweep —— nodeAddresses 原值
// 进 resolveAllowHost，"IP:6366" 既过不了 NormalizeAllowEntry、
// DNS 解析亦失败，白名单静默丢拨号节点，入站 allow 缺口。
func TestR28FixF01NodeAddressesStripPort(t *testing.T) {
	addrs := nodeAddresses(&model.Node{ServerIP: "16.146.199.68:6366"})
	for _, a := range addrs {
		if a == "16.146.199.68:6366" {
			t.Fatalf("nodeAddresses must strip :port, got %v", addrs)
		}
	}
	found := false
	for _, a := range addrs {
		if a == "16.146.199.68" {
			found = true
		}
	}
	if !found {
		t.Fatalf("nodeAddresses must contain bare host, got %v", addrs)
	}
	if got := resolveAllowHost("16.146.199.68"); len(got) == 0 {
		t.Fatal("resolveAllowHost on stripped host must yield an allow entry")
	}
}
