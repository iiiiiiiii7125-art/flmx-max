package handler

import (
	"reflect"
	"testing"
)

// Combination regression (game-chain audit, 2026-11-11): the
// tunnel half of the SS2022 + group-tunnel + socks-chain stack.
// The landing node is the tunnel exit, so the entry's forward for
// the SS2022 inbound targets 127.0.0.1:<inbound port> through a
// Type-2 tunnel whose segment protocol is tcp. This pins the
// rendered agent shapes end to end — entry forward services, the
// chain definition pushed to the entry, and the exit's relay
// service — including the fact that a tcp segment renders NO relay
// connector metadata at all (no "nodelay"), which is what makes
// the agent data plane run the relay handshake in delayed mode
// (F-212-B). The sing-box half of the composition lives in
// internal/kernel/combo_ss2022_socks_test.go.
func TestComboSS2022TunnelRelayGeneration(t *testing.T) {
	h := &Handler{jwtSecret: "combo-regression-secret"}
	entry := &nodeRecord{ID: 1, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	landing := &nodeRecord{ID: 2, TCPListenAddr: "[::]", UDPListenAddr: "[::]"}

	// Entry forward services for the managed relay of the SS2022
	// inbound (target is the inbound on the exit/landing node).
	forward := &forwardRecord{RemoteAddr: "127.0.0.1:11080", Strategy: "fifo", TunnelID: 99}
	services := buildForwardServiceConfigs("1_99_0", forward, &tunnelRecord{ID: 99, Type: 2}, entry, 12106, "", forwardRuntimeLimiters{}, false)
	if len(services) != 2 {
		t.Fatalf("expected tcp+udp forward service pair, got %d", len(services))
	}
	for _, svc := range services {
		handlerCfg, _ := svc["handler"].(map[string]interface{})
		if handlerCfg["chain"] != "chains_99" {
			t.Fatalf("forward service %v must carry the tunnel chain, got handler %v", svc["name"], handlerCfg)
		}
		if handlerCfg["type"] != svc["listener"].(map[string]interface{})["type"] {
			t.Fatalf("forward service %v: handler/listener type drift: %v", svc["name"], svc)
		}
	}

	// Chain definition pushed to the entry agent: one hop to the
	// exit, relay connector + tcp dialer, and — for protocol tcp —
	// no connector metadata whatsoever.
	chainCfg, err := h.buildTunnelChainConfig(99, 1,
		[]tunnelRuntimeNode{{NodeID: 2, Protocol: "tcp", Port: 24001, ChainType: 3, Strategy: "round", ConnectIP: "203.0.113.10"}},
		map[int64]*nodeRecord{1: entry, 2: landing}, "", "tcp")
	if err != nil {
		t.Fatalf("build chain config: %v", err)
	}
	if chainCfg["name"] != "chains_99" {
		t.Fatalf("chain name: %v", chainCfg["name"])
	}
	hops := chainCfg["hops"].([]map[string]interface{})
	if len(hops) != 1 || len(hops[0]["nodes"].([]map[string]interface{})) != 1 {
		t.Fatalf("chain hops shape: %v", chainCfg)
	}
	hopNode := hops[0]["nodes"].([]map[string]interface{})[0]
	if hopNode["addr"] != "203.0.113.10:24001" {
		t.Fatalf("hop addr: %v", hopNode["addr"])
	}
	if !reflect.DeepEqual(hopNode["connector"], map[string]interface{}{"type": "relay"}) {
		t.Fatalf("tcp segment connector must be bare relay (no metadata): %v", hopNode["connector"])
	}
	if !reflect.DeepEqual(hopNode["dialer"], map[string]interface{}{"type": "tcp"}) {
		t.Fatalf("tcp segment dialer must be bare tcp: %v", hopNode["dialer"])
	}

	// Exit service on the landing node: relay handler with bind,
	// no chain (terminal), tcp listener.
	exitServices := h.buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 2, Protocol: "tcp", Port: 24001, ChainType: 3}, landing, 1, "tcp")
	if len(exitServices) != 1 {
		t.Fatalf("expected 1 exit service, got %d", len(exitServices))
	}
	exit := exitServices[0]
	if exit["name"] != "tunnel_99" {
		t.Fatalf("exit service name: %v", exit["name"])
	}
	exitHandler := exit["handler"].(map[string]interface{})
	if exitHandler["type"] != "relay" {
		t.Fatalf("exit handler type: %v", exitHandler)
	}
	if _, ok := exitHandler["chain"]; ok {
		t.Fatalf("exit handler must not carry a chain: %v", exitHandler)
	}
	if md, _ := exitHandler["metadata"].(map[string]interface{}); md["bind"] != true {
		t.Fatalf("exit handler metadata must carry bind=true: %v", exitHandler)
	}
	if exit["listener"].(map[string]interface{})["type"] != "tcp" {
		t.Fatalf("exit listener type: %v", exit["listener"])
	}

	// Middle shape (same builder, ChainType 2) must carry the
	// chain on its relay handler.
	middleServices := h.buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 2, Protocol: "tcp", Port: 24002, ChainType: 2}, landing, 1, "tcp")
	if len(middleServices) != 1 {
		t.Fatalf("expected 1 middle service, got %d", len(middleServices))
	}
	middleHandler := middleServices[0]["handler"].(map[string]interface{})
	if middleHandler["chain"] != "chains_99" {
		t.Fatalf("middle handler must carry the tunnel chain: %v", middleHandler)
	}
}
