package handler

import (
	"reflect"
	"testing"
)

// KU9 回归门：SOCKS5 UDP associate 经 relay 链的 bind 生成缺口
// （Phase F 真机定性）。gost socks 的 associate 恒走 connector
// Bind；relay handler 的 bind 是 metadata opt-in，且只有链终端
// （handler 无 chain）才分发 handleBind 并查此开关，中继跳走
// forward 转发不查。故 bind 只能开在终端 handler 上：
//   - 本地链出口（buildTunnelChainServiceConfig ChainType==3）
//   - 联邦 exit（buildFederationServiceConfig role=="exit"）
// 两面同口径；中继面、connector 面、forward 服务逐字不变。

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
		fedService := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", protocol, "exit", "", 1, "")
		fedMetadata := ku9HandlerMetadata(t, fedService)
		if fedMetadata["bind"] != true {
			t.Fatalf("protocol %s: federation exit handler must carry bind=true, got %v", protocol, fedMetadata)
		}

		localServices := buildTunnelChainServiceConfig(99,
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

	fedService := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", "udp", "exit", "", 1, "")
	if got := ku9HandlerMetadata(t, fedService); !reflect.DeepEqual(got, want) {
		t.Fatalf("federation udp exit metadata = %v, want %v", got, want)
	}
	localServices := buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "udp", Port: 24001, ChainType: 3}, node, 1)
	if got := ku9HandlerMetadata(t, localServices[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("local udp exit metadata = %v, want %v", got, want)
	}
}

// tls 出口：bind 必须与既有 mux metadata 合并共存，不得覆盖。
func TestKU9TLSExitBindMergesWithMuxMetadata(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	localServices := buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "tls", Port: 24001, ChainType: 3}, node, 1)
	metadata := ku9HandlerMetadata(t, localServices[0])
	if metadata["bind"] != true {
		t.Fatalf("tls exit must carry bind=true, got %v", metadata)
	}
	if metadata["mux.maxStreamBuffer"] != 2097152 {
		t.Fatalf("tls exit mux metadata must be preserved alongside bind, got %v", metadata)
	}
}

// 对照面：中继跳（forward 模式、不执行 BIND）、connector 面、
// forward 服务都不得出现 bind 键——开启面最小化、非 relay 形态
// 逐字不变。
func TestKU9NonTerminalSurfacesHaveNoBind(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	fromNode := &nodeRecord{ID: 1, TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	targetNode := &nodeRecord{ID: 2, TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}

	for _, protocol := range []string{"udp", "tls"} {
		middleServices := buildTunnelChainServiceConfig(99,
			tunnelRuntimeNode{NodeID: 5, Protocol: protocol, Port: 24001, ChainType: 2}, node, 1)
		ku9AssertNoBindKey(t, "local middle service", middleServices[0])

		fedMiddle := buildFederationServiceConfig("fed_svc_1", "192.0.2.2:24002", protocol, "middle", "fed_chain_1", 1, "")
		ku9AssertNoBindKey(t, "federation middle service", fedMiddle)

		localChain, err := buildTunnelChainConfig(99, 1,
			[]tunnelRuntimeNode{{NodeID: 2, Protocol: protocol, Port: 24001, ConnectIP: "10.0.0.2"}},
			map[int64]*nodeRecord{1: fromNode, 2: targetNode}, "")
		if err != nil {
			t.Fatalf("protocol %s: build local chain: %v", protocol, err)
		}
		ku9AssertNoBindKey(t, "local chain config", localChain)

		fedChain, err := buildFederationMiddleChainConfig("fed_chain_1", 7, protocol, "round",
			[]federationRuntimeTarget{{Host: "10.0.0.2", Port: 24001, Protocol: protocol}}, "")
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
