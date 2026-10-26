package handler

import (
	"database/sql"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R29-L1-E01 / R29-FIX-F01（产品 P2）：buildTunnelInIP 的显式
// ServerIPv4/ServerIPv6 分支原值进 tunnel.in_ip，未剥离 :port。
// 写侧 nodeCreate 只校验 serverIp，serverIpV4/serverIpV6 原值落库，
// 故显式字段可含 :port；仅 ServerIP 回退分支经 stripShareHostPort。
// 探针按 L1B 三格口径重建（R29-L1B-probe-handler.log，探针源码已删）：
// 显式 V4、显式 V6、双显式第二地址；对照格 5 格全绿。

func TestR29FixF01BuildTunnelInIPExplicitV4StripsPort(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIPv4: "198.51.100.77:8443"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, ""); got != "198.51.100.77" {
		t.Fatalf("buildTunnelInIP explicit v4 with :port = %q, want %q", got, "198.51.100.77")
	}
}

func TestR29FixF01BuildTunnelInIPExplicitV6StripsPort(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIPv6: "[2001:db8::1]:8443"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, ""); got != "2001:db8::1" {
		t.Fatalf("buildTunnelInIP explicit v6 with :port = %q, want %q", got, "2001:db8::1")
	}
}

func TestR29FixF01BuildTunnelInIPDualExplicitSecondKept(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIPv4: "198.51.100.77:8443", ServerIPv6: "2001:db8::1"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, "v6"); got != "2001:db8::1,198.51.100.77" {
		t.Fatalf("buildTunnelInIP v6-prefer explicit values = %q, want %q", got, "2001:db8::1,198.51.100.77")
	}
}

func TestR29FixF01BuildTunnelInIPExplicitBareControl(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIPv4: "198.51.100.77", ServerIPv6: "2001:db8::1"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, ""); got != "198.51.100.77,2001:db8::1" {
		t.Fatalf("buildTunnelInIP bare explicit values = %q, want %q", got, "198.51.100.77,2001:db8::1")
	}
}

func TestR29FixF01BuildTunnelInIPFallbackStillStripped(t *testing.T) {
	nodes := map[int64]*nodeRecord{1: {ID: 1, ServerIP: "203.0.113.10:6366"}}
	if got := buildTunnelInIP([]tunnelRuntimeNode{{NodeID: 1}}, nodes, ""); got != "203.0.113.10" {
		t.Fatalf("buildTunnelInIP fallback ServerIP with :port = %q, want %q", got, "203.0.113.10")
	}
}

func TestR29FixF01PickNodeAddressExplicitPorted(t *testing.T) {
	if got := pickNodeAddressV4(&nodeRecord{ServerIPv4: "198.51.100.77:8443"}); got != "198.51.100.77" {
		t.Fatalf("pickNodeAddressV4 explicit v4 with :port = %q, want %q", got, "198.51.100.77")
	}
	if got := pickNodeAddressV6(&nodeRecord{ServerIPv6: "[2001:db8::1]:8443"}); got != "2001:db8::1" {
		t.Fatalf("pickNodeAddressV6 explicit v6 with :port = %q, want %q", got, "2001:db8::1")
	}
}

func TestR29FixF01SelectDialHostExplicitPorted(t *testing.T) {
	from := &nodeRecord{ServerIPv4: "198.51.100.1"}
	to := &nodeRecord{ServerIPv4: "198.51.100.77:8443"}
	host, err := selectTunnelDialHost(from, to, "", "")
	if err != nil || host != "198.51.100.77" {
		t.Fatalf("selectTunnelDialHost = %q %v, want %q", host, err, "198.51.100.77")
	}
}

func TestR29FixF01NodeSupportsExplicitPorted(t *testing.T) {
	v4 := &nodeRecord{ServerIPv4: "198.51.100.77:8443"}
	if !nodeSupportsV4(v4) || nodeSupportsV6(v4) {
		t.Fatalf("explicit ported v4 must support v4 only, got v4=%t v6=%t", nodeSupportsV4(v4), nodeSupportsV6(v4))
	}
	v6 := &nodeRecord{ServerIPv6: "[2001:db8::1]:8443"}
	if nodeSupportsV4(v6) || !nodeSupportsV6(v6) {
		t.Fatalf("explicit ported v6 must support v6 only, got v4=%t v6=%t", nodeSupportsV4(v6), nodeSupportsV6(v6))
	}
}

// Sweep 探针（R29-FIX-F01 §3）：显式 v4/v6 其余后端消费点逐点核对，
// 均已剥离——nodeShareIP（订阅分享面）、nodeAddresses（allow 白名单
// 面）、relayTarget（中转拨号面，显式 v4 仅 ServerIP 为空时取用，
// 与 ServerIP 共用 JoinHostPort 前的同一 strip 点）。

func TestR29FixF01SweepNodeShareIPExplicitPorted(t *testing.T) {
	n := &model.Node{ServerIPV4: sql.NullString{String: "198.51.100.77:8443", Valid: true}}
	if got := nodeShareIP(n, false); got != "198.51.100.77" {
		t.Fatalf("nodeShareIP explicit v4 with :port = %q, want %q", got, "198.51.100.77")
	}
	n6 := &model.Node{ServerIPV6: sql.NullString{String: "[2001:db8::1]:8443", Valid: true}}
	if got := nodeShareIP(n6, true); got != "2001:db8::1" {
		t.Fatalf("nodeShareIP explicit v6 with :port = %q, want %q", got, "2001:db8::1")
	}
}

func TestR29FixF01SweepNodeAddressesExplicitPorted(t *testing.T) {
	addrs := nodeAddresses(&model.Node{
		ServerIP:   "198.51.100.1",
		ServerIPV4: sql.NullString{String: "198.51.100.77:8443", Valid: true},
		ServerIPV6: sql.NullString{String: "[2001:db8::1]:8443", Valid: true},
	})
	for _, a := range addrs {
		if a == "198.51.100.77:8443" || a == "[2001:db8::1]:8443" || a == "2001:db8::1:8443" {
			t.Fatalf("nodeAddresses must strip :port, got %v", addrs)
		}
	}
	foundV4, foundV6 := false, false
	for _, a := range addrs {
		if a == "198.51.100.77" {
			foundV4 = true
		}
		if a == "2001:db8::1" {
			foundV6 = true
		}
	}
	if !foundV4 || !foundV6 {
		t.Fatalf("nodeAddresses must contain stripped explicit hosts, got %v", addrs)
	}
}

func TestR29FixF01SweepRelayTargetExplicitV4Ported(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r29-f01-relay-target.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	// ServerIP 为空、显式 v4 含端口：relayTarget 取 v4 分支，须经
	// JoinHostPort 前的同一 strip 点产出可拨号地址。
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, '', ?, '', '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, "landing-xv4", "landing-xv4-secret", "198.51.100.77:8443", now, now).Error; err != nil {
		t.Fatal(err)
	}
	var landing int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, "landing-xv4").Scan(&landing).Error; err != nil {
		t.Fatal(err)
	}
	t1 := model.Tunnel{Name: "type1-xv4", Type: 1, Protocol: "tcp", Flow: 99999, Status: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.DB().Create(&t1).Error; err != nil {
		t.Fatal(err)
	}
	got, err := h.relayTarget(&model.NodeInbound{NodeID: landing, Listen: "::", Port: 443}, &tunnelRecord{ID: t1.ID, Type: 1})
	if err != nil || got != "198.51.100.77:443" {
		t.Fatalf("relayTarget explicit v4 with :port = %q %v, want %q", got, err, "198.51.100.77:443")
	}
}
