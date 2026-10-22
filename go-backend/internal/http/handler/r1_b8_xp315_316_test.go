package handler

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// X-P3-15 (L-04-1): forwardBatchChangeTunnel 校验环补显式 1–65535。
// 存量非法端口 99999 在目标节点 PortRange 空规格（放行）时，修复前
// 被原样写回、批量换隧道报成功；修复后该条应失败且转发不迁移。
func TestR1B8_XP315_BatchChangeTunnelRejectsInvalidStoredPort(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp315.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()

	// PortRange 为空规格的节点（validateLocalNodePort 对空规格放行）。
	if err := r.DB().Exec(`INSERT INTO node(name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES('xp315-node', 's315', '198.51.100.31', '198.51.100.31', '', '', '', 'v1', 0, 0, 0, ?, ?, 0, '[::]', '[::]', 0)`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	nodeID := mustLastInsertID(t, r, "node")
	tunnel1 := seedTunnelForNftables(t, h, "xp315-t1", nodeID)
	tunnel2 := seedTunnelForNftables(t, h, "xp315-t2", nodeID)

	forwardID, err := r.CreateForwardTx(1, "admin", "bad-port-fwd", tunnel1, "203.0.113.9:443", "fifo", now, 1, []int64{nodeID}, 99999, "", nil, 0, 0, nil, 0, 0, 0)
	if err != nil {
		t.Fatalf("seed forward with stored invalid port: %v", err)
	}

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
	if payload.Data.SuccessCount != 0 || payload.Data.FailCount != 1 {
		t.Fatalf("非法端口应整条失败: %+v body=%s", payload.Data, res.Body.String())
	}
	fwd, err := h.getForwardRecord(forwardID)
	if err != nil {
		t.Fatal(err)
	}
	if fwd.TunnelID != tunnel1 {
		t.Fatalf("失败后转发不应迁移: tunnel=%d want %d", fwd.TunnelID, tunnel1)
	}
}

// X-P3-16 (L-04-2, BC-14, V-11): 转发 create/update/batch 入口节点读
// 失败统一 fail-closed。校验 helper 对缺失节点必须报错（修复前两路
// 均 continue 跳过校验落库）。
func TestR1B8_XP316_EntryNodeReadFailureFailsClosed(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/xp316.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	if err := h.validateForwardEntryNodes([]int64{999999}, 30001, 0); err == nil {
		t.Fatalf("入口节点读失败必须 fail-closed 报错，不得跳过校验")
	}
	nodeID := seedRelayTargetNode(t, r, "xp316-node", "198.51.100.32")
	if err := h.validateForwardEntryNodes([]int64{nodeID}, 30001, 0); err != nil {
		t.Fatalf("正常节点+空闲端口应通过: %v", err)
	}
}
