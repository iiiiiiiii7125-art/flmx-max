package handler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// relay-over-UDP 专项 KU1（生成面）回归门：
//   F-UDP-1 会话型 UDP listener 必须带 keepAlive（枚举全集钉死）；
//   F-UDP-4 protocol 白名单（本地隧道创建面）；
//   F-UDP-6 udp/kcp 服务绑 UDPListenAddr。
// （F-UDP-4 联邦三入口与 F-UDP-7 联邦/本地对齐面已随 DEV-RM-SHARE
// 删除，相关用例一并摘除。）

func ku1ListenerMetadata(t *testing.T, service map[string]interface{}) map[string]interface{} {
	t.Helper()
	listener, _ := service["listener"].(map[string]interface{})
	if listener == nil {
		t.Fatalf("service has no listener config: %v", service)
	}
	metadata, _ := listener["metadata"].(map[string]interface{})
	return metadata
}

func ku1AssertUDPListenerKeepAlive(t *testing.T, surface string, service map[string]interface{}) {
	t.Helper()
	listener, _ := service["listener"].(map[string]interface{})
	// type 原样透传 builder 入参（大小写归一是入口层契约，由入口
	// 测试钉住）；本 helper 只钉 metadata 口径。
	if listener == nil || !strings.EqualFold(asString(listener["type"]), "udp") {
		t.Fatalf("%s: expected udp listener, got %v", surface, service["listener"])
	}
	metadata := ku1ListenerMetadata(t, service)
	if metadata["keepAlive"] != true {
		t.Fatalf("%s: udp listener must carry keepAlive=true (F-UDP-1), got %v", surface, metadata)
	}
	if metadata["ttl"] != "30s" {
		t.Fatalf("%s: udp listener ttl must be 30s (forward _udp 口径), got %v", surface, metadata)
	}
}

func TestKU1TunnelListenerConfigUDPKeepAlive(t *testing.T) {
	ku1AssertUDPListenerKeepAlive(t, "buildTunnelListenerConfig(udp)",
		map[string]interface{}{"listener": buildTunnelListenerConfig("udp", "", kcpTierSmall, false)})
	ku1AssertUDPListenerKeepAlive(t, "buildTunnelListenerConfig(UDP)",
		map[string]interface{}{"listener": buildTunnelListenerConfig("UDP", "", kcpTierSmall, false)})

	for _, protocol := range []string{"tcp", "tls", "mtls", "mtcp", "ws", "wss", "mws", "mwss"} {
		listener := buildTunnelListenerConfig(protocol, "", kcpTierSmall, false)
		if _, hasMetadata := listener["metadata"]; hasMetadata {
			t.Fatalf("protocol %s must not gain listener metadata, got %v", protocol, listener)
		}
	}
	kcp := buildTunnelListenerConfig("kcp", "", kcpTierSmall, false)
	kcpMetadata, _ := kcp["metadata"].(map[string]interface{})
	if kcpMetadata == nil || kcpMetadata["kcp.mode"] != "fast3" {
		t.Fatalf("kcp listener metadata must be preserved, got %v", kcp)
	}
	if _, hasKeepAlive := kcpMetadata["keepAlive"]; hasKeepAlive {
		t.Fatalf("kcp listener must not gain udp keepAlive key, got %v", kcpMetadata)
	}
}

// TestKU1UDPGenerationSurfaceEnumeration 是 F-UDP-1 的枚举门：面板
// 全部 UDP 生成形态逐个登记在此，任一形态漏配 keepAlive 即红；
// 新增 UDP 生成面必须先把形态加进本表。
func TestKU1UDPGenerationSurfaceEnumeration(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}

	surfaces := map[string]func() map[string]interface{}{}
	for _, chainType := range []int{2, 3} {
		chainType := chainType
		surfaces[map[int]string{2: "本地链中段服务", 3: "本地链出口服务"}[chainType]] = func() map[string]interface{} {
			chain := tunnelRuntimeNode{NodeID: 5, Protocol: "udp", Port: 24001, ChainType: chainType}
			services := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99, chain, node, 1)
			if len(services) != 1 {
				t.Fatalf("chain type %d: expected 1 service, got %d", chainType, len(services))
			}
			return services[0]
		}
	}
	surfaces["本地转发 _udp 服务（既有合规面，防回退）"] = func() map[string]interface{} {
		forward := &forwardRecord{RemoteAddr: "1.2.3.4:53", Strategy: "fifo", TunnelID: 7}
		services := buildForwardServiceConfigs("1_2_0", forward, nil, node, 24003, "", forwardRuntimeLimiters{})
		for _, svc := range services {
			if listener, _ := svc["listener"].(map[string]interface{}); listener["type"] == "udp" {
				return svc
			}
		}
		t.Fatalf("forward services missing _udp: %v", services)
		return nil
	}

	for name, build := range surfaces {
		t.Run(name, func(t *testing.T) {
			ku1AssertUDPListenerKeepAlive(t, name, build())
		})
	}
}

