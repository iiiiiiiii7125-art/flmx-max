package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R5A-01 回归：节点 forwardMode 切换不得静默丢 SSH 配置。
// 旧口径：resolveNodeSSHConfig 在 forwardMode != "nftables" 时直接
// 删行；切回 nftables 时更新路径 preserveSecrets=true 且模式切换
// 请求本身不带 sshConfig 整段，V2-06 守卫直接 return nil“整段
// 不动”——行已被删，不动就是永远删了，全程无报错无提示，节点
// 静默脱离 SSH 采集集。删行分支自 v1.0.0（2026-10-18，df621cfa）
// 就有，V2-06 守卫自 2026-10-21（551e854f）；最近才频繁踩到是
// 因为 nftables 模式拦装内核（code -1）逼大家走“agent 装内核再
// 切回”——即用户唯一真实的切换场景。修法：切换保留 sshConfig；
// 采集枚举（ListNftablesNodesForCollection）只取 forward_mode =
// nftables 的节点，保留的行在 agent 模式下是惰性的，切回即恢复
// 采集，不会双采也不会误采。

func TestR5A01ModeSwitchPreservesSSHConfig(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r5a01.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, port, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, forward_mode)
		VALUES('nft-r5a01', 's', '198.51.100.41', '30000-30010', ?, ?, 1, '[::]', '[::]', 'nftables')
	`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	if err := r.UpsertNodeSSHConfig(nodeID, repo.NftSSHConfigInput{
		Host: "198.51.100.41", Port: 2222, Username: "root",
		AuthType: "password", Password: "secret", SudoMode: "none",
	}, nowMs); err != nil {
		t.Fatalf("seed ssh config: %v", err)
	}
	readCfg := func() *struct {
		host string
		port int
		user string
	} {
		cfg, err := r.GetNodeSSHConfig(nodeID)
		if err != nil || cfg == nil {
			t.Fatalf("get ssh config: %v", err)
		}
		return &struct {
			host string
			port int
			user string
		}{cfg.Host, cfg.Port, cfg.Username}
	}
	if got := readCfg(); got.port != 2222 {
		t.Fatalf("seed ssh port = %d, want 2222", got.port)
	}

	// ① 切到 agent（请求不带 sshConfig，与面板表单/C2 workaround 同形）：
	// 必须 code 0，且已存配置原样保留。
	res := postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id": nodeID, "forwardMode": "agent",
	})
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("switch to agent must succeed, code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if got := readCfg(); got.host != "198.51.100.41" || got.port != 2222 || got.user != "root" {
		t.Fatalf("R5A-01: ssh config lost by switch to agent, got %+v", got)
	}

	// ② 切回 nftables（请求同样不带 sshConfig）：必须 code 0，且配置
	// 仍在——采集随即恢复，无需重下发。
	res = postJSONToHandler(t, h.nodeUpdate, map[string]interface{}{
		"id": nodeID, "forwardMode": "nftables",
	})
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("switch back to nftables must succeed, code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if got := readCfg(); got.host != "198.51.100.41" || got.port != 2222 || got.user != "root" {
		t.Fatalf("R5A-01: ssh config lost by switch back to nftables, got %+v", got)
	}

	// ③ agent 模式创建时带的 sshConfig 不落库（无行可删、无行可留，
	// 与旧口径可观察行为一致：忽略、不报错）。
	res = postJSONToHandler(t, h.nodeCreate, map[string]interface{}{
		"name": "agent-r5a01", "serverIp": "198.51.100.42",
		"port": "30000-30010", "forwardMode": "agent",
		"sshConfig": map[string]interface{}{
			"host": "198.51.100.42", "port": 22, "username": "root",
			"authType": "password", "password": "secret",
		},
	})
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("agent-mode create must succeed, code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	agentNodeID := mustLastInsertID(t, r, "node")
	if cfg, _ := r.GetNodeSSHConfig(agentNodeID); cfg != nil {
		t.Fatalf("agent-mode create must not store sshConfig, got %+v", cfg)
	}
}
