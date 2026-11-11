package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// Zombie-session root cure (edge reaper + mid-hop backstop):
// the three node fields are panel-authoritative and ride to the
// agent as service metadata, exactly like idleTimeoutSeconds.
// These tests pin the panel half of that contract: validation
// ranges, presence-semantics parsing, emission at both builder
// sites (keys only when > 0), the reapRole topology stamps, and
// the change-triggered repush (F-212-A: a stored-but-never-pushed
// knob is a dead knob).

func TestValidateNodeReapWindowSeconds(t *testing.T) {
	for _, v := range []int{0, 60, 900, 3600, 86400, 1 << 20} {
		if err := validateNodeReapWindowSeconds(v, "窗口"); err != nil {
			t.Fatalf("expected %d to be valid, got %v", v, err)
		}
	}
	for _, v := range []int{1, 30, 59, -1, -60} {
		if err := validateNodeReapWindowSeconds(v, "窗口"); err == nil {
			t.Fatalf("expected %d to be rejected", v)
		}
	}
}

func TestValidateNodeBackstopTrickleBps(t *testing.T) {
	for _, v := range []int{0, 1, 256, 1000000} {
		if err := validateNodeBackstopTrickleBps(v); err != nil {
			t.Fatalf("expected %d to be valid, got %v", v, err)
		}
	}
	for _, v := range []int{-1, 1000001, 1 << 30} {
		if err := validateNodeBackstopTrickleBps(v); err == nil {
			t.Fatalf("expected %d to be rejected", v)
		}
	}
}

func TestNodeReapSettingsFromReq(t *testing.T) {
	got, err := nodeReapSettingsFromReq(map[string]interface{}{})
	if err != nil || got.anyPresent() {
		t.Fatalf("absent fields: got %+v err=%v, want nothing present", got, err)
	}

	got, err = nodeReapSettingsFromReq(map[string]interface{}{
		"edgeIdleTimeoutSeconds":     float64(900),
		"backstopIdleTimeoutSeconds": float64(3600),
		"backstopTrickleBps":         float64(128),
	})
	if err != nil {
		t.Fatalf("valid triple rejected: %v", err)
	}
	if !got.EdgePresent || got.EdgeSeconds != 900 ||
		!got.BackstopPresent || got.BackstopSeconds != 3600 ||
		!got.TricklePresent || got.TrickleBps != 128 {
		t.Fatalf("parsed triple mismatch: %+v", got)
	}

	// Partial presence keeps the other fields absent (A2-04).
	got, err = nodeReapSettingsFromReq(map[string]interface{}{"backstopTrickleBps": float64(0)})
	if err != nil || got.EdgePresent || got.BackstopPresent || !got.TricklePresent || got.TrickleBps != 0 {
		t.Fatalf("partial presence mismatch: %+v err=%v", got, err)
	}

	for key, bad := range map[string]float64{
		"edgeIdleTimeoutSeconds":     59,
		"backstopIdleTimeoutSeconds": 30,
		"backstopTrickleBps":         1000001,
	} {
		if _, err := nodeReapSettingsFromReq(map[string]interface{}{key: bad}); err == nil {
			t.Fatalf("expected %s=%v to be rejected", key, bad)
		}
	}

	// merged: submitted fields override, absent fields keep stock.
	got, _ = nodeReapSettingsFromReq(map[string]interface{}{"edgeIdleTimeoutSeconds": float64(1200)})
	edge, backstop, trickle := got.merged(900, 3600, 256)
	if edge != 1200 || backstop != 3600 || trickle != 256 {
		t.Fatalf("merged = (%d, %d, %d), want (1200, 3600, 256)", edge, backstop, trickle)
	}
}

