package handler

import (
	"testing"
)

func TestValidateNodeIdleTimeoutSeconds(t *testing.T) {
	for _, v := range []int{0, 1, 300, 86400} {
		if err := validateNodeIdleTimeoutSeconds(v); err != nil {
			t.Fatalf("expected %d to be valid, got %v", v, err)
		}
	}
	for _, v := range []int{-1, -300, 86401, 1 << 30} {
		if err := validateNodeIdleTimeoutSeconds(v); err == nil {
			t.Fatalf("expected %d to be rejected", v)
		}
	}
}

func TestNodeIdleTimeoutFromReq(t *testing.T) {
	if _, present, err := nodeIdleTimeoutFromReq(map[string]interface{}{}); present || err != nil {
		t.Fatalf("absent field: present=%v err=%v, want absent without error", present, err)
	}
	seconds, present, err := nodeIdleTimeoutFromReq(map[string]interface{}{"idleTimeoutSeconds": float64(300)})
	if !present || err != nil || seconds != 300 {
		t.Fatalf("present 300: got (%d, %v, %v)", seconds, present, err)
	}
	if _, _, err := nodeIdleTimeoutFromReq(map[string]interface{}{"idleTimeoutSeconds": float64(86401)}); err == nil {
		t.Fatal("expected out-of-range value to be rejected")
	}
}

// The node's idleTimeoutSeconds reaches the agent as service-level
// metadata on every generated forward service; 0 (the default)
// must inject nothing, so an unconfigured node's services stay
// byte-identical to the pre-hotfix shape.
func TestBuildForwardServiceConfigs_IdleTimeoutMetadata(t *testing.T) {
	forward := &forwardRecord{RemoteAddr: "1.2.3.4:80", Strategy: "fifo", TunnelID: 7}

	node := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]", IdleTimeoutSeconds: 300}
	services := buildForwardServiceConfigs("1_2_0", forward, nil, node, 22000, "", forwardRuntimeLimiters{}, false)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	for _, svc := range services {
		md, _ := svc["metadata"].(map[string]interface{})
		if md["idleTimeout"] != 300 {
			t.Fatalf("service %v: metadata.idleTimeout = %v, want 300", svc["name"], md["idleTimeout"])
		}
	}

	nodeOff := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	services = buildForwardServiceConfigs("1_2_0", forward, nil, nodeOff, 22000, "", forwardRuntimeLimiters{}, false)
	for _, svc := range services {
		if md, ok := svc["metadata"].(map[string]interface{}); ok {
			if _, present := md["idleTimeout"]; present {
				t.Fatalf("service %v: idleTimeout injected while disabled", svc["name"])
			}
		}
	}
}
