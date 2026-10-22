package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// X-P3-17 (L-04-3): 批量换隧道逐条 DB 写收敛为单 tx 后的行为守卫——
// 端口替换步被注入失败时，该条计失败且转发保持原隧道与原端口（单 tx
// 回滚，无补偿依赖）；正常条目仍可成功迁移（部分成功契约不变）。
// 原子性本体由 repo 级 TestR1B8_XP317_ChangeForwardTunnelAndPortsAtomic
// 钉死（新方法 ChangeForwardTunnelAndPorts）。
func TestR1B8_XP317_BatchChangeTunnelAtomicPerItem(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp317.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES('xp317-node', 's317', '198.51.100.33', '198.51.100.33', '', '', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	tunnel1 := seedTunnelForNftables(t, h, "xp317-t1", nodeID)
	tunnel2 := seedTunnelForNftables(t, h, "xp317-t2", nodeID)

	runBatch := func(forwardID int64) (success, fail int) {
		t.Helper()
		req := newAuthenticatedJSONRequest(t, map[string]interface{}{
			"forwardIds":     []int64{forwardID},
			"targetTunnelId": tunnel2,
		})
		res := httptest.NewRecorder()
		h.forwardBatchChangeTunnel(res, req)
		var payload struct {
			Code int `json:"code"`
			Data struct {
				SuccessCount int `json:"successCount"`
				FailCount    int `json:"failCount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v body=%s", err, res.Body.String())
		}
		return payload.Data.SuccessCount, payload.Data.FailCount
	}
	assertForward := func(forwardID, wantTunnel int64, wantPort int, stage string) {
		t.Helper()
		fwd, err := h.getForwardRecord(forwardID)
		if err != nil {
			t.Fatal(err)
		}
		if fwd.TunnelID != wantTunnel {
			t.Fatalf("%s: tunnel=%d want %d", stage, fwd.TunnelID, wantTunnel)
		}
		ports, err := r.ListForwardPorts(forwardID)
		if err != nil {
			t.Fatal(err)
		}
		if len(ports) != 1 || ports[0].Port != wantPort {
			t.Fatalf("%s: ports=%v want single %d", stage, ports, wantPort)
		}
	}

	// 正常路径：迁移成功，隧道与端口一并切换。
	okFwd, err := r.CreateForwardTx(1, "admin", "xp317-ok", tunnel1, "203.0.113.9:443", "fifo", now, 1, []int64{nodeID}, 32001, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("seed ok forward: %v", err)
	}
	if s, f := runBatch(okFwd); s != 1 || f != 0 {
		t.Fatalf("正常条目应成功: success=%d fail=%d", s, f)
	}
	assertForward(okFwd, tunnel2, 32001, "happy")

	// 注入端口插入失败：该条失败且不迁移、端口不变。
	badFwd, err := r.CreateForwardTx(1, "admin", "xp317-bad", tunnel1, "203.0.113.10:443", "fifo", now, 2, []int64{nodeID}, 32003, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("seed bad forward: %v", err)
	}
	if err := r.DB().Exec(`CREATE TRIGGER xp317_fail_port_insert BEFORE INSERT ON forward_port BEGIN SELECT RAISE(ABORT,'xp317 boom'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if s, f := runBatch(badFwd); s != 0 || f != 1 {
		t.Fatalf("注入失败条目应失败: success=%d fail=%d", s, f)
	}
	assertForward(badFwd, tunnel1, 32003, "injected-failure")
	if err := r.DB().Exec(`DROP TRIGGER xp317_fail_port_insert`).Error; err != nil {
		t.Fatal(err)
	}
}
