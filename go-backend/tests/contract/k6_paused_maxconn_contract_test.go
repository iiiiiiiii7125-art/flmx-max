package contract_test

// Round 3 K6 / B09-002 永久契约回归（由 B09 席探针转正、断言翻转）：
// 用户级 maxConn 变更时，syncUserMaxConnForwards 必须枚举该用户全部
// 转发（含暂停态），暂停转发在变更当刻即收到新的 user_conn_limit 值
// （推送后复位暂停），不得等恢复后仍执行旧值与 DB 永久分叉。
// 修复前：暂停转发被 status=1 过滤跳过，phase 3 零 climiter 推送、
// phase 4 恢复只发 ResumeService——本测试在 phase 3 处先红。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go-backend/internal/auth"
	"go-backend/internal/http/response"
)

type k6Cmd struct {
	Type string
	Data json.RawMessage
}

func k6Post(t *testing.T, router http.Handler, token, path string, payload map[string]interface{}) response.R {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	var out response.R
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

func k6CmdTypes(list []k6Cmd) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Type)
	}
	return out
}

func k6Itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestK6PausedForwardMaxConnResyncsOnUpdate(t *testing.T) {
	secret := "k6-maxconn-secret"
	router, r := setupContractRouter(t, secret)
	server := httptest.NewServer(router)
	defer server.Close()

	adminToken, err := auth.GenerateToken(1, "admin@user", 0, secret)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	now := time.Now().UnixMilli()
	exp := now + 365*24*3600*1000
	stmts := []string{
		`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, max_conn, created_time, updated_time, status) VALUES(2, 'k6_user', 'pwd', 1, ` + k6Itoa(exp) + `, 99999, 0, 0, 1, 10, 5, ` + k6Itoa(now) + `, ` + k6Itoa(now) + `, 1)`,
		`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, in_ip, inx) VALUES(10, 'k6-tunnel', 1.0, 1, 'tls', 99999, ` + k6Itoa(now) + `, ` + k6Itoa(now) + `, 1, NULL, 0)`,
		`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx) VALUES(20, 'k6-node', 'k6-node-secret', '10.22.0.1', '10.22.0.1', '', '32200-32210', '', 'v1', 1, 1, 1, ` + k6Itoa(now) + `, ` + k6Itoa(now) + `, 0, '[::]', '[::]', 0)`,
		`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol) VALUES(10, 1, 20, 32201, 'round', 1, 'tls')`,
		`INSERT INTO user_tunnel(id, user_id, tunnel_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status) VALUES(30, 2, 10, 10, 99999, 0, 0, 1, ` + k6Itoa(exp) + `, 1)`,
		`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx, max_conn) VALUES(40, 2, 'k6_user', 'k6-forward', 10, '1.1.1.1:443', 'fifo', 0, 0, ` + k6Itoa(now) + `, ` + k6Itoa(now) + `, 1, 0, 0)`,
		`INSERT INTO forward_port(forward_id, node_id, port, in_ip) VALUES(40, 20, 32205, '')`,
	}
	for _, s := range stmts {
		if err := r.DB().Exec(s).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	var mu sync.Mutex
	var cmds []k6Cmd
	stopNode := startMockSessionForMaxConn(t, server.URL, "k6-node-secret", func(cmdType string, data json.RawMessage) (bool, string) {
		mu.Lock()
		defer mu.Unlock()
		cp := append(json.RawMessage(nil), data...)
		cmds = append(cmds, k6Cmd{Type: cmdType, Data: cp})
		return false, ""
	})
	defer stopNode()
	waitNodeStatus(t, r, 20, 1)

	snapshot := func() []k6Cmd {
		mu.Lock()
		defer mu.Unlock()
		out := append([]k6Cmd(nil), cmds...)
		cmds = nil
		return out
	}
	climiterValues := func(list []k6Cmd) []string {
		var vals []string
		for _, c := range list {
			if c.Type != "AddCLimiters" && c.Type != "UpdateCLimiters" {
				continue
			}
			if strings.Contains(string(c.Data), "user_conn_limit_2") {
				vals = append(vals, c.Type+":"+string(c.Data))
			}
		}
		return vals
	}
	userUpdate := func(maxConn int) response.R {
		return k6Post(t, router, adminToken, "/api/v1/user/update", map[string]interface{}{
			"id": 2, "user": "k6_user", "flow": 99999, "num": 10,
			"expTime": exp, "flowResetTime": 1, "status": 1, "maxConn": maxConn,
		})
	}

	// Phase 1（对照组）：转发启用中，maxConn 5 -> 37 必须重下发。
	snapshot()
	if out := userUpdate(37); out.Code != 0 {
		t.Fatalf("phase1 user update: code=%d msg=%s", out.Code, out.Msg)
	}
	p1 := snapshot()
	if len(climiterValues(p1)) == 0 || !strings.Contains(strings.Join(climiterValues(p1), " "), "$ 37") {
		t.Fatalf("phase1 control failed: no user_conn_limit_2 $ 37 pushed; cmds=%v", k6CmdTypes(p1))
	}

	// Phase 2：暂停转发。
	if out := k6Post(t, router, adminToken, "/api/v1/forward/pause", map[string]interface{}{"id": 40}); out.Code != 0 {
		t.Fatalf("pause: code=%d msg=%s", out.Code, out.Msg)
	}
	snapshot()

	// Phase 3（B09-002 本体）：暂停期间改 maxConn 37 -> 99，节点必须在
	// 本次更新内收到 $ 99，且转发保持暂停（推送后复位暂停）。
	if out := userUpdate(99); out.Code != 0 {
		t.Fatalf("phase3 user update: code=%d msg=%s", out.Code, out.Msg)
	}
	p3 := snapshot()
	if len(climiterValues(p3)) == 0 || !strings.Contains(strings.Join(climiterValues(p3), " "), "$ 99") {
		t.Fatalf("B09-002: paused forward was skipped on maxConn update — node never received user_conn_limit_2 $ 99; cmds=%v", k6CmdTypes(p3))
	}
	pausedAgain := false
	for _, c := range p3 {
		if c.Type == "PauseService" {
			pausedAgain = true
		}
	}
	if !pausedAgain {
		t.Fatalf("B09-002: sync of a paused forward must re-assert the pause after pushing (PauseService), cmds=%v", k6CmdTypes(p3))
	}
	var fwdStatus int
	if err := r.DB().Raw("SELECT status FROM forward WHERE id = 40").Scan(&fwdStatus).Error; err != nil {
		t.Fatalf("read forward status: %v", err)
	}
	if fwdStatus != 0 {
		t.Fatalf("B09-002: sync must not change the paused status in DB, got %d", fwdStatus)
	}

	// Phase 4：恢复。节点已在 phase 3 收敛到 99，恢复只需 ResumeService。
	if out := k6Post(t, router, adminToken, "/api/v1/forward/resume", map[string]interface{}{"id": 40}); out.Code != 0 {
		t.Fatalf("resume: code=%d msg=%s", out.Code, out.Msg)
	}
	p4 := snapshot()
	resumed := false
	for _, c := range p4 {
		if c.Type == "ResumeService" {
			resumed = true
		}
	}
	if !resumed {
		t.Fatalf("phase4: expected ResumeService, got %v", k6CmdTypes(p4))
	}
	var dbMaxConn int
	if err := r.DB().Raw("SELECT max_conn FROM user WHERE id = 2").Scan(&dbMaxConn).Error; err != nil {
		t.Fatalf("read user max_conn: %v", err)
	}
	if dbMaxConn != 99 {
		t.Fatalf("expected DB max_conn 99, got %d", dbMaxConn)
	}
}
