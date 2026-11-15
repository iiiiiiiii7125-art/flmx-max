package handler

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// C-04: 配额暂停可连续失败，失败期转发继续跑。
// 根因是 PauseService 的 NotFound 自愈重建：在已监听端口上 bind 撞
// EADDRINUSE（登记转发 168 两次失败），而转发继续跑；且
// ForceClosePortConnections 恒返回 nil（tcpkill 缺失亦然），已建立
// 连接排干约 2 分钟（IPv6 连接 tcpkill 碰不到）。

func TestC04AgentSupportsPortProbe(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"2.1.13", true},
		{"2.1.13-cand (debian/amd64)", true},
		{"2.1.14", true},
		{"2.2.0", true},
		{"2.1.12", false},
		{"2.1.8", false},
		{"", false},
		{"dev", false},
	}
	for _, c := range cases {
		if got := agentSupportsPortProbe(c.raw); got != c.want {
			t.Fatalf("agentSupportsPortProbe(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// c04SeedForward creates an active forward on tunnel 41 with one port
// (24510) on the online fake agent (node 1), and stamps the node's agent
// version. Returns the forward record for controlForwardServices.
func c04SeedForward(t *testing.T, a *nodeSwapFakeAgent, agentVersion string) *forwardRecord {
	t.Helper()
	r := a.h.repo
	nowMs := time.Now().UnixMilli()
	userID, err := r.CreateUser("c04-user", "hash", 1, 0, 0, 0, 10, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx)
		VALUES(41, 'c04-tunnel', 1.0, 1, 'tls', 2, ?, ?, 1, 0)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_tunnel(user_id, tunnel_id, num, flow, flow_mib, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(?, 41, 10, 100000, 0, 0, 0, 1, ?, 1)`, userID, nowMs+86400000).Error; err != nil {
		t.Fatalf("insert user_tunnel: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(?, 'c04-user', 'c04-fwd', 41, '203.0.113.1:443', 'fifo', 0, 0, ?, ?, 1, 0)`, userID, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	fwdID := mustLastInsertID(t, r, "c04-fwd")
	if err := r.DB().Exec(`INSERT INTO forward_port(forward_id, node_id, port) VALUES(?, 1, 24510)`, fwdID).Error; err != nil {
		t.Fatalf("insert forward_port: %v", err)
	}
	if err := r.DB().Exec(`UPDATE node SET version = ? WHERE id = 1`, agentVersion).Error; err != nil {
		t.Fatalf("stamp node version: %v", err)
	}
	return &forwardRecord{ID: fwdID, UserID: userID, TunnelID: 41, Status: 1}
}

// c04NotFoundReply answers every PauseService variant with "not found"
// and every UpdateService with success, modelling an agent whose
// registry lost the service.
func c04NotFoundReply(probe func(command cleanupAgentCommand) (bool, string, map[string]interface{})) func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
	return func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		switch command.Type {
		case "PauseService":
			return false, "service not found", nil
		case "UpdateService":
			return true, "", nil
		case "CheckPortListening":
			return probe(command)
		}
		return true, "", nil
	}
}

func c04ProbeSilent(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
	return true, "", map[string]interface{}{"listening": []interface{}{}, "established": []interface{}{}}
}

func c04ProbeListening(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
	return true, "", map[string]interface{}{"listening": []interface{}{24510}, "established": []interface{}{}}
}

// NotFound pause on a probe-capable agent with silent ports: success,
// and no self-heal rebuild is attempted (the filed bind failure).
func TestC04PauseNotFoundSilentPortsSucceedsWithoutRebuild(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = c04NotFoundReply(c04ProbeSilent)
	forward := c04SeedForward(t, a, "2.1.13")

	if err := a.h.controlForwardServices(forward, "PauseService", false); err != nil {
		t.Fatalf("C-04: NotFound pause with silent ports must succeed, got %v", err)
	}
	if got := a.commandsOfType("UpdateService"); len(got) != 0 {
		t.Fatalf("C-04: probe-capable agent must not receive self-heal UpdateService, got %d", len(got))
	}
	if got := a.commandsOfType("CheckPortListening"); len(got) == 0 {
		t.Fatalf("C-04: NotFound pause must verify port state via CheckPortListening")
	}
}

// NotFound pause with the port still listening: precise conflict error,
// no rebuild, no false success.
func TestC04PauseNotFoundOrphanPortFailsLoud(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = c04NotFoundReply(c04ProbeListening)
	forward := c04SeedForward(t, a, "2.1.13")

	err := a.h.controlForwardServices(forward, "PauseService", false)
	if err == nil {
		t.Fatalf("C-04: NotFound pause with a listening port must fail, got nil")
	}
	if !strings.Contains(err.Error(), "仍在监听") {
		t.Fatalf("C-04: orphan-port error must name the listening port, got %v", err)
	}
	if got := a.commandsOfType("UpdateService"); len(got) != 0 {
		t.Fatalf("C-04: orphan-port pause must not attempt self-heal rebuild, got %d UpdateService", len(got))
	}
}

// NotFound pause on an old agent (no probe command): legacy self-heal
// rebuild is still attempted (transitional path).
func TestC04PauseNotFoundOldAgentKeepsSelfHeal(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = c04NotFoundReply(c04ProbeSilent)
	forward := c04SeedForward(t, a, "2.1.12")

	err := a.h.controlForwardServices(forward, "PauseService", false)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("C-04: old-agent NotFound pause must return the NotFound error after heal retry, got %v", err)
	}
	if got := a.commandsOfType("UpdateService"); len(got) == 0 {
		t.Fatalf("C-04: old agent must still receive self-heal UpdateService (transitional path)")
	}
}

// Successful pause verifies the ports are dead via the probe.
func TestC04PauseSuccessVerifiesPortsDead(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		switch command.Type {
		case "PauseService":
			var body struct {
				Services []string `json:"services"`
			}
			_ = json.Unmarshal(command.Data, &body)
			for _, name := range body.Services {
				if strings.HasSuffix(name, "_tcp") || strings.HasSuffix(name, "_udp") {
					return true, "", nil
				}
			}
			return false, "service not found", nil
		case "CheckPortListening":
			return c04ProbeSilent(command)
		}
		return true, "", nil
	}
	forward := c04SeedForward(t, a, "2.1.13")

	if err := a.h.controlForwardServices(forward, "PauseService", false); err != nil {
		t.Fatalf("C-04: successful pause must stay successful, got %v", err)
	}
	if got := a.commandsOfType("CheckPortListening"); len(got) == 0 {
		t.Fatalf("C-04: successful pause must verify ports dead via CheckPortListening")
	}
}

