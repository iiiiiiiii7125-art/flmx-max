package handler

import (
	"encoding/json"
	"net"
	"testing"

	"go-backend/internal/store/model"
)

// PORT-CLOSURE Lane D — PC-01（P2）：peer_share_runtime_lifecycle.go:76
// 第 4 处 TCPListenAddr 直拼。探针按 ledger §4（S-04 实例：
// TCPListenAddr="203.0.113.7:9999"、runtime.Port=22001）重建：经真实
// applyPeerShareRoleRuntime 下发 UpdateService，断言服务 addr 合法。
// 修复前 addr="203.0.113.7:9999:22001"（too many colons，agent 绑定失败）；
// 修复后 "203.0.113.7:22001"。对照格：裸监听地址产物不变；默认 "[::]"
// 与裸 IPv6 监听地址走 JoinHostPort 规范形态。
func TestPortClosureDPC01PeerShareServiceAddr(t *testing.T) {
	cases := []struct {
		name       string
		listenAddr string
		want       string
	}{
		{"ported-v4", "203.0.113.7:9999", "203.0.113.7:22001"},
		{"bare-v4-control", "203.0.113.7", "203.0.113.7:22001"},
		{"default-any", "[::]", "[::]:22001"},
		{"bare-v6", "2001:db8::1", "[2001:db8::1]:22001"},
		{"bracketed-v6-ported", "[2001:db8::1]:9999", "[2001:db8::1]:22001"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agent := newCleanupAgent(t)
			if err := agent.h.repo.DB().Model(&model.Node{}).Where("id = ?", 1).Update("tcp_listen_addr", c.listenAddr).Error; err != nil {
				t.Fatal(err)
			}
			runtime := roleRuntimeFixture(t, agent, "exit")
			runtime.Port = 22001
			if err := agent.h.applyPeerShareRoleRuntime(runtime); err != nil {
				t.Fatalf("applyPeerShareRoleRuntime: %v", err)
			}
			cmds := agent.commandsOfType("UpdateService")
			if len(cmds) == 0 {
				t.Fatal("no UpdateService command captured")
			}
			var services []map[string]interface{}
			if err := json.Unmarshal(cmds[len(cmds)-1].Data, &services); err != nil || len(services) == 0 {
				t.Fatalf("decode UpdateService data %s: %v", string(cmds[len(cmds)-1].Data), err)
			}
			got, _ := services[0]["addr"].(string)
			if got != c.want {
				t.Errorf("service addr = %q, want %q", got, c.want)
			}
			if _, _, err := net.SplitHostPort(got); err != nil {
				t.Errorf("service addr %q is not a valid host:port: %v", got, err)
			}
		})
	}
}
