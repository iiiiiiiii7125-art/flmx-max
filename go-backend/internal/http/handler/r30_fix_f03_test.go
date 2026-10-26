package handler

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// R30 sweep 新面 F-03（产品 P2）：persistNodeSSHConfig 的 SSH host
// 回退取 req["serverIp"] 原值；节点 serverIp 含 :port 且未显式填
// ssh host 时，node_ssh_config.host 存入 "IP:port"，下游
// runner.go:110 以 net.JoinHostPort(host, sshPort) 拼出
// "IP:port:22" 畸形地址，nftables 节点 SSH 拨号失败。显式填的
// sshConfig.host 不动（用户显式输入，非节点字段派生值）。
// 修法：仅 serverIp 派生的回退分支复用 stripShareHostPort。

func TestR30FixF03PersistSSHConfigStripsPortedServerIPFallback(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f03.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.77")
	err = h.persistNodeSSHConfig(nodeID, map[string]interface{}{
		"serverIp": "198.51.100.77:8443",
		"sshConfig": map[string]interface{}{
			"username": "root", "port": 2222,
			"authType": "private_key", "privateKey": "test-key",
		},
	}, "nftables", now, false)
	if err != nil {
		t.Fatalf("persistNodeSSHConfig: %v", err)
	}
	cfg, err := r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.Host != "198.51.100.77" {
		t.Fatalf("ssh config host = %q, want %q (ported serverIp fallback must be stripped)", cfg.Host, "198.51.100.77")
	}
}

func TestR30FixF03PersistSSHConfigExplicitHostUntouched(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f03b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.77")
	err = h.persistNodeSSHConfig(nodeID, map[string]interface{}{
		"serverIp": "198.51.100.77:8443",
		"sshConfig": map[string]interface{}{
			"host": "203.0.113.5", "username": "root", "port": 2222,
			"authType": "private_key", "privateKey": "test-key",
		},
	}, "nftables", now, false)
	if err != nil {
		t.Fatalf("persistNodeSSHConfig: %v", err)
	}
	cfg, err := r.GetNodeSSHConfig(nodeID)
	if err != nil {
		t.Fatalf("get ssh config: %v", err)
	}
	if cfg.Host != "203.0.113.5" {
		t.Fatalf("explicit ssh host must pass through, got %q want %q", cfg.Host, "203.0.113.5")
	}
}
