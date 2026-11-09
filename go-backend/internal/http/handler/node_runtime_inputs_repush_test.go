package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

// S2-4/5 + S2-6 (A1 同族, LOOKBACK-final-212 sec2): node/update
// persisted three more generation inputs without any re-push, so a
// running agent kept the old generated shape until an unrelated
// redeploy or reconnect:
//
//   - interfaceName — service metadata "interface" on forward
//     services and chain hops/exit services;
//   - tcpListenAddr / udpListenAddr — the listen address baked
//     into every generated service;
//   - the node's own address — embedded, in derived form, in the
//     PREVIOUS hop's chain config of every tunnel the node joins
//     (buildTunnelChainConfig → selectTunnelDialHost), a third
//     materialization the addrChanged refresh set (inbound
//     whitelists + Managed relay RemoteAddrs) does not cover.
//
// All three now funnel into the same guarded whole-node repush A1
// added for idleTimeoutSeconds (repushNodeRuntimeAfterSettingChange:
// real change only, nftables/offline skipped, offline nodes
// converge via onNodeOnline). These tests pin the behaviour at the
// command stream of a live mock agent, not at the DB row.

func postNodeUpdateOK(t *testing.T, a *cleanupAgent, payload map[string]interface{}) {
	t.Helper()
	res := postJSONToHandler(t, a.h.nodeUpdate, payload)
	var env map[string]interface{}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, res.Body.String())
	}
	if env["code"].(float64) != 0 {
		t.Fatalf("nodeUpdate must succeed, got %v", env)
	}
}

