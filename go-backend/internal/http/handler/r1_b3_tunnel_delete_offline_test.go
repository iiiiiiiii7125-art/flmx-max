package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R1 批次 3 · X-P1-1（E-01，BC-01，按 V-02）：删隧道时区分「节点离线/不可达」
// 与「在线真实失败」——离线类删库继续、接口成功（gost 靠重连对账撤监听、
// nft 入 pending-delete 台账）；在线真实失败仍中止。

func seedGostTunnelWithForward(t *testing.T, h *Handler) (int64, *forwardRecord) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := h.repo.CreateNode("gost-node", "secret", "198.51.100.20", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, ""); err != nil {
		t.Fatalf("create node: %v", err)
	}
	node, err := h.repo.GetNodeRecord(1)
	if err != nil || node == nil {
		t.Fatalf("get node: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "gost-tunnel-del", node.ID)
	forward := seedForwardForNftables(t, h, tunnelID, node.ID, "203.0.113.9:8080", 20000)
	return tunnelID, forward
}

func openR1B3Handler(t *testing.T, name string) *Handler {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return New(r, "test-secret")
}

// TestR1B3TunnelDeleteGostNodeOfflineSucceeds: gost 隧道挂转发、入口节点
// agent 未连接（离线）时删隧道应成功且 DB 全清（隧道 + 转发均删除）。
func TestR1B3TunnelDeleteGostNodeOfflineSucceeds(t *testing.T) {
	h := openR1B3Handler(t, "r1b3-gost-offline.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("offline gost tunnel delete must succeed, got code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if tun, _ := h.repo.GetTunnelRecord(tunnelID); tun != nil {
		t.Fatalf("tunnel row remains after offline delete")
	}
	if fr, _ := h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward row remains after offline delete")
	}
}

// TestR1B3TunnelBatchDeleteGostNodeOfflineSucceeds: 批量删隧道同门同改。
func TestR1B3TunnelBatchDeleteGostNodeOfflineSucceeds(t *testing.T) {
	h := openR1B3Handler(t, "r1b3-gost-offline-batch.db")
	tunnelID, forward := seedGostTunnelWithForward(t, h)

	res := httptest.NewRecorder()
	h.tunnelBatchDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{tunnelID}}))
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
	if payload.Code != 0 || payload.Data.SuccessCount != 1 || payload.Data.FailCount != 0 {
		t.Fatalf("offline batch delete must report 1 success 0 fail, got %+v body=%s", payload, res.Body.String())
	}
	if tun, _ := h.repo.GetTunnelRecord(tunnelID); tun != nil {
		t.Fatalf("tunnel row remains after offline batch delete")
	}
	if fr, _ := h.repo.GetForwardRecord(forward.ID); fr != nil {
		t.Fatalf("forward row remains after offline batch delete")
	}
}

// TestR1B3TunnelDeleteNftNodeOfflineGoesPending: nft 隧道入口节点不可达时
// 删隧道应成功、隧道行删除，转发进行既有 pending-delete 台账（转发行为
// pending 墓碑、台账行存在），等待后台重试清理。
func TestR1B3TunnelDeleteNftNodeOfflineGoesPending(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	manager := &fakeNftablesManager{reconcileErr: errFakeSSHDown}
	h.nftablesManager = newSerializedNftManager(manager)

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": forward.TunnelID}))
	if code, msg := decodePendingEnvelopeCode(t, res); code != 0 {
		t.Fatalf("offline nft tunnel delete must succeed, got code=%d msg=%q body=%s", code, msg, res.Body.String())
	}
	if tun, _ := h.repo.GetTunnelRecord(forward.TunnelID); tun != nil {
		t.Fatalf("tunnel row remains after offline delete")
	}
	fr, err := h.repo.GetForwardRecord(forward.ID)
	if err != nil || fr == nil {
		t.Fatalf("pending forward tombstone missing: %+v err=%v", fr, err)
	}
	if fr.Status != model.ForwardStatusPendingDelete {
		t.Fatalf("expected forward status pending-delete (%d), got %d", model.ForwardStatusPendingDelete, fr.Status)
	}
	pending, err := h.repo.GetNftPendingDelete(forward.ID)
	if err != nil || pending == nil {
		t.Fatalf("expected pending-delete ledger row, got %+v err=%v", pending, err)
	}
	if pending.NodeID != fixture.nodeID {
		t.Fatalf("expected ledger node %d, got %d", fixture.nodeID, pending.NodeID)
	}
}

// TestR1B3TunnelDeleteOnlineRealFailureAborts: 节点在线但数据面清理真实
// 失败（远程节点明确返回失败）时删隧道仍应中止，隧道与转发均保留。
func TestR1B3TunnelDeleteOnlineRealFailureAborts(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"type":    "DeleteService",
				"success": false,
				"message": "simulated real failure: permission denied",
			},
		})
	}))
	t.Cleanup(remote.Close)

	h := openR1B3Handler(t, "r1b3-online-fail.db")
	now := time.Now().UnixMilli()
	if _, err := h.repo.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatalf("create user: %v", err)
	}
	remoteNode := &model.Node{
		Name: "remote-entry", Secret: "remote-secret", ServerIP: "198.51.100.30", Port: "1000-65535",
		Status: 1, IsRemote: 1,
		RemoteURL:   sql.NullString{String: remote.URL, Valid: true},
		RemoteToken: sql.NullString{String: "token", Valid: true},
	}
	if err := h.repo.DB().Create(remoteNode).Error; err != nil {
		t.Fatalf("create remote node: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "remote-tunnel-del", remoteNode.ID)
	forward := seedForwardForNftables(t, h, tunnelID, remoteNode.ID, "203.0.113.9:8080", 20000)

	res := httptest.NewRecorder()
	h.tunnelDelete(res, newAuthenticatedJSONRequest(t, map[string]any{"id": tunnelID}))
	if code, _ := decodePendingEnvelopeCode(t, res); code == 0 {
		t.Fatalf("online real failure must abort tunnel delete, got success body=%s", res.Body.String())
	}
	if tun, _ := h.repo.GetTunnelRecord(tunnelID); tun == nil {
		t.Fatalf("tunnel row deleted despite online cleanup failure")
	}
	if fr, _ := h.repo.GetForwardRecord(forward.ID); fr == nil {
		t.Fatalf("forward row deleted despite online cleanup failure")
	}
}
