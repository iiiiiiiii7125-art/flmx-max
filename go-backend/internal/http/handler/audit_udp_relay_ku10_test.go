package handler

import (
	"reflect"
	"testing"
)

// KU10 回归门：KU9 的终端-only bind 口径被 Phase F 收尾复验真机
// 证伪后的全集修正。机制（真机定版）：relay BIND 逐跳门控、
// 逐跳传递——x 的 relay handler 对 BIND 命令在每一跳 handler
// 都先查 enableBind，未开即 "relay: BIND is disabled" 拒绝、
// 根本不进 chain 转发。故开启面必须是 relay handler 生成面
// 全集：
//   - 本地链：buildTunnelChainServiceConfig 产出的 ChainType 2
//     （中段）与 ChainType 3（出口）服务；ChainType 1（入口）
//     不经本 builder 产服务（只下发 chain 配置），无 relay
//     handler 面。
// connector/dialer 链配置与 forward 服务不产 relay handler、
// 不读 bind 键，其无 bind 断言在 audit_udp_relay_ku9_test.go
// （TestKU9NonRelaySurfacesHaveNoBind）。

func ku10Handler(t *testing.T, service map[string]interface{}) map[string]interface{} {
	t.Helper()
	handler, _ := service["handler"].(map[string]interface{})
	if handler == nil {
		t.Fatalf("service has no handler config: %v", service)
	}
	return handler
}

// 全角色枚举：全部隧道协议下，本地 ChainType 2/3 两个面的
// relay handler 都必须带 bind:true，且两面 handler metadata
// 逐字一致（F-UDP-7 对齐门扩展到全部链上角色）。
// （联邦 middle/exit 两面已随 DEV-RM-SHARE 删除。）
func TestKU10AllChainRoleRelayHandlersCarryBind(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	for _, protocol := range []string{"tcp", "tls", "mtls", "mtcp", "kcp", "udp", "ws", "wss"} {
		faces := map[string]map[string]interface{}{}

		localMiddle := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
			tunnelRuntimeNode{NodeID: 5, Protocol: protocol, Port: 24001, ChainType: 2}, node, 1)
		if len(localMiddle) != 1 {
			t.Fatalf("protocol %s: expected 1 local middle service, got %d", protocol, len(localMiddle))
		}
		faces["local middle (ChainType 2)"] = localMiddle[0]

		localExit := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
			tunnelRuntimeNode{NodeID: 6, Protocol: protocol, Port: 24003, ChainType: 3}, node, 1)
		if len(localExit) != 1 {
			t.Fatalf("protocol %s: expected 1 local exit service, got %d", protocol, len(localExit))
		}
		faces["local exit (ChainType 3)"] = localExit[0]

		var reference map[string]interface{}
		var referenceFace string
		for face, service := range faces {
			metadata := ku9HandlerMetadata(t, service)
			if metadata["bind"] != true {
				t.Fatalf("protocol %s: %s handler must carry bind=true, got %v", protocol, face, metadata)
			}
			if reference == nil {
				reference, referenceFace = metadata, face
			} else if !reflect.DeepEqual(reference, metadata) {
				t.Fatalf("protocol %s: handler metadata drift, %s %v vs %s %v",
					protocol, referenceFace, reference, face, metadata)
			}
		}
	}
}

// 多跳形态：本地三跳链（两段 ChainType 2 中段＋ChainType 3
// 出口，即 Phase F 收尾复验 FAIL 的形态加长版）逐跳生成的
// relay handler 全部带 bind——BIND 到达任一跳都不得被门控
// 拒绝。中段 handler 须挂 chain、出口不得挂 chain。
func TestKU10LocalMultiHopChainHandlersCarryBind(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	for _, protocol := range []string{"udp", "tls"} {
		hops := []struct {
			name      string
			chainNode tunnelRuntimeNode
			nextCount int
			wantChain bool
		}{
			{"middle 1", tunnelRuntimeNode{NodeID: 11, Protocol: protocol, Port: 24101, ChainType: 2}, 2, true},
			{"middle 2", tunnelRuntimeNode{NodeID: 12, Protocol: protocol, Port: 24102, ChainType: 2}, 1, true},
			{"exit", tunnelRuntimeNode{NodeID: 13, Protocol: protocol, Port: 24103, ChainType: 3}, 1, false},
		}
		for _, hop := range hops {
			services := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99, hop.chainNode, node, hop.nextCount)
			if len(services) != 1 {
				t.Fatalf("protocol %s hop %s: expected 1 service, got %d", protocol, hop.name, len(services))
			}
			handler := ku10Handler(t, services[0])
			metadata := ku9HandlerMetadata(t, services[0])
			if metadata["bind"] != true {
				t.Fatalf("protocol %s hop %s: handler must carry bind=true, got %v", protocol, hop.name, metadata)
			}
			_, hasChain := handler["chain"]
			if hasChain != hop.wantChain {
				t.Fatalf("protocol %s hop %s: chain presence = %v, want %v", protocol, hop.name, hasChain, hop.wantChain)
			}
		}
	}

	// udp 中段：tunnelRelayMetadata 本为空集，metadata 必须恰为
	// {bind:true, reapRole:backstop}（新建而非合并残缺，与出口同
	// 形态；reapRole 是根治角色标记，全部段服务无条件携带）。
	udpMiddle := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 11, Protocol: "udp", Port: 24101, ChainType: 2}, node, 1)
	if got, want := ku9HandlerMetadata(t, udpMiddle[0]), map[string]interface{}{"bind": true, "reapRole": "backstop"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local udp middle metadata = %v, want %v", got, want)
	}
}

// tls 本地中段：bind 必须与既有 mux metadata 合并共存，不得
// 覆盖（KU9 的出口同名断言的中段对应面；原联邦中段面已随
// DEV-RM-SHARE 删除，断言迁至本地中段面）。
func TestKU10LocalMiddleBindMergesWithMuxMetadata(t *testing.T) {
	node := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	localMiddle := (&Handler{jwtSecret: "ku-regression-secret"}).buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "tls", Port: 24001, ChainType: 2}, node, 1)
	if len(localMiddle) != 1 {
		t.Fatalf("expected 1 local middle service, got %d", len(localMiddle))
	}
	metadata := ku9HandlerMetadata(t, localMiddle[0])
	if metadata["bind"] != true {
		t.Fatalf("tls local middle must carry bind=true, got %v", metadata)
	}
	if metadata["mux.maxStreamBuffer"] != 2097152 {
		t.Fatalf("tls local middle mux metadata must be preserved alongside bind, got %v", metadata)
	}
}