// waitForServicePayload polls for an UpdateService command recorded
// at index >= from whose payload contains substr.
func waitForServicePayload(t *testing.T, a *cleanupAgent, from int, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmds := a.commandsOfType("UpdateService")
		if from <= len(cmds) {
			for _, cmd := range cmds[from:] {
				if strings.Contains(string(cmd.Data), substr) {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no UpdateService containing %q reached the agent within %v; commands=%v",
		substr, timeout, r19F03CommandTypes(t, a))
}

// assertNoNewUpdateServices is the shared negative leg: after a
// settle window the UpdateService count must equal the baseline.
func assertNoNewUpdateServices(t *testing.T, a *cleanupAgent, baseline int, context string) {
	t.Helper()
	time.Sleep(2 * time.Second)
	a.probe(t)
	if got := len(a.commandsOfType("UpdateService")); got != baseline {
		t.Fatalf("%s: got %d extra UpdateService commands", context, got-baseline)
	}
}

func TestNodeUpdateInterfaceNameChangeRepushesServices(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	postNodeUpdateOK(t, a, map[string]interface{}{"id": 1, "interfaceName": "eth9"})
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.InterfaceName.String != "eth9" {
		t.Fatalf("interfaceName not persisted: %+v", node.InterfaceName)
	}
	// Forward services on a type-1 tunnel carry the node's
	// interface name as service metadata (buildForwardServiceConfigs).
	waitForServicePayload(t, a, baseCount, `"eth9"`, 10*time.Second)

	// Re-submitting the SAME value is not a change: drain the
	// first burst, then the count must not move (A1 pattern).
	waitForCommandQuiescence(t, a, "UpdateService", 2*time.Second, 20*time.Second)
	a.probe(t)
	midCount := len(a.commandsOfType("UpdateService"))
	postNodeUpdateOK(t, a, map[string]interface{}{"id": 1, "interfaceName": "eth9"})
	time.Sleep(1500 * time.Millisecond)
	a.probe(t)
	waitForCommandQuiescence(t, a, "UpdateService", time.Second, 10*time.Second)
	if got := len(a.commandsOfType("UpdateService")); got != midCount {
		t.Fatalf("unchanged interfaceName triggered %d extra UpdateService commands", got-midCount)
	}
}

func TestNodeUpdateListenAddrChangeRepushesServices(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	postNodeUpdateOK(t, a, map[string]interface{}{
		"id": 1, "tcpListenAddr": "10.99.0.1", "udpListenAddr": "10.99.0.2",
	})
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.TCPListenAddr != "10.99.0.1" || node.UDPListenAddr != "10.99.0.2" {
		t.Fatalf("listen addrs not persisted: tcp=%q udp=%q", node.TCPListenAddr, node.UDPListenAddr)
	}
	// Generated services bind the per-protocol listen address:
	// TCP services carry the TCP value, UDP services the UDP one.
	waitForServicePayload(t, a, baseCount, "10.99.0.1", 10*time.Second)
	waitForServicePayload(t, a, baseCount, "10.99.0.2", 10*time.Second)

	waitForCommandQuiescence(t, a, "UpdateService", 2*time.Second, 20*time.Second)
	a.probe(t)
	midCount := len(a.commandsOfType("UpdateService"))
	postNodeUpdateOK(t, a, map[string]interface{}{
		"id": 1, "tcpListenAddr": "10.99.0.1", "udpListenAddr": "10.99.0.2",
	})
	time.Sleep(1500 * time.Millisecond)
	a.probe(t)
	waitForCommandQuiescence(t, a, "UpdateService", time.Second, 10*time.Second)
	if got := len(a.commandsOfType("UpdateService")); got != midCount {
		t.Fatalf("unchanged listen addrs triggered %d extra UpdateService commands", got-midCount)
	}
}

// The repush funnel's skip branches (A1 review noted they had no
// dedicated test): an offline node must not be repushed — it
// converges from the DB on reconnect — but the edit itself still
// persists.
func TestNodeUpdateRuntimeInputChangeSkipsOfflineNode(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	if err := a.h.repo.UpdateNodeStatus(1, 0); err != nil {
		t.Fatalf("set node offline: %v", err)
	}
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	postNodeUpdateOK(t, a, map[string]interface{}{"id": 1, "interfaceName": "eth9"})
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.InterfaceName.String != "eth9" {
		t.Fatalf("offline edit must still persist: %+v", node.InterfaceName)
	}
	assertNoNewUpdateServices(t, a, baseCount, "offline node must not be repushed")
}

// nftables nodes have no gost runtime to re-push; the setting is
// inert there and the funnel must skip them even while online.
func TestNodeUpdateRuntimeInputChangeSkipsNftablesNode(t *testing.T) {
	a := newCleanupAgent(t)
	seedR19F03Forward(t, a, 0, 0)
	if err := a.h.repo.DB().Model(&model.Node{}).Where("id = ?", 1).Update("forward_mode", "nftables").Error; err != nil {
		t.Fatalf("set node forward_mode=nftables: %v", err)
	}
	a.probe(t)
	baseCount := len(a.commandsOfType("UpdateService"))

	postNodeUpdateOK(t, a, map[string]interface{}{"id": 1, "interfaceName": "eth9"})
	node, err := a.h.repo.GetNodeByID(1)
	if err != nil || node == nil {
		t.Fatalf("reload node: %v", err)
	}
	if node.InterfaceName.String != "eth9" {
		t.Fatalf("nftables edit must still persist: %+v", node.InterfaceName)
	}
	assertNoNewUpdateServices(t, a, baseCount, "nftables node must not be repushed")
}

// S2-6: node 1 is the entry of a type-2 tunnel whose exit is
// node 2. The entry's chain config embeds the exit's dial address
// (derived from node 2's ServerIP at generation time). Editing
// node 2's address must re-push the entry's chain with the new
// address — before the fix nothing regenerated it, so the entry
// kept dialing the old address until a tunnel edit or a
// participant reconnect happened to rebuild the tunnel.
func TestNodeUpdateAddressChangeRepushesChainDialAddress(t *testing.T) {
	a := newCleanupAgent(t)
	now := time.Now().UnixMilli()
	if err := a.h.repo.DB().Create(&model.Node{
		ID: 2, Name: "exit-node", Secret: "s2-6-exit-secret",
		ServerIP: "203.0.113.10", Port: "30000", Status: 1,
	}).Error; err != nil {
		t.Fatalf("create exit node: %v", err)
	}
	if err := a.h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(77, 's2-6-chain', 1, 2, 'tls', 1, ?, ?, 1, 77, '', '', 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	for _, row := range []struct {
		chainType string
		nodeID    int64
		port      interface{}
	}{
		{"1", 1, nil},
		{"3", 2, 35009},
	} {
		if err := a.h.repo.DB().Exec(`
			INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
			VALUES(77, ?, ?, ?, 'round', 1, 'tls')
		`, row.chainType, row.nodeID, row.port).Error; err != nil {
			t.Fatalf("insert chain row: %v", err)
		}
	}
	a.probe(t)

	postNodeUpdateOK(t, a, map[string]interface{}{"id": 2, "serverIp": "198.51.100.77"})
	node, err := a.h.repo.GetNodeByID(2)
	if err != nil || node == nil {
		t.Fatalf("reload exit node: %v", err)
	}
	if node.ServerIP != "198.51.100.77" {
		t.Fatalf("address not persisted: %q", node.ServerIP)
	}

	deadline := time.Now().Add(15 * time.Second)
	found := false
	for time.Now().Before(deadline) && !found {
		for _, cmdType := range []string{"AddChains", "UpdateChains"} {
			for _, cmd := range a.commandsOfType(cmdType) {
				if strings.Contains(string(cmd.Data), "198.51.100.77:35009") {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !found {
		t.Fatalf("no chain config with new dial address 198.51.100.77:35009 reached the entry node after the address change (S2-6); commands=%v",
			r19F03CommandTypes(t, a))
	}
	// The tunnel was never deployed before this edit, so no chain
	// payload may carry the old address at all.
	for _, cmdType := range []string{"AddChains", "UpdateChains"} {
		for _, cmd := range a.commandsOfType(cmdType) {
			if strings.Contains(string(cmd.Data), "203.0.113.10") {
				t.Fatalf("chain config still carries the old dial address: %s", string(cmd.Data))
			}
		}
	}
}
