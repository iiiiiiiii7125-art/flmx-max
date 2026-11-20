package handler

import (
	"strings"
	"testing"
)

// R18-C5-01 (allowlist + builder legs): the allowlist is derived
// from the panel-known topology, and buildForwardServiceConfigs
// arms PROXY receive only when it is non-empty (fail-closed).
// This file references the new symbols, so it cannot compile on
// old code — compile failure is its red signal there (verified by
// stash in the final report); the behavioral red lives in
// audit_c5_proxy_protocol_receive_test.go.

func TestAuditC5ProxyProtocolReceiveAllowlist(t *testing.T) {
	h := setupK9Handler(t)
	receiver := c5SeedReceiver(t, h)
	// Sender on node 11 (ServerIP 10.0.0.2) dials the receiver
	// entry 10.0.0.1:30005 with PROXY send on.
	c5SeedSender(t, h, "10.0.0.1:30005", 11, 35005)

	node, err := h.getNodeRecord(10)
	if err != nil {
		t.Fatalf("get receiver node: %v", err)
	}
	allow := h.proxyProtocolReceiveAllowlistForForward(receiver, node, 30005)
	if len(allow) != 1 || allow[0] != "10.0.0.2" {
		t.Fatalf("allowlist must be [10.0.0.2], got %v", allow)
	}

	// No sender at all: empty.
	h2 := setupK9Handler(t)
	receiver2 := c5SeedReceiver(t, h2)
	node2, err := h2.getNodeRecord(10)
	if err != nil {
		t.Fatalf("get receiver node: %v", err)
	}
	if allow := h2.proxyProtocolReceiveAllowlistForForward(receiver2, node2, 30005); len(allow) != 0 {
		t.Fatalf("allowlist must be empty without a sender, got %v", allow)
	}

	// Sender dials a different port: no match.
	h3 := setupK9Handler(t)
	receiver3 := c5SeedReceiver(t, h3)
	c5SeedSender(t, h3, "10.0.0.1:39999", 11, 35005)
	node3, err := h3.getNodeRecord(10)
	if err != nil {
		t.Fatalf("get receiver node: %v", err)
	}
	if allow := h3.proxyProtocolReceiveAllowlistForForward(receiver3, node3, 30005); len(allow) != 0 {
		t.Fatalf("allowlist must be empty when the sender dials another port, got %v", allow)
	}

	// Hostname target: never matched (no DNS here).
	h4 := setupK9Handler(t)
	receiver4 := c5SeedReceiver(t, h4)
	c5SeedSender(t, h4, "node10.example.invalid:30005", 11, 35005)
	node4, err := h4.getNodeRecord(10)
	if err != nil {
		t.Fatalf("get receiver node: %v", err)
	}
	if allow := h4.proxyProtocolReceiveAllowlistForForward(receiver4, node4, 30005); len(allow) != 0 {
		t.Fatalf("allowlist must be empty for a hostname target, got %v", allow)
	}

	// Loopback target with the sender's entry on the same node:
	// the dial originates locally, so the loopback literals are
	// trusted (the dialer is a local FLMX forward service).
	h5 := setupK9Handler(t)
	receiver5 := c5SeedReceiver(t, h5)
	c5SeedSender(t, h5, "127.0.0.1:30005", 10, 30006)
	node5, err := h5.getNodeRecord(10)
	if err != nil {
		t.Fatalf("get receiver node: %v", err)
	}
	allow5 := h5.proxyProtocolReceiveAllowlistForForward(receiver5, node5, 30005)
	joined := strings.Join(allow5, ",")
	if !strings.Contains(joined, "127.0.0.1") || !strings.Contains(joined, "::1") {
		t.Fatalf("loopback sender allowlist must contain 127.0.0.1 and ::1, got %v", allow5)
	}
}

