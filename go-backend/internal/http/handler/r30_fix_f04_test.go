package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/store/repo"
)

// R30 写侧归一化 F-04（用户 2026-10-26 拍板）：节点 create/update
// 入库时对 serverIp / ServerIPv4 / ServerIPv6 用 stripShareHostPort
// 剥离 :port 后再存。IPv6 括号按 stripShareHostPort 现有语义处理
// （去括号，与各消费点一致）。非法值（SplitHostPort 失败）原样
// 通过。未提交的字段保持存量原值（存量不改写）。extraIPs 不在
// 本次三字段口径内（仍按原定在转发 intake 收敛，见 R30-FIX-F01）。

func r30f04CreateNode(t *testing.T, h *Handler, payload string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/create", bytes.NewReader([]byte(payload)))
	rec := httptest.NewRecorder()
	h.nodeCreate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeCreate response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("nodeCreate failed: %+v body=%s", out, rec.Body.String())
	}
}

func r30f04NodeAddrs(t *testing.T, r *repo.Repository, name string) (string, string, string) {
	t.Helper()
	var serverIP, v4, v6 string
	row := r.DB().Raw(`SELECT server_ip, COALESCE(server_ip_v4,''), COALESCE(server_ip_v6,'') FROM node WHERE name = ?`, name).Row()
	if err := row.Scan(&serverIP, &v4, &v6); err != nil {
		t.Fatalf("query node %q: %v", name, err)
	}
	return serverIP, v4, v6
}

func TestR30FixF04NodeCreateStripsPortedAddresses(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f04.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r30f04-c","serverIp":"198.51.100.77:8443","serverIpV4":"198.51.100.78:8443","serverIpV6":"[2001:db8::1]:8443","forwardMode":"agent"}`)
	gotIP, gotV4, gotV6 := r30f04NodeAddrs(t, r, "r30f04-c")
	if gotIP != "198.51.100.77" {
		t.Fatalf("serverIp stored %q, want %q", gotIP, "198.51.100.77")
	}
	if gotV4 != "198.51.100.78" {
		t.Fatalf("serverIpV4 stored %q, want %q", gotV4, "198.51.100.78")
	}
	if gotV6 != "2001:db8::1" {
		t.Fatalf("serverIpV6 stored %q, want %q (bracketed v6 with :port)", gotV6, "2001:db8::1")
	}
}

func TestR30FixF04NodeCreateBareAndBracketedControl(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f04b.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r30f04-bare","serverIp":"198.51.100.77","serverIpV4":"198.51.100.78","serverIpV6":"[2001:db8::1]","forwardMode":"agent"}`)
	gotIP, gotV4, gotV6 := r30f04NodeAddrs(t, r, "r30f04-bare")
	if gotIP != "198.51.100.77" || gotV4 != "198.51.100.78" || gotV6 != "2001:db8::1" {
		t.Fatalf("bare/bracketed control stored %q/%q/%q, want 198.51.100.77/198.51.100.78/2001:db8::1", gotIP, gotV4, gotV6)
	}
}

func TestR30FixF04NodeCreateInvalidValuePassesThrough(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f04c.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	// 非法值（非 host:port 形态）原样落库，不被剥离逻辑改写或拒收。
	r30f04CreateNode(t, h, `{"name":"r30f04-inv","serverIp":"198.51.100.77","serverIpV4":"not-an-ip","serverIpV6":"2001:db8::1","forwardMode":"agent"}`)
	gotIP, gotV4, gotV6 := r30f04NodeAddrs(t, r, "r30f04-inv")
	if gotIP != "198.51.100.77" || gotV4 != "not-an-ip" || gotV6 != "2001:db8::1" {
		t.Fatalf("invalid-value control stored %q/%q/%q, want 198.51.100.77/not-an-ip/2001:db8::1", gotIP, gotV4, gotV6)
	}
}

func TestR30FixF04NodeUpdateStripsPortedAddresses(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f04d.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	r30f04CreateNode(t, h, `{"name":"r30f04-u","serverIp":"198.51.100.77","serverIpV4":"198.51.100.78","serverIpV6":"2001:db8::1","forwardMode":"agent"}`)
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, "r30f04-u").Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("lookup node id: %d %v", id, err)
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"id": id, "serverIp": "203.0.113.9:6366",
		"serverIpV4": "203.0.113.10:6366", "serverIpV6": "[2001:db8::9]:6366",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("nodeUpdate failed: %+v body=%s", out, rec.Body.String())
	}
	gotIP, gotV4, gotV6 := r30f04NodeAddrs(t, r, "r30f04-u")
	if gotIP != "203.0.113.9" || gotV4 != "203.0.113.10" || gotV6 != "2001:db8::9" {
		t.Fatalf("update stored %q/%q/%q, want 203.0.113.9/203.0.113.10/2001:db8::9", gotIP, gotV4, gotV6)
	}
}

func TestR30FixF04NodeUpdateUntouchedFieldKeepsStock(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r30-f04e.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	// 存量带端口值（经直接写库模拟旧数据）：只改 name 的更新不得
	// 顺手改写未提交字段——存量不改写。
	now := int64(1793000000000)
	if err := r.DB().Exec(`
		INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(?, ?, ?, ?, ?, '30000-30010', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`,
		"r30f04-stock", "r30f04-stock-secret", "198.51.100.77:8443", "198.51.100.78:8443", "[2001:db8::1]:8443", now, now).Error; err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := r.DB().Raw(`SELECT id FROM node WHERE name = ?`, "r30f04-stock").Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"id": id, "name": "r30f04-stock-renamed"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/node/update", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.nodeUpdate(rec, req)
	var out response.R
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode nodeUpdate response: %v body=%s", err, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("nodeUpdate failed: %+v body=%s", out, rec.Body.String())
	}
	gotIP, gotV4, gotV6 := r30f04NodeAddrs(t, r, "r30f04-stock-renamed")
	if gotIP != "198.51.100.77:8443" || gotV4 != "198.51.100.78:8443" || gotV6 != "[2001:db8::1]:8443" {
		t.Fatalf("untouched stock fields must stay verbatim, got %q/%q/%q", gotIP, gotV4, gotV6)
	}
}