// Forward services are the client-facing head: they carry the
// reapRole=edge handler stamp unconditionally (role follows
// topology, not the windows), while the three window keys are
// service metadata emitted only when the node value is > 0 —
// an all-zero node must see no reap keys at all.
func TestBuildForwardServiceConfigs_ReapMetadata(t *testing.T) {
	forward := &forwardRecord{RemoteAddr: "1.2.3.4:80", Strategy: "fifo", TunnelID: 7}

	node := &nodeRecord{
		TCPListenAddr: "[::]", UDPListenAddr: "[::]",
		EdgeIdleTimeoutSeconds: 900, BackstopIdleTimeoutSeconds: 3600, BackstopTrickleBps: 128,
	}
	services := buildForwardServiceConfigs("1_2_0", forward, nil, node, 22000, "", forwardRuntimeLimiters{}, false)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	for _, svc := range services {
		md, _ := svc["metadata"].(map[string]interface{})
		if md["edgeIdleTimeout"] != 900 || md["backstopIdleTimeout"] != 3600 || md["backstopTrickleBps"] != 128 {
			t.Fatalf("service %v: reap metadata = edge %v backstop %v trickle %v, want 900/3600/128",
				svc["name"], md["edgeIdleTimeout"], md["backstopIdleTimeout"], md["backstopTrickleBps"])
		}
		handlerMd := svc["handler"].(map[string]interface{})["metadata"].(map[string]interface{})
		if handlerMd["reapRole"] != "edge" {
			t.Fatalf("service %v: handler reapRole = %v, want edge", svc["name"], handlerMd["reapRole"])
		}
	}

	nodeOff := &nodeRecord{TCPListenAddr: "[::]", UDPListenAddr: "[::]"}
	services = buildForwardServiceConfigs("1_2_0", forward, nil, nodeOff, 22000, "", forwardRuntimeLimiters{}, false)
	for _, svc := range services {
		if md, ok := svc["metadata"].(map[string]interface{}); ok {
			for _, key := range []string{"edgeIdleTimeout", "backstopIdleTimeout", "backstopTrickleBps"} {
				if _, present := md[key]; present {
					t.Fatalf("service %v: %s injected while disabled", svc["name"], key)
				}
			}
		}
		handlerMd := svc["handler"].(map[string]interface{})["metadata"].(map[string]interface{})
		if handlerMd["reapRole"] != "edge" {
			t.Fatalf("service %v (disabled): handler reapRole = %v, want edge (topology stamp is unconditional)",
				svc["name"], handlerMd["reapRole"])
		}
	}
}

// Every tunnel segment service (middle ChainType 2 and exit
// ChainType 3 alike) is a mid-hop: reapRole=backstop on the
// relay handler, window keys in service metadata iff > 0.
func TestBuildTunnelChainServiceConfig_ReapMetadata(t *testing.T) {
	h := &Handler{jwtSecret: "rootcure-panel-test"}
	node := &nodeRecord{
		TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2",
		EdgeIdleTimeoutSeconds: 900, BackstopIdleTimeoutSeconds: 3600, BackstopTrickleBps: 128,
	}
	for _, chainType := range []int{2, 3} {
		services := h.buildTunnelChainServiceConfig(99,
			tunnelRuntimeNode{NodeID: 5, Protocol: "tls", Port: 24001, ChainType: chainType}, node, 1)
		if len(services) != 1 {
			t.Fatalf("chainType %d: expected 1 service, got %d", chainType, len(services))
		}
		svc := services[0]
		handlerMd := svc["handler"].(map[string]interface{})["metadata"].(map[string]interface{})
		if handlerMd["reapRole"] != "backstop" {
			t.Fatalf("chainType %d: handler reapRole = %v, want backstop", chainType, handlerMd["reapRole"])
		}
		md, _ := svc["metadata"].(map[string]interface{})
		if md["edgeIdleTimeout"] != 900 || md["backstopIdleTimeout"] != 3600 || md["backstopTrickleBps"] != 128 {
			t.Fatalf("chainType %d: reap metadata = edge %v backstop %v trickle %v, want 900/3600/128",
				chainType, md["edgeIdleTimeout"], md["backstopIdleTimeout"], md["backstopTrickleBps"])
		}
	}

	nodeOff := &nodeRecord{TCPListenAddr: "192.0.2.1", UDPListenAddr: "192.0.2.2"}
	services := h.buildTunnelChainServiceConfig(99,
		tunnelRuntimeNode{NodeID: 5, Protocol: "tls", Port: 24001, ChainType: 2}, nodeOff, 1)
	svc := services[0]
	if md, ok := svc["metadata"].(map[string]interface{}); ok {
		for _, key := range []string{"edgeIdleTimeout", "backstopIdleTimeout", "backstopTrickleBps"} {
			if _, present := md[key]; present {
				t.Fatalf("disabled node: %s injected into segment service", key)
			}
		}
	}
	handlerMd := svc["handler"].(map[string]interface{})["metadata"].(map[string]interface{})
	if handlerMd["reapRole"] != "backstop" {
		t.Fatalf("disabled node: handler reapRole = %v, want backstop (topology stamp is unconditional)", handlerMd["reapRole"])
	}
}

