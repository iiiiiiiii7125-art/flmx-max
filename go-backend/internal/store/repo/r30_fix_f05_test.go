package repo

import (
	"testing"
	"time"
)

// R30 写侧归一化 F-05（用户 2026-10-26 拍板）：备份恢复亦属节点
// 入库语义——importNodes 对 ServerIP / ServerIPv4 / ServerIPv6
// 同口径剥离 :port 后再存（repo 层 stripIngressHostPort，与
// handler 层 stripShareHostPort 同语义）。存量备份里的带端口值
// 恢复后即为裸 IP；extraIPs 不在本次三字段口径内。

func TestR30FixF05BackupImportStripsPortedNodeAddresses(t *testing.T) {
	source := openBackupRepo(t, "r30-f05-src.db")
	dest := openBackupRepo(t, "r30-f05-dst.db")
	now := time.Now().UnixMilli()
	// 源库经直接写库模拟含端口的旧备份内容（绕过写侧归一化，
	// 还原“备份文件里就是带端口值”的形态）。
	if err := source.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`,
		"r30f05-node", "r30f05-secret", "198.51.100.77:8443", "198.51.100.78:8443", "[2001:db8::1]:8443", now, now).Error; err != nil {
		t.Fatal(err)
	}
	roundTripBackup(t, source, dest)
	var serverIP, v4, v6 string
	row := dest.DB().Raw(`SELECT server_ip, COALESCE(server_ip_v4,''), COALESCE(server_ip_v6,'') FROM node WHERE name = ?`, "r30f05-node").Row()
	if err := row.Scan(&serverIP, &v4, &v6); err != nil {
		t.Fatalf("query imported node: %v", err)
	}
	if serverIP != "198.51.100.77" {
		t.Fatalf("imported serverIp = %q, want %q", serverIP, "198.51.100.77")
	}
	if v4 != "198.51.100.78" {
		t.Fatalf("imported serverIpV4 = %q, want %q", v4, "198.51.100.78")
	}
	if v6 != "2001:db8::1" {
		t.Fatalf("imported serverIpV6 = %q, want %q", v6, "2001:db8::1")
	}
}

func TestR30FixF05BackupImportBareControl(t *testing.T) {
	source := openBackupRepo(t, "r30-f05b-src.db")
	dest := openBackupRepo(t, "r30-f05b-dst.db")
	now := time.Now().UnixMilli()
	if err := source.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`,
		"r30f05-bare", "r30f05b-secret", "198.51.100.77", "198.51.100.78", "2001:db8::1", now, now).Error; err != nil {
		t.Fatal(err)
	}
	roundTripBackup(t, source, dest)
	var serverIP, v4, v6 string
	row := dest.DB().Raw(`SELECT server_ip, COALESCE(server_ip_v4,''), COALESCE(server_ip_v6,'') FROM node WHERE name = ?`, "r30f05-bare").Row()
	if err := row.Scan(&serverIP, &v4, &v6); err != nil {
		t.Fatalf("query imported node: %v", err)
	}
	if serverIP != "198.51.100.77" || v4 != "198.51.100.78" || v6 != "2001:db8::1" {
		t.Fatalf("bare control imported %q/%q/%q, want 198.51.100.77/198.51.100.78/2001:db8::1", serverIP, v4, v6)
	}
}
