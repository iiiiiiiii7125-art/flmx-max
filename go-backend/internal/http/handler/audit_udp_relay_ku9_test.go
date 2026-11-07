package handler

import (
	"reflect"
	"testing"
)

// KU9/KU10 回归门：SOCKS5 UDP associate 经 relay 链的 bind 生成
// 缺口（Phase F 真机定性）。gost socks 的 associate 恒走
// connector Bind；relay handler 的 bind 是 metadata opt-in。
// KU9 曾假设「有 chain 的 handler 一律 forward、不查开关」，只给
// 链终端开 bind；Phase F 收尾复验真机证伪该前提——BIND 逐跳
// 门控、逐跳传递：每一跳 handler 都先查 enableBind，未开即
// "relay: BIND is disabled" 拒绝、根本不进 chain 转发（本地多跳
// 链 middle 第一跳 0/6）。KU10 起改为全集口径：relay handler
// 生成面全集（本地 ChainType 2/3、联邦 middle/exit）全部下发
// bind:true；非 relay 面（connector 链配置、forward 服务）仍
// 不得出现 bind 键。本文件的终端断言继续有效，全角色断言见
// audit_udp_relay_ku10_test.go。

func ku9HandlerMetadata(t *testing.T, service map[string]interface{}) map[string]interface{} {
	t.Helper()
	handler, _ := service["handler"].(map[string]interface{})
	if handler == nil {
		t.Fatalf("service has no handler config: %v", service)
	}
	metadata, _ := handler["metadata"].(map[string]interface{})
	return metadata
}

func ku9AssertNoBindKey(t *testing.T, surface string, value interface{}) {
	t.Helper()
	switch v := value.(type) {
	case map[string]interface{}:
		for key, item := range v {
			if key == "bind" {
				t.Fatalf("%s: unexpected bind key in %v", surface, v)
			}
			ku9AssertNoBindKey(t, surface, item)
		}
	case []map[string]interface{}:
		for _, item := range v {
			ku9AssertNoBindKey(t, surface, item)
		}
	case []interface{}:
		for _, item := range v {
			ku9AssertNoBindKey(t, surface, item)
		}
	}
}

// 终端面枚举：本地出口与联邦 exit 在全部隧道协议下都必须带
// bind:true，且两面 handler metadata 逐字一致（F-UDP-7 对齐门
// 扩展到 bind 键）。
func TestKU9TerminalRelayHandlersCarryBindOptIn(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	for _, protocol := range []string{"tcp", "tls", "mtls", "mtcp", "kcp", "udp", "ws", "wss"} {
		fedService := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", protocol, "exit", "", 1, "", "")
		fedMetadata := ku9HandlerMetadata(t, fedService)
		if fedMetadata["bind"] != true {
			t.Fatalf("protocol %s: federation exit handler must carry bind=true, got %v", protocol, fedMetadata)
		}

		localServices := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
			tunnelRuntimeNode{NodeID: 5, Protocol: protocol, Port: 24001, ChainType: 3}, node, 1)
		if len(localServices) != 1 {
			t.Fatalf("protocol %s: expected 1 local exit service, got %d", protocol, len(localServices))
		}
		localMetadata := ku9HandlerMetadata(t, localServices[0])
		if localMetadata["bind"] != true {
			t.Fatalf("protocol %s: local exit handler must carry bind=true, got %v", protocol, localMetadata)
		}
		if !reflect.DeepEqual(fedMetadata, localMetadata) {
			t.Fatalf("protocol %s: terminal handler metadata drift, federation %v vs local %v",
				protocol, fedMetadata, localMetadata)
		}
	}
}

// udp 协议下 tunnelRelayMetadata 本为空集：修复前终端 handler 无
// metadata 键，修复后必须恰好是 {bind:true}（新建而非合并残缺）。
func TestKU9UDPExitMetadataIsExactlyBind(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	want := map[string]interface{}{"bind": true}

	fedService := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "udp", "exit", "", 1, "", "")
	if got := ku9HandlerMetadata(t, fedService); !reflect.DeepEqual(got, want) {
		t.Fatalf("federation udp exit metadata = %v, want %v", got, want)
	}
	localServices := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "udp", Port: 24001, ChainType: 3}, node, 1)
	if got := ku9HandlerMetadata(t, localServices[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("local udp exit metadata = %v, want %v", got, want)
	}
}

// tls 出口：bind 必须与既有 mux metadata 合并共存，不得覆盖。
func TestKU9TLSExitBindMergesWithMuxMetadata(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	localServices := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "tls", Port: 24001, ChainType: 3}, node, 1)
	metadata := ku9HandlerMetadata(t, localServices[0])
	if metadata["bind"] != true {
		t.Fatalf("tls exit must carry bind=true, got %v", metadata)
	}
	if metadata["mux.maxStreamBuffer"] != 2097152 {
		t.Fatalf("tls exit mux metadata must be preserved alongside bind, got %v", metadata)
	}
}

// 对照面：非 relay 面——connector 链配置、forward 服务——都
// 不得出现 bind 键（connector 不读 bind、forward handler 非
// relay）。中继跳的 relay handler 服务属开启面（KU10 起恒带
// bind），其正向断言见 audit_udp_relay_ku10_test.go；本测试
// 前身曾断言「中继面全树无 bind」，该口径已被真机证伪、删除。
func TestKU9NonRelaySurfacesHaveNoBind(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	fromNode := &nodeRecord{ID: 1, TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	targetNode := &nodeRecord{ID: 2, TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}

	for _, protocol := range []string{"udp", "tls"} {
		localChain, err := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainConfig(99, 1,
			[]tunnelRuntimeNode{{NodeID: 2, Protocol: protocol, Port: 24001, ConnectIP: "10.0.0.2"}},
			map[int64]*nodeRecord{1: fromNode, 2: targetNode}, "")
		if err != nil {
			t.Fatalf("protocol %s: build local chain: %v", protocol, err)
		}
		ku9AssertNoBindKey(t, "local chain config", localChain)

		fedChain, err := buildFederationMiddleChainConfig("fed_chain_1", 7, protocol, "round",
			[]federationRuntimeTarget{{Host: "10.0.0.2", Port: 24001, Protocol: protocol}}, "", "", 0)
		if err != nil {
			t.Fatalf("protocol %s: build federation middle chain: %v", protocol, err)
		}
		ku9AssertNoBindKey(t, "federation middle chain config", fedChain)
	}

	// forward 服务（链的消费端，handler 为 tcp/udp 非 relay）：
	// 直连形态与挂链形态都不得出现 bind 键。
	forward := &forwardRecord{RemoteAddr: "1.2.3.4:53", Strategy: "fifo", TunnelID: 7}
	direct := buildForwardServiceConfigs("1_2_0", forward, nil, node, 24003, "", forwardRuntimeLimiters{})
	for _, svc := range direct {
		ku9AssertNoBindKey(t, "direct forward service", svc)
	}
	chained := buildForwardServiceConfigs("1_2_0", forward, &tunnelRecord{Type: 2}, node, 24003, "", forwardRuntimeLimiters{})
	for _, svc := range chained {
		ku9AssertNoBindKey(t, "chained forward service", svc)
	}
}
