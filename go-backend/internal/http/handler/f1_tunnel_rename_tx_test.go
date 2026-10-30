package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// F1：tunnelUpdate 的提交段改名复检（B06-001）原先在事务闭包内调用
// 池版 TunnelNameExists——SQLite MaxOpenConns(1) 下事务占住唯一
// 连接，复检无限等待连接释放、自死锁；生产 SQLite 部署任何隧道
// 改名都会挂死（门禁中 TestR11B4TunnelUpdatePartialPreservesStock-
// OutOfRange 挂 23 分钟即此症，其改名走的正是提交段复检路径）。
// 复检已改走事务句柄版 TunnelNameExistsTx。本文件的三格都经
// tunnelUpdate 全链（验证段→提交段），且每次调用带 20s 守卫：
// 回归时快速变红，不再挂死整个 handler 包。

func f1PostTunnelUpdate(t *testing.T, h *Handler, body string) (int, string) {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		res := httptest.NewRecorder()
		h.tunnelUpdate(res, httptest.NewRequest(http.MethodPost, "/api/v1/tunnel/update", bytes.NewReader([]byte(body))))
		done <- res
	}()
	select {
	case res := <-done:
		return decodeEnvelopeA204(t, res)
	case <-time.After(20 * time.Second):
		t.Fatal("tunnelUpdate did not return within 20s — transaction self-deadlock regression (F1)")
		return 0, ""
	}
}

// 改名到未占用新名：提交段复检放行、改名落库（F1 的挂死 repro 面）。
func TestF1TunnelUpdateRenameToFreshNameSucceeds(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 91, "f1-before", "", 0)
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":91,"name":"f1-after","type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("rename to a fresh name must succeed, code=%d msg=%s", code, msg)
	}
	if _, _, count := r11b4TunnelRow(t, h, "f1-after"); count != 1 {
		t.Fatalf("renamed tunnel row count = %d, want 1", count)
	}
	if _, _, count := r11b4TunnelRow(t, h, "f1-before"); count != 0 {
		t.Fatalf("old name rows = %d, want 0", count)
	}
}

// f1SeedTunnelRowOnly 只插隧道行不插 chain 行：seedProbeTargetTunnel
// 给每条隧道插同一 (node 10, port 30001) 的 chain 行，第二条会撞
// chain_tunnel 唯一索引。改名拒绝在预检段即返回，被改名隧道无需
// chain 行即可走到预检。
func f1SeedTunnelRowOnly(t *testing.T, h *Handler, id int64, name string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(?, ?, 1, 1, 'tls', 1, ?, ?, 1, ?, '', '', 0)
	`, id, name, now, now, id).Error; err != nil {
		t.Fatalf("insert tunnel row: %v", err)
	}
}

// 改名撞他人名：仍被拒（验证段预检命中），且原名不变。
func TestF1TunnelUpdateRenameToExistingRejected(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 92, "f1-keep", "", 0)
	f1SeedTunnelRowOnly(t, h, 93, "f1-victim")
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":93,"name":"f1-keep","type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code == 0 {
		t.Fatal("rename to an existing tunnel name must be rejected")
	}
	if !strings.Contains(msg, "隧道名称重复") {
		t.Fatalf("rejection must name 隧道名称重复, got %q", msg)
	}
	if _, _, count := r11b4TunnelRow(t, h, "f1-victim"); count != 1 {
		t.Fatalf("rejected rename must keep the original row, count = %d", count)
	}
}

// 提交自身原名：同名即自身不判重，更新成功（同 C03 契约口径，
// handler 直调面补一格且带守卫）。
func TestF1TunnelUpdateKeepingOwnNameSucceeds(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 94, "f1-own", "", 0)
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":94,"name":"f1-own","type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code != 0 {
		t.Fatalf("keeping the tunnel's own name must succeed, code=%d msg=%s", code, msg)
	}
	if strings.Contains(msg, "隧道名称重复") {
		t.Fatalf("own name must not be reported as duplicate, got %q", msg)
	}
}
