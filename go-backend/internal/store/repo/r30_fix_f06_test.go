package repo

import (
	"testing"
	"time"
)

// R30 写侧归一化 F-06（用户 2026-10-26 拍板）：联邦同步建节点
// （nodeImport → CreateRemoteNode）亦属节点入库语义——serverIP
// 同口径剥离 :port 后再存（repo 层 stripIngressHostPort）。
// 远端 ServerIP 在本侧仅作展示/分享（联邦流量走 RemoteURL），
// 剥离后仍为合法展示值；ServerIPV4/V6 在远端节点行恒为空。

func TestR30FixF06CreateRemoteNodeStripsPortedServerIP(t *testing.T) {
	r := openBackupRepo(t, "r30-f06.db")
	now := time.Now().UnixMilli()
	if err := r.CreateRemoteNode("remote-x", "secret", "198.51.100.77:6366", "1000-65535", now, 1, 0, "https://example.invalid", "tok", "{}"); err != nil {
		t.Fatalf("CreateRemoteNode: %v", err)
	}
	var serverIP string
	if err := r.DB().Raw(`SELECT server_ip FROM node WHERE name = ?`, "remote-x").Scan(&serverIP).Error; err != nil {
		t.Fatalf("query remote node: %v", err)
	}
	if serverIP != "198.51.100.77" {
		t.Fatalf("remote node serverIp = %q, want %q", serverIP, "198.51.100.77")
	}
}

func TestR30FixF06CreateRemoteNodeBareAndV6Control(t *testing.T) {
	r := openBackupRepo(t, "r30-f06b.db")
	now := time.Now().UnixMilli()
	if err := r.CreateRemoteNode("remote-bare", "secret", "198.51.100.77", "1000-65535", now, 1, 0, "https://example.invalid", "tok", "{}"); err != nil {
		t.Fatalf("CreateRemoteNode bare: %v", err)
	}
	if err := r.CreateRemoteNode("remote-v6", "secret", "[2001:db8::1]:8443", "1000-65535", now, 1, 1, "https://example.invalid", "tok", "{}"); err != nil {
		t.Fatalf("CreateRemoteNode v6: %v", err)
	}
	var bare, v6 string
	if err := r.DB().Raw(`SELECT server_ip FROM node WHERE name = ?`, "remote-bare").Scan(&bare).Error; err != nil {
		t.Fatalf("query remote-bare: %v", err)
	}
	if err := r.DB().Raw(`SELECT server_ip FROM node WHERE name = ?`, "remote-v6").Scan(&v6).Error; err != nil {
		t.Fatalf("query remote-v6: %v", err)
	}
	if bare != "198.51.100.77" || v6 != "2001:db8::1" {
		t.Fatalf("control stored bare=%q v6=%q, want 198.51.100.77/2001:db8::1", bare, v6)
	}
}
