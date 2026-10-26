package handler

import (
	"testing"
)

// R30 sweep 新面 F-02（产品 P2）：tunnel connectIp（链节点自定义
// 连接 IP，下拉源与 forward inIp 同为节点原始字段）在
// selectTunnelDialHost 原值返回；下游再拼 ":port" 即产出畸形拨号
// 地址（gost 链配置经 processServerAddress 得 "[IP:port]:port"，
// 联邦 RuntimeTarget Host 带端口、延迟/诊断探针目标带端口）。
// connectIp 无任何格式校验（validateTunnelConnectIPConstraints 只
// 拦多节点），前端 getNodeIpOptions（tunnel.tsx:412）原值入下拉。
// 修法：choke 点 selectTunnelDialHost 的 connectIp 分支复用
// stripShareHostPort（R25 口径）；存储 chain_tunnel.connect_ip
// 保持原值（与 R29 消费面修复同口径，纵深防御在消费点）。

func TestR30FixF02SelectTunnelDialHostStripsPortedConnectIP(t *testing.T) {
	from := &nodeRecord{ID: 1, ServerIP: "198.51.100.1", ServerIPv4: "198.51.100.1"}
	to := &nodeRecord{ID: 2, ServerIP: "198.51.100.77", ServerIPv4: "198.51.100.77"}
	if host, err := selectTunnelDialHost(from, to, "", "198.51.100.77:8443"); err != nil || host != "198.51.100.77" {
		t.Fatalf("selectTunnelDialHost ported connectIp = %q %v, want %q", host, err, "198.51.100.77")
	}
	if host, err := selectTunnelDialHost(from, to, "", " 198.51.100.77:8443 "); err != nil || host != "198.51.100.77" {
		t.Fatalf("selectTunnelDialHost spaced ported connectIp = %q %v, want %q", host, err, "198.51.100.77")
	}
}

func TestR30FixF02SelectTunnelDialHostStripsBracketedV6ConnectIP(t *testing.T) {
	from := &nodeRecord{ID: 1, ServerIP: "198.51.100.1", ServerIPv4: "198.51.100.1"}
	to := &nodeRecord{ID: 2, ServerIP: "2001:db8::1", ServerIPv6: "2001:db8::1"}
	if host, err := selectTunnelDialHost(from, to, "v6", "[2001:db8::1]:8443"); err != nil || host != "2001:db8::1" {
		t.Fatalf("selectTunnelDialHost bracketed v6 connectIp = %q %v, want %q", host, err, "2001:db8::1")
	}
}

func TestR30FixF02SelectTunnelDialHostBareConnectIPControl(t *testing.T) {
	from := &nodeRecord{ID: 1, ServerIP: "198.51.100.1", ServerIPv4: "198.51.100.1"}
	to := &nodeRecord{ID: 2, ServerIP: "198.51.100.77", ServerIPv4: "198.51.100.77"}
	if host, err := selectTunnelDialHost(from, to, "", "198.51.100.77"); err != nil || host != "198.51.100.77" {
		t.Fatalf("selectTunnelDialHost bare connectIp = %q %v, want %q", host, err, "198.51.100.77")
	}
	// 空 connectIp 走原有 pick 口径（R29 已剥离），不受本修复影响。
	if host, err := selectTunnelDialHost(from, to, "", ""); err != nil || host != "198.51.100.77" {
		t.Fatalf("selectTunnelDialHost empty connectIp = %q %v, want %q", host, err, "198.51.100.77")
	}
}

func TestR30FixF02BuildTunnelChainConfigStripsPortedConnectIP(t *testing.T) {
	nodes := map[int64]*nodeRecord{
		1: {ID: 1, ServerIP: "198.51.100.1", ServerIPv4: "198.51.100.1"},
		2: {ID: 2, ServerIP: "198.51.100.77", ServerIPv4: "198.51.100.77"},
	}
	targets := []tunnelRuntimeNode{{NodeID: 2, Protocol: "tls", Strategy: "round", Port: 30001, ConnectIP: "198.51.100.77:8443"}}
	cfg, err := buildTunnelChainConfig(7, 1, targets, nodes, "")
	if err != nil {
		t.Fatalf("buildTunnelChainConfig: %v", err)
	}
	got := extractR30F02ChainAddrs(t, cfg)
	if len(got) != 1 || got[0] != "198.51.100.77:30001" {
		t.Fatalf("chain node addr = %v, want [198.51.100.77:30001] (ported connectIp must not produce [IP:port]:port)", got)
	}
}

func extractR30F02ChainAddrs(t *testing.T, cfg map[string]interface{}) []string {
	t.Helper()
	var out []string
	hops, _ := cfg["hops"].([]map[string]interface{})
	for _, hop := range hops {
		ns, _ := hop["nodes"].([]map[string]interface{})
		for _, n := range ns {
			addr, _ := n["addr"].(string)
			out = append(out, addr)
		}
	}
	return out
}
