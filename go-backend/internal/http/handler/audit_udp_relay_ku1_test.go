package handler

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// relay-over-UDP 专项 KU1（生成面）回归门：
//   F-UDP-1 会话型 UDP listener 必须带 keepAlive（枚举全集钉死）；
//   F-UDP-4 protocol 白名单（联邦三入口 + 本地隧道创建面 + resources
//   透传归一化）；
//   F-UDP-6 udp/kcp 服务绑 UDPListenAddr；
//   F-UDP-7 联邦/本地两套 builder 的 mux metadata 收敛一致。

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
	for _, role := range []string{"middle", "exit"} {
		role := role
		surfaces["联邦 "+role+" runtime 服务"] = func() map[string]interface{} {
			return buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "udp", role, "fed_chain_1", 1, "", "", false)
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

// F-UDP-7: 联邦 builder 与本地 builder 在同一协议下产出的 relay
// metadata 必须逐字一致（收敛构造 tunnelRelayMetadata 的永久门）。
func TestKU1MuxMetadataAlignedBetweenBuilders(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	fromNode := &nodeRecord{ID: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	targetNode := &nodeRecord{ID: 2, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}

	for _, protocol := range []string{"tcp", "tls", "mtls", "mtcp", "kcp", "ws"} {
		// handler 面：联邦服务 vs 本地链服务。
		fedService := buildFederationServiceConfig("fed_svc_1", "[::]:24002", protocol, "exit", "", 1, "", "", false)
		fedHandler, _ := fedService["handler"].(map[string]interface{})
		localServices := (&Handler{jwtSecret: "ku1-align-secret"}).buildTunnelChainServiceConfig(99, tunnelRuntimeNode{NodeID: 2, Protocol: protocol, Port: 24001, ChainType: 3}, node, 1)
		localHandler, _ := localServices[0]["handler"].(map[string]interface{})
		if !reflect.DeepEqual(fedHandler["metadata"], localHandler["metadata"]) {
			t.Fatalf("protocol %s: handler metadata drift, federation %v vs local %v",
				protocol, fedHandler["metadata"], localHandler["metadata"])
		}

		// connector 面：联邦 middle 链 vs 本地链。
		fedChain, err := buildFederationMiddleChainConfig("fed_chain_1", 7, protocol, "round",
			[]federationRuntimeTarget{{Host: "10.0.0.2", Port: 24001, Protocol: protocol}}, "", "ku1-align-secret", 99, false)
		if err != nil {
			t.Fatalf("protocol %s: build federation middle chain: %v", protocol, err)
		}
		localChain, err := (&Handler{jwtSecret: "ku1-align-secret"}).buildTunnelChainConfig(99, 1,
			[]tunnelRuntimeNode{{NodeID: 2, Protocol: protocol, Port: 24001, ConnectIP: "10.0.0.2"}},
			map[int64]*nodeRecord{1: fromNode, 2: targetNode}, "")
		if err != nil {
			t.Fatalf("protocol %s: build local chain: %v", protocol, err)
		}
		fedConnector := ku1FirstChainConnector(t, fedChain)
		localConnector := ku1FirstChainConnector(t, localChain)
		if !reflect.DeepEqual(fedConnector["metadata"], localConnector["metadata"]) {
			t.Fatalf("protocol %s: connector metadata drift, federation %v vs local %v",
				protocol, fedConnector["metadata"], localConnector["metadata"])
		}
	}
}

func ku1FirstChainConnector(t *testing.T, chain map[string]interface{}) map[string]interface{} {
	t.Helper()
	hops, _ := chain["hops"].([]map[string]interface{})
	if len(hops) == 0 {
		t.Fatalf("chain has no hops: %v", chain)
	}
	nodes, _ := hops[0]["nodes"].([]map[string]interface{})
	if len(nodes) == 0 {
		t.Fatalf("chain hop has no nodes: %v", hops[0])
	}
	connector, _ := nodes[0]["connector"].(map[string]interface{})
	if connector == nil {
		t.Fatalf("chain node has no connector: %v", nodes[0])
	}
	return connector
}

// F-UDP-4 校验器单元面：合法集全过（大小写归一后），非法值全拒。
func TestKU1ValidateTunnelProtocol(t *testing.T) {
	legal := []string{"tcp", "udp", "tls", "mtls", "mtcp", "kcp", "ws", "wss", "mws", "mwss",
		"UDP", "TLS", "Kcp", " udp "}
	for _, protocol := range legal {
		if err := validateTunnelProtocol(protocol); err != nil {
			t.Fatalf("protocol %q must be allowed, got %v", protocol, err)
		}
	}
	illegal := []string{"", " ", "sshd", "SSHD", "tun", "Tun", "dns", "ss", "socks", "quic",
		"wg", "redirect", "tap", "udp2", "tlss", "http"}
	for _, protocol := range illegal {
		if err := validateTunnelProtocol(protocol); err == nil {
			t.Fatalf("protocol %q must be rejected", protocol)
		}
	}
}

// F-UDP-4 / VB2-002: resources 透传归一化。
func TestKU1NormalizePeerServiceUDPListenerMetadata(t *testing.T) {
	cases := []struct {
		name       string
		config     map[string]interface{}
		wantKeep   interface{}
		wantTTL    interface{}
		wantAbsent bool
	}{
		{
			name:     "relay+udp 无 metadata 注入",
			config:   map[string]interface{}{"handler": map[string]interface{}{"type": "relay"}, "listener": map[string]interface{}{"type": "udp"}},
			wantKeep: true, wantTTL: "30s",
		},
		{
			name:     "relay+udp 显式 false 强制归一",
			config:   map[string]interface{}{"handler": map[string]interface{}{"type": "relay"}, "listener": map[string]interface{}{"type": "udp", "metadata": map[string]interface{}{"keepAlive": false, "ttl": "45s"}}},
			wantKeep: true, wantTTL: "45s",
		},
		{
			name:     "forward+udp 缺 keepAlive 补齐",
			config:   map[string]interface{}{"handler": map[string]interface{}{"type": "forward"}, "listener": map[string]interface{}{"type": "udp", "metadata": map[string]interface{}{"ttl": "60s"}}},
			wantKeep: true, wantTTL: "60s",
		},
		{
			name:     "forward+udp 显式 false 保留（非 relay 面不强制）",
			config:   map[string]interface{}{"handler": map[string]interface{}{"type": "forward"}, "listener": map[string]interface{}{"type": "udp", "metadata": map[string]interface{}{"keepAlive": false}}},
			wantKeep: false, wantTTL: "30s",
		},
		{
			name:       "tcp listener 不动",
			config:     map[string]interface{}{"handler": map[string]interface{}{"type": "relay"}, "listener": map[string]interface{}{"type": "tcp"}},
			wantAbsent: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalizePeerServiceUDPListenerMetadata(tc.config)
			listener, _ := tc.config["listener"].(map[string]interface{})
			metadata, _ := listener["metadata"].(map[string]interface{})
			if tc.wantAbsent {
				if metadata != nil {
					t.Fatalf("metadata must stay absent, got %v", metadata)
				}
				return
			}
			if metadata["keepAlive"] != tc.wantKeep || metadata["ttl"] != tc.wantTTL {
				t.Fatalf("metadata = %v, want keepAlive=%v ttl=%v", metadata, tc.wantKeep, tc.wantTTL)
			}
		})
	}
}

// F-UDP-4 联邦三入口的 HTTP 面：非法 protocol 被拒且不落库，
// 合法值（含 udp 与大小写形态）通过并小写归一落库。
func TestKU1PeerEndpointsProtocolWhitelist(t *testing.T) {
	h, r, share := fix132ShareFixture(t, 31000, 31099)

	reserve := func(key, protocol string) (int, string) {
		payload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/reserve-port", share.Token,
			map[string]interface{}{"resourceKey": key, "protocol": protocol},
			h.federationRuntimeReservePort)
		data, _ := payload.Data.(map[string]interface{})
		return payload.Code, asString(data["reservationId"])
	}

	for _, bad := range []string{"sshd", "tun", "SSHD", "Tun", "dns"} {
		key := "ku1-bad-" + strings.ToLower(bad)
		if code, _ := reserve(key, bad); code == 0 {
			t.Fatalf("reserve protocol %q must be rejected", bad)
		}
		stored, err := r.GetPeerShareRuntimeByResourceKey(share.ID, key)
		if err != nil {
			t.Fatalf("lookup runtime %s: %v", key, err)
		}
		if stored != nil {
			t.Fatalf("reserve protocol %q must not persist a runtime, got %+v", bad, stored)
		}
	}

	code, udpReservation := reserve("ku1-udp", "udp")
	if code != 0 || udpReservation == "" {
		t.Fatalf("reserve udp must succeed, code=%d", code)
	}
	stored, err := r.GetPeerShareRuntimeByResourceKey(share.ID, "ku1-udp")
	if err != nil || stored == nil {
		t.Fatalf("load udp runtime: %v", err)
	}
	if stored.Protocol != "udp" {
		t.Fatalf("stored protocol = %q, want udp", stored.Protocol)
	}

	if code, _ := reserve("ku1-udp-upper", "UDP"); code != 0 {
		t.Fatalf("reserve UDP must succeed after normalization, code=%d", code)
	}
	stored, err = r.GetPeerShareRuntimeByResourceKey(share.ID, "ku1-udp-upper")
	if err != nil || stored == nil {
		t.Fatalf("load UDP runtime: %v", err)
	}
	if stored.Protocol != "udp" {
		t.Fatalf("stored protocol = %q, want normalized udp", stored.Protocol)
	}

	if code, _ := reserve("ku1-default", ""); code != 0 {
		t.Fatalf("reserve empty protocol must keep the tls default, code=%d", code)
	}
	stored, err = r.GetPeerShareRuntimeByResourceKey(share.ID, "ku1-default")
	if err != nil || stored == nil || stored.Protocol != "tls" {
		t.Fatalf("default reserve stored = %+v, want protocol tls", stored)
	}

	// apply-role：请求面非法 protocol 被拒，runtime 不得被占用。
	applyRole := func(body map[string]interface{}) int {
		payload := fix132PeerRequest(t, h, "/api/v1/federation/runtime/apply-role", share.Token, body,
			h.federationRuntimeApplyRole)
		return payload.Code
	}
	if code := applyRole(map[string]interface{}{
		"reservationId": udpReservation, "role": "exit", "protocol": "dns",
	}); code == 0 {
		t.Fatal("apply-role with protocol dns must be rejected")
	}
	if code := applyRole(map[string]interface{}{
		"reservationId": udpReservation, "role": "middle", "protocol": "udp",
		"targets": []map[string]interface{}{{"host": "8.8.8.8", "port": 9999, "protocol": "sshd"}},
	}); code == 0 {
		t.Fatal("apply-role with target protocol sshd must be rejected")
	}
	stored, err = r.GetPeerShareRuntimeByResourceKey(share.ID, "ku1-udp")
	if err != nil || stored == nil {
		t.Fatalf("reload udp runtime: %v", err)
	}
	if stored.Role != "" {
		t.Fatalf("rejected apply-role must not occupy the runtime, role=%q", stored.Role)
	}

	// 存量非法值继承面：修复前落库的 sshd 预留，apply 时不带
	// protocol 也必须被生效值校验拦下。
	now := time.Now().UnixMilli()
	legacy := &repo.PeerShareRuntime{
		ShareID: share.ID, NodeID: share.NodeID, ReservationID: "ku1-legacy-res",
		ResourceKey: "ku1-legacy", Protocol: "sshd", Strategy: "round", Port: 31050,
		Status: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreatePeerShareRuntime(legacy); err != nil {
		t.Fatalf("create legacy runtime: %v", err)
	}
	if code := applyRole(map[string]interface{}{
		"reservationId": "ku1-legacy-res", "role": "exit",
	}); code == 0 {
		t.Fatal("apply-role inheriting stored protocol sshd must be rejected")
	}

	// tunnel/create：非法 protocol 被拒，udp 通过。
	tunnelCreate := func(protocol string, port int) int {
		payload := fix132PeerRequest(t, h, "/api/v1/federation/tunnel/create", share.Token,
			map[string]interface{}{"protocol": protocol, "remotePort": port, "target": "1.1.1.1:443"},
			h.federationTunnelCreate)
		return payload.Code
	}
	if code := tunnelCreate("sshd", 31060); code == 0 {
		t.Fatal("tunnel/create with protocol sshd must be rejected")
	}
	if code := tunnelCreate("udp", 31061); code != 0 {
		t.Fatalf("tunnel/create with protocol udp must succeed, code=%d", code)
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
			INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, is_remote, remote_url, remote_token, remote_config)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, name, name+"-secret", "10.0.0.1", "10.0.0.1", "", "31000-31099", "", "v1", 1, 1, 1, now, now, 1, "[::]", "[::]", 0, 0, "", "", "").Error; execErr != nil {
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

// F-UDP-1 + F-UDP-6 联邦端到端：经正式 reserve/apply-role 入口生成
// 的 exit 服务，其 UpdateService 载荷必须是 udp listener 带
// keepAlive/ttl 且绑 UDPListenAddr；tls 对照绑 TCPListenAddr。
func TestKU1FederationRoleUDPServiceEndToEnd(t *testing.T) {
	a := newCleanupAgent(t)
	if err := a.h.repo.DB().Exec(`UPDATE node SET tcp_listen_addr = ?, udp_listen_addr = ? WHERE id = 1`,
		"10.10.0.1", "10.10.0.2").Error; err != nil {
		t.Fatalf("set node listen addrs: %v", err)
	}
	now := time.Now().UnixMilli()
	share := &repo.PeerShare{
		Name: "ku1-e2e", NodeID: 1, Token: "ku1-e2e-token",
		PortRangeStart: 31000, PortRangeEnd: 31010, IsActive: 1,
		CreatedTime: now, UpdatedTime: now,
	}
	if err := a.h.repo.CreatePeerShare(share); err != nil {
		t.Fatalf("create share: %v", err)
	}

	applyExit := func(key, protocol string, port int) map[string]interface{} {
		reservePayload := fix132PeerRequest(t, a.h, "/api/v1/federation/runtime/reserve-port", share.Token,
			map[string]interface{}{"resourceKey": key, "protocol": protocol, "requestedPort": port},
			a.h.federationRuntimeReservePort)
		if reservePayload.Code != 0 {
			t.Fatalf("reserve %s (%s) failed: %+v", key, protocol, reservePayload)
		}
		reserveData, _ := reservePayload.Data.(map[string]interface{})
		applyPayload := fix132PeerRequest(t, a.h, "/api/v1/federation/runtime/apply-role", share.Token,
			map[string]interface{}{
				"reservationId": asString(reserveData["reservationId"]),
				"role":          "exit",
				"protocol":      protocol,
			},
			a.h.federationRuntimeApplyRole)
		if applyPayload.Code != 0 {
			t.Fatalf("apply exit %s (%s) failed: %+v", key, protocol, applyPayload)
		}
		cmds := a.commandsOfType("UpdateService")
		if len(cmds) == 0 {
			t.Fatalf("apply exit %s: no UpdateService command captured", key)
		}
		var services []map[string]interface{}
		if err := json.Unmarshal(cmds[len(cmds)-1].Data, &services); err != nil || len(services) != 1 {
			t.Fatalf("decode UpdateService payload: err=%v services=%v", err, services)
		}
		return services[0]
	}

	udpService := applyExit("ku1-e2e-udp", "udp", 31001)
	ku1AssertUDPListenerKeepAlive(t, "联邦 exit 端到端", udpService)
	if addr, _ := udpService["addr"].(string); addr != "10.10.0.2:31001" {
		t.Fatalf("udp exit service addr = %q, want UDPListenAddr 10.10.0.2:31001", addr)
	}

	tlsService := applyExit("ku1-e2e-tls", "tls", 31002)
	if addr, _ := tlsService["addr"].(string); addr != "10.10.0.1:31002" {
		t.Fatalf("tls exit service addr = %q, want TCPListenAddr 10.10.0.1:31002", addr)
	}
	if metadata := ku1ListenerMetadata(t, tlsService); metadata != nil {
		t.Fatalf("tls listener must stay metadata-free, got %v", metadata)
	}
}