// Successful pause whose ports stay listening: loud error, no false success.
func TestC04PauseSuccessPortStillListeningFails(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		switch command.Type {
		case "PauseService":
			return true, "", nil
		case "CheckPortListening":
			return c04ProbeListening(command)
		}
		return true, "", nil
	}
	forward := c04SeedForward(t, a, "2.1.13")

	err := a.h.controlForwardServices(forward, "PauseService", false)
	if err == nil || !strings.Contains(err.Error(), "仍在监听") {
		t.Fatalf("C-04: pause with ports still listening must fail loud, got %v", err)
	}
}

// Probe helpers: established-only counts, missing `established` field
// (2.1.13 agent) verifies the listener only, probe failure is
// fail-closed for the NotFound check and fail-open after a success.
func TestC04ProbeHelpers(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type != "CheckPortListening" {
			return true, "", nil
		}
		return true, "", map[string]interface{}{"listening": []interface{}{}, "established": []interface{}{24510}}
	}
	if dead, err := a.h.verifyForwardPortsDeadOnNode(1, []int{24510}); err != nil || dead {
		t.Fatalf("C-04: established-only port must not count as dead (dead=%v err=%v)", dead, err)
	}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type != "CheckPortListening" {
			return true, "", nil
		}
		// 2.1.13 agent: no `established` field at all.
		return true, "", map[string]interface{}{"listening": []interface{}{}}
	}
	if dead, err := a.h.verifyForwardPortsDeadOnNode(1, []int{24510}); err != nil || !dead {
		t.Fatalf("C-04: silent listener with absent established field must count as dead (dead=%v err=%v)", dead, err)
	}
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		return false, "node offline", nil
	}
	if _, err := a.h.verifyForwardPortsDeadOnNode(1, []int{24510}); err == nil {
		t.Fatalf("C-04: failed probe must fail closed for the NotFound check")
	}
	if err := a.h.verifyPausedPortsDead(1, []int{24510}); err != nil {
		t.Fatalf("C-04: failed probe must fail open after a successful pause, got %v", err)
	}
}