func TestAuditC5ProxyProtocolReceiveNoAllowlistDropsReceive(t *testing.T) {
	forward := &forwardRecord{
		ID:                   1,
		UserID:               2,
		TunnelID:             3,
		RemoteAddr:           "1.1.1.1:443",
		Strategy:             "fifo",
		ProxyProtocolReceive: 1,
		ProxyProtocolSend:    2,
	}
	node := &nodeRecord{
		TCPListenAddr: "0.0.0.0",
		UDPListenAddr: "0.0.0.0",
	}
	// Fail-closed: receive=1 with no allowlist must arm nothing.
	services := buildForwardServiceConfigs("1_2_3", forward, &tunnelRecord{Type: 2}, node, 4001, "", forwardRuntimeLimiters{}, false, nil)
	for _, service := range services {
		serviceMetadata, _ := service["metadata"].(map[string]interface{})
		if _, ok := serviceMetadata["proxyProtocol"]; ok {
			t.Fatalf("service %q must not carry proxyProtocol without an allowlist: %v", service["name"], serviceMetadata)
		}
		if _, ok := serviceMetadata["proxyProtocolAllowlist"]; ok {
			t.Fatalf("service %q must not carry proxyProtocolAllowlist without an allowlist: %v", service["name"], serviceMetadata)
		}
		listenerConfig, _ := service["listener"].(map[string]interface{})
		listenerMetadata, _ := listenerConfig["metadata"].(map[string]interface{})
		if _, ok := listenerMetadata["proxyProtocolAllowlist"]; ok {
			t.Fatalf("service %q listener must not carry proxyProtocolAllowlist without an allowlist: %v", service["name"], listenerMetadata)
		}
		// Send side is unaffected by the receive gate.
		handlerConfig, _ := service["handler"].(map[string]interface{})
		handlerMetadata, _ := handlerConfig["metadata"].(map[string]interface{})
		if strings.HasSuffix(service["name"].(string), "_tcp") && handlerMetadata["proxyProtocol"] != 2 {
			t.Fatalf("tcp send side must stay proxyProtocol 2, got %v", handlerMetadata["proxyProtocol"])
		}
	}

	// Non-empty allowlist arms receive on TCP only, with the list
	// in both the service metadata and the listener metadata.
	services = buildForwardServiceConfigs("1_2_3", forward, &tunnelRecord{Type: 2}, node, 4001, "", forwardRuntimeLimiters{}, false, []string{"10.0.0.2"})
	for _, service := range services {
		name, _ := service["name"].(string)
		serviceMetadata, _ := service["metadata"].(map[string]interface{})
		listenerConfig, _ := service["listener"].(map[string]interface{})
		listenerMetadata, _ := listenerConfig["metadata"].(map[string]interface{})
		if strings.HasSuffix(name, "_udp") {
			if _, ok := serviceMetadata["proxyProtocol"]; ok {
				t.Fatalf("udp service must not carry proxyProtocol: %v", serviceMetadata)
			}
			if _, ok := serviceMetadata["proxyProtocolAllowlist"]; ok {
				t.Fatalf("udp service must not carry proxyProtocolAllowlist: %v", serviceMetadata)
			}
			continue
		}
		if serviceMetadata["proxyProtocol"] != 1 {
			t.Fatalf("tcp service must carry proxyProtocol 1, got %v", serviceMetadata["proxyProtocol"])
		}
		if allow, _ := serviceMetadata["proxyProtocolAllowlist"].([]string); len(allow) != 1 || allow[0] != "10.0.0.2" {
			t.Fatalf("tcp service must carry proxyProtocolAllowlist [10.0.0.2], got %v", serviceMetadata["proxyProtocolAllowlist"])
		}
		if allow, _ := listenerMetadata["proxyProtocolAllowlist"].([]string); len(allow) != 1 || allow[0] != "10.0.0.2" {
			t.Fatalf("tcp listener must carry proxyProtocolAllowlist [10.0.0.2], got %v", listenerMetadata["proxyProtocolAllowlist"])
		}
	}
}
