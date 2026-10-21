package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// FIX-B A2-04（BC-06）：userUpdate / tunnelUpdate / nodeUpdate 的「缺省即
// 重置」残余——未传字段被硬默认值覆写（停用用户被重新启用、到期被续
// 一年、隧道倍率复位、nft 节点被翻回 agent 模式）。按 userTunnelUpdate
// 样板改真部分更新：未传字段保持现值，传了才改（含显式传 0）。

func decodeEnvelopeA204(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	return env.Code, env.Msg
}

func TestUserUpdatePartialPreservesFields(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/partial-user.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	exp := nowMs + 30*86400000
	userID, err := r.CreateUser("partialuser", "hash", 1, exp, 1, 1, 100, 1, 0, nowMs)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`UPDATE "user" SET status = 0, num = 3, exp_time = ?, flow = 5, flow_mib = 512, flow_reset_time = 7, max_conn = 9 WHERE id = ?`,
		exp, userID).Error; err != nil {
		t.Fatalf("seed user fields: %v", err)
	}

	res := httptest.NewRecorder()
	h.userUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": userID, "user": "partialuser-renamed",
	}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("userUpdate failed: code=%d msg=%s", code, msg)
	}
	u, err := r.GetUserByID(userID)
	if err != nil || u == nil {
		t.Fatalf("get user: %v", err)
	}
	if u.User != "partialuser-renamed" {
		t.Fatalf("username not updated: %q", u.User)
	}
	if u.Status != 0 {
		t.Fatalf("A2-04: disabled user silently re-enabled by partial update, status=%d", u.Status)
	}
	if u.Num != 3 {
		t.Fatalf("A2-04: num reset to %d, want 3", u.Num)
	}
	if u.ExpTime != exp {
		t.Fatalf("A2-04: expTime changed to %d, want %d (no silent +365d renewal)", u.ExpTime, exp)
	}
	if u.Flow != 5 || u.FlowMiB != 512 {
		t.Fatalf("A2-04: flow reset to %d/%d, want 5/512", u.Flow, u.FlowMiB)
	}
	if u.FlowResetTime != 7 {
		t.Fatalf("A2-04: flowResetTime reset to %d, want 7", u.FlowResetTime)
	}
	if u.MaxConn != 9 {
		t.Fatalf("A2-04: maxConn reset to %d, want 9", u.MaxConn)
	}

	// 显式传值仍生效（含传 0 清零语义）。
	res2 := httptest.NewRecorder()
	h.userUpdate(res2, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": userID, "user": "partialuser-renamed", "status": 1, "num": 0, "maxConn": 0,
	}))
	if code, msg := decodeEnvelopeA204(t, res2); code != 0 {
		t.Fatalf("userUpdate explicit failed: code=%d msg=%s", code, msg)
	}
	u2, _ := r.GetUserByID(userID)
	if u2.Status != 1 || u2.Num != 0 || u2.MaxConn != 0 {
		t.Fatalf("explicit values not applied: status=%d num=%d maxConn=%d", u2.Status, u2.Num, u2.MaxConn)
	}
}

func TestTunnelUpdatePartialPreservesFields(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/partial-tunnel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.16")
	tunnelID := seedTunnelForNftables(t, h, "partial-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET flow = 2, traffic_ratio = 2.5 WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	h.tunnelUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id":       tunnelID,
		"inNodeId": []map[string]interface{}{{"nodeId": nodeID, "protocol": "tls"}},
	}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("tunnelUpdate failed: code=%d msg=%s body=%s", code, msg, res.Body.String())
	}
	var row struct {
		Name         string
		Flow         int64
		TrafficRatio float64
		Type         int
		Status       int
	}
	if err := r.DB().Raw(`SELECT name, flow, traffic_ratio, type, status FROM tunnel WHERE id = ?`, tunnelID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "partial-tunnel" {
		t.Fatalf("A2-04: tunnel name cleared by partial update, got %q", row.Name)
	}
	if row.Flow != 2 {
		t.Fatalf("A2-04: tunnel flow reset to %d, want 2", row.Flow)
	}
	if row.TrafficRatio != 2.5 {
		t.Fatalf("A2-04: trafficRatio reset to %v, want 2.5", row.TrafficRatio)
	}
	if row.Type != 1 || row.Status != 1 {
		t.Fatalf("A2-04: type/status changed to %d/%d, want 1/1", row.Type, row.Status)
	}
}

func TestNodeUpdatePartialPreservesFields(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/partial-node.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nowMs := time.Now().UnixMilli()
	exp := nowMs + 90*86400000
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx, forward_mode, extra_ips, expiry_time, renewal_cycle, remark)
		VALUES('nft-partial', 'np-secret', '198.51.100.20', '198.51.100.20', '', '30000-30010', 'eth0', 'v1', 1, 1, 1, ?, ?, 0, '[::]', '[::]', 0, 'nftables', '198.51.100.21', ?, 'month', 'keepme')
	`, nowMs, nowMs, exp).Error; err != nil {
		t.Fatalf("insert node: %v", err)
	}
	var nodeID int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = 'nft-partial'`).Scan(&nodeID).Error; err != nil {
		t.Fatal(err)
	}
	seedNftablesSSHConfig(t, h, nodeID)

	res := httptest.NewRecorder()
	h.nodeUpdate(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": nodeID}))
	if code, msg := decodeEnvelopeA204(t, res); code != 0 {
		t.Fatalf("nodeUpdate failed: code=%d msg=%s body=%s", code, msg, res.Body.String())
	}
	n, err := r.GetNodeByID(nodeID)
	if err != nil || n == nil {
		t.Fatalf("get node: %v", err)
	}
	if n.Name != "nft-partial" {
		t.Fatalf("A2-04: node name cleared, got %q", n.Name)
	}
	if n.ServerIP != "198.51.100.20" {
		t.Fatalf("A2-04: serverIp cleared, got %q", n.ServerIP)
	}
	if n.Port != "30000-30010" {
		t.Fatalf("A2-04: port range reset to %q, want 30000-30010", n.Port)
	}
	if n.ForwardMode != "nftables" {
		t.Fatalf("A2-04: forwardMode flipped to %q, want nftables (nft node must not revert to agent)", n.ForwardMode)
	}
	if n.TCPListenAddr != "[::]" || n.UDPListenAddr != "[::]" {
		t.Fatalf("A2-04: listen addrs changed to %q/%q", n.TCPListenAddr, n.UDPListenAddr)
	}
	if !n.ExtraIPs.Valid || n.ExtraIPs.String != "198.51.100.21" {
		t.Fatalf("A2-04: extraIPs lost: %+v", n.ExtraIPs)
	}
	if !n.ExpiryTime.Valid || n.ExpiryTime.Int64 != exp {
		t.Fatalf("A2-04: expiryTime lost: %+v", n.ExpiryTime)
	}
	if !n.RenewalCycle.Valid || n.RenewalCycle.String != "month" {
		t.Fatalf("A2-04: renewalCycle lost: %+v", n.RenewalCycle)
	}
	if !n.InterfaceName.Valid || n.InterfaceName.String != "eth0" {
		t.Fatalf("A2-04: interfaceName lost: %+v", n.InterfaceName)
	}
	if !n.Remark.Valid || n.Remark.String != "keepme" {
		t.Fatalf("A2-04: remark lost: %+v", n.Remark)
	}
}