// F-UDP-6: 本地链服务的监听地址按协议分取。
func TestKU1ChainServiceListenAddrByProtocol(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	cases := []struct {
		protocol string
		want     string
	}{
		{"udp", "192.0.2.2:24001"},
		{"kcp", "192.0.2.2:24001"},
		{"tls", "192.0.2.1:24001"},
		{"tcp", "192.0.2.1:24001"},
	}
	for _, tc := range cases {
		chain := tunnelRuntimeNode{NodeID: 5, Protocol: tc.protocol, Port: 24001, ChainType: 3}
		services := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99, chain, node, 1)
		if len(services) != 1 {
			t.Fatalf("protocol %s: expected 1 service, got %d", tc.protocol, len(services))
		}
		if addr, _ := services[0]["addr"].(string); addr != tc.want {
			t.Fatalf("protocol %s: addr = %q, want %q", tc.protocol, addr, tc.want)
		}
	}
}

func TestKU1NodeListenAddrForProtocol(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	for _, protocol := range []string{"udp", "kcp", "quic", "UDP"} {
		if got := nodeListenAddrForProtocol(node, protocol); got != "192.0.2.2" {
			t.Fatalf("protocol %s: got %q, want UDPListenAddr", protocol, got)
		}
	}
	for _, protocol := range []string{"tcp", "tls", "ws", ""} {
		if got := nodeListenAddrForProtocol(node, protocol); got != "192.0.2.1" {
			t.Fatalf("protocol %s: got %q, want TCPListenAddr", protocol, got)
		}
	}
	if got := nodeListenAddrForProtocol(nil, "udp"); got != "" {
		t.Fatalf("nil node: got %q, want empty", got)
	}
}

// F-UDP-4 校验器单元面：合法集全过（大小写归一后），非法值全拒。
// （原 validateTunnelProtocol 寄居于已删的 federation_resources.go、
// 其生产调用面全为联邦入口；本地创建面现行校验器为
// isAllowedTunnelProtocol + normalizeTunnelProtocol，本格改钉现行面，
// 合法/非法集逐字不变。）
func TestKU1ValidateTunnelProtocol(t *testing.T) {
	legal := []string{"tcp", "udp", "tls", "mtls", "mtcp", "kcp", "ws", "wss", "mws", "mwss",
		"UDP", "TLS", "Kcp", " udp "}
	for _, protocol := range legal {
		if !isAllowedTunnelProtocol(protocol) {
			t.Fatalf("protocol %q must be allowed", protocol)
		}
	}
	illegal := []string{"", " ", "sshd", "SSHD", "tun", "Tun", "dns", "ss", "socks", "quic",
		"wg", "redirect", "tap", "udp2", "tlss", "http"}
	for _, protocol := range illegal {
		if isAllowedTunnelProtocol(protocol) {
			t.Fatalf("protocol %q must be rejected", protocol)
		}
	}
}

// F-UDP-4 本地隧道创建面：prepareTunnelCreateState 是创建/更新
// 共用收口点，三组节点（入口/链/出口）的 protocol 都要校验，
// 合法值小写归一后进入 state。
func TestKU1PrepareTunnelCreateStateProtocolWhitelist(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "ku1.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()
	h := &Handler{repo: r}
	now := time.Now().UnixMilli()

	insertNode := func(name string) int64 {
		if execErr := r.DB().Exec(`
			INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, name, name+"-secret", "10.0.0.1", "10.0.0.1", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0).Error; execErr != nil {
			t.Fatalf("insert node %s: %v", name, execErr)
		}
		return mustLastInsertID(t, r, name)
	}
	entryID := insertNode("ku1-entry")
	midID := insertNode("ku1-mid")
	outID := insertNode("ku1-out")

	buildReq := func(inProtocol, midProtocol, outProtocol string) map[string]interface{} {
		return map[string]interface{}{
			"name": "ku1-tunnel",
			"inNodeId": []interface{}{
				map[string]interface{}{"nodeId": float64(entryID), "protocol": inProtocol, "strategy": "round"},
			},
			"outNodeId": []interface{}{
				map[string]interface{}{"nodeId": float64(outID), "protocol": outProtocol, "strategy": "round", "port": float64(0)},
			},
			"chainNodes": []interface{}{
				[]interface{}{
					map[string]interface{}{"nodeId": float64(midID), "protocol": midProtocol, "strategy": "round", "port": float64(0)},
				},
			},
		}
	}
	prepare := func(req map[string]interface{}) (*tunnelCreateState, error) {
		tx := r.DB().Begin()
		defer tx.Rollback()
		return h.prepareTunnelCreateState(tx, req, 2, 0)
	}

	for _, tc := range []struct {
		name string
		req  map[string]interface{}
	}{
		{"入口 sshd", buildReq("sshd", "tls", "tls")},
		{"链节点 tun", buildReq("tls", "tun", "tls")},
		{"出口 dns", buildReq("tls", "tls", "dns")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := prepare(tc.req); err == nil {
				t.Fatalf("%s: illegal segment protocol must be rejected", tc.name)
			} else if !strings.Contains(err.Error(), "不支持的隧道协议") {
				t.Fatalf("%s: unexpected error %v", tc.name, err)
			}
		})
	}

	state, err := prepare(buildReq("UDP", "Udp", "udp"))
	if err != nil {
		t.Fatalf("udp segments must pass: %v", err)
	}
	if state.InNodes[0].Protocol != "udp" || state.OutNodes[0].Protocol != "udp" ||
		len(state.ChainHops) != 1 || state.ChainHops[0][0].Protocol != "udp" {
		t.Fatalf("segment protocols must be normalized to udp: in=%q out=%q hops=%v",
			state.InNodes[0].Protocol, state.OutNodes[0].Protocol, state.ChainHops)
	}
}