// Write-surface validation: out-of-range values and unparseable
// numerics are rejected by node/update and nothing is persisted.
func TestNodeUpdateReapSettingsValidationRejects(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)

	for _, req := range []map[string]interface{}{
		{"id": 1, "edgeIdleTimeoutSeconds": 59},
		{"id": 1, "backstopIdleTimeoutSeconds": 30},
		{"id": 1, "backstopTrickleBps": 1000001},
		{"id": 1, "edgeIdleTimeoutSeconds": "abc"},
	} {
		res := postJSONToHandler(t, a.h.nodeUpdate, req)
		var env map[string]interface{}
		if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode nodeUpdate response: %v body=%s", err, res.Body.String())
		}
		if env["code"].(float64) == 0 {
			t.Fatalf("nodeUpdate %v must be rejected, got success", req)
		}
	}
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.EdgeIdleTimeoutSeconds != 0 || node.BackstopIdleTimeoutSeconds != 0 || node.BackstopTrickleBps != 0 {
		t.Fatalf("rejected updates must not persist, got %+v", node)
	}
}

// F-212-A mirror for the reap fields: changing
// backstopIdleTimeoutSeconds through node/update must re-push
// the node's services with the new metadata, and re-submitting
// the same value must not re-push again.
func TestNodeUpdateReapSettingsChangeRepushesServices(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	res := postJSONToHandler(t, a.h.nodeUpdate, map[string]interface{}{
		"id": 1, "backstopIdleTimeoutSeconds": 3600,
	})
	var env map[string]interface{}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, res.Body.String())
	}
	if env["code"].(float64) != 0 {
		t.Fatalf("nodeUpdate must succeed, got %v", env)
	}
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.BackstopIdleTimeoutSeconds != 3600 {
		t.Fatalf("value not persisted: %+v", node)
	}

	deadline := time.Now().Add(8 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, cmd := range a.commandsOfType("UpdateService")[baseCount:] {
			if payloadHasKeyFloat(cmd.Data, "backstopIdleTimeout", 3600) {
				found = true
				break
			}
		}
		if !found {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("no UpdateService with backstopIdleTimeout=3600 reached the agent after node/update (F-212-A shape); commands=%v",
			r19F03CommandTypes(t, a))
	}

	// Same-value resubmission: drain to quiescence first (see the
	// idle-timeout mirror test for why), then assert silence.
	waitForCommandQuiescence(t, a, "UpdateService", 2*time.Second, 20*time.Second)
	a.probe(t)
	midCount := len(a.commandsOfType("UpdateService"))
	res = postJSONToHandler(t, a.h.nodeUpdate, map[string]interface{}{
		"id": 1, "backstopIdleTimeoutSeconds": 3600,
	})
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode second nodeUpdate response: %v", err)
	}
	if env["code"].(float64) != 0 {
		t.Fatalf("second nodeUpdate must succeed, got %v", env)
	}
	time.Sleep(1500 * time.Millisecond)
	a.probe(t)
	waitForCommandQuiescence(t, a, "UpdateService", time.Second, 10*time.Second)
	if got := len(a.commandsOfType("UpdateService")); got != midCount {
		t.Fatalf("unchanged backstopIdleTimeoutSeconds triggered %d extra UpdateService commands", got-midCount)
	}
}

// payloadHasKeyFloat reports whether a command payload carries
// the given numeric key/value anywhere in its JSON tree.
func payloadHasKeyFloat(data json.RawMessage, key string, want float64) bool {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return jsonTreeHasKeyValue(v, key, want)
}
