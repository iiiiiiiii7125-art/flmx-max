package handler

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// B-NEW 回归：轮询摄入中途、用户相触发的 KernelApply（超限切断）
// 换代 epoch，旧 epoch 的入站样本不得丢失；换代后首个新 epoch 样
// 本也不得再丢（无 flip-flop 回响）。
//
// 机制（旧序）：ingestKernelCountersMode 先算用户计数（含
// syncSubscriptions，可触发 KernelApply→重建→新 epoch→settle 重
// 定基线），后算入站计数——同一 status 的陈旧入站样本撞上新 epoch
// 基线即 delta 0（B 路实测 7.3MB 全丢）；且陈旧入站记账会把基线
// 写回旧 epoch，使下一轮首个新 epoch 样本也 delta 0（原站 2MB
// 丢增量）。修复后两相分隔：全部样本先对旧基线结算（Phase A），
// 副作用（sync/执法尾）后行（Phase B）。
func TestInboundCountersSurviveMidPollKernelRebuild(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/bnew-meter.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	if _, err := r.CreateUser("admin", "hash", 0, now+86400000, 1, 1, 100, 1, 0, now); err != nil {
		t.Fatal(err)
	}

	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.31")
	k := seedChainKernel(t, r, nodeID, kernel.TypeXray)
	// 具体版本避开 apply 路径的 "latest" 解析（测试环境无外网）。
	if err := r.UpdateNodeKernelFields(k.ID, map[string]interface{}{"version": "v25.1.1"}); err != nil {
		t.Fatal(err)
	}
	seedChainInbound(t, r, k, "pre-existing", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 8443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)

	mock := &inboundMeterMock{}
	srv := httptest.NewServer(h.WebSocketHandler())
	defer srv.Close()
	stop := startInboundMeterMockNode(t, srv.URL, "entry-secret", mock)
	defer stop()
	waitForCondition(t, 5*time.Second, func() bool { return h.wsServer.IsNodeOnline(nodeID) }, "mock node online")
	// 等重连对账轮下发完成且命令计数稳定（与 sibling 测试同口径）。
	{
		lastApply, lastStatus, stable := -1, -1, 0
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			a, s := mock.calls()
			if a == lastApply && s == lastStatus && a >= 1 {
				stable++
				if stable >= 3 {
					break
				}
			} else {
				stable = 0
				lastApply, lastStatus = a, s
			}
			time.Sleep(100 * time.Millisecond)
		}
		if lastApply < 1 {
			t.Fatal("reconnect round never applied the kernel")
		}
	}

	// 被测订阅：分享订阅，限额 1000B——一轮 7.3MB 入账即超限，
	// 本轮询的 syncSubscriptions 必切断并下发（带 settle 的独立
	// 下发，epoch 换代 + 计数清零）。
	inbound2 := seedChainInbound(t, r, k, "late-inbound", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 9443,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	sub := &model.Subscription{Name: "bnew-sub", Kind: model.SubscriptionShare, Token: strings.Repeat("e", 32),
		UUID: "u-bnew", Password: "bnew-pw", Enabled: 1, SourceAll: 1, TrafficLimit: 1000, CreatedTime: now, UpdatedTime: now}
	if err := r.SaveSubscription(sub); err != nil {
		t.Fatal(err)
	}
	if err := r.DB().Exec(`UPDATE subscription SET applied_active = 1 WHERE id = ?`, sub.ID).Error; err != nil {
		t.Fatal(err)
	}
	mock.mu.Lock()
	mock.inboundID = inbound2.ID
	mock.userID = sub.ID
	mock.mu.Unlock()

	// 独立下发钉基线（0/0，epoch e1），与 sibling 测试同口径。
	if err := h.applyKernel(k.ID, false); err != nil {
		t.Fatalf("standalone apply: %v", err)
	}
	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM node_inbound_counter WHERE inbound_id = ? AND bytes = 0`, inbound2.ID); n != 2 {
		t.Fatalf("inbound baseline after apply: want 2 rows pinned at 0 (tcp in/out), got %d", n)
	}
	if n := mustHandlerCount(t, h, `SELECT COUNT(*) FROM subscription_counter WHERE subscription_id = ? AND bytes = 0`, sub.ID); n != 2 {
		t.Fatalf("subscription baseline after apply: want 2 rows pinned at 0 (up/down), got %d", n)
	}

	// B 路实测数：7.3MB 下载（订阅行 +7,376,031）；入站行上下行
	// 须同步入账，不得丢。
	mock.setTraffic(4000, 7376031, 3000, 7376031)
	// 武装换代：下一发 KernelApply（即本轮询 sync 切断触发的那发）
	// 翻 epoch e1→e2 并清零计数。
	mock.mu.Lock()
	mock.nextEpoch = "e2"
	mock.mu.Unlock()
	applyBefore, _ := mock.calls()

	// 周期轮询到达：用户相入账超限→切断下发→换代→settle，入站行
	// 必须仍拿到重建前流量的全额增量。
	h.refreshNodeKernelStatus(nodeID)

	// 触发证明：本轮询内确实发生过一次下发（否则测试是空转）。
	applyAfter, _ := mock.calls()
	if applyAfter <= applyBefore {
		t.Fatalf("no KernelApply happened during the poll (apply %d -> %d); cutoff never triggered, test is vacuous", applyBefore, applyAfter)
	}
	mock.mu.Lock()
	epochNow := mock.epoch
	mock.mu.Unlock()
	if epochNow != "e2" {
		t.Fatalf("mock epoch after poll = %q, want e2 (rebuild never landed)", epochNow)
	}
	row, err := r.GetNodeInbound(inbound2.ID)
	if err != nil || row == nil {
		t.Fatalf("load inbound2: %v", err)
	}
	if row.UploadBytes != 4000 || row.DownloadBytes != 7376031 {
		t.Fatalf("B-NEW: inbound totals after mid-poll rebuild = %d/%d, want 4000/7376031 (pre-rebuild traffic lost)", row.UploadBytes, row.DownloadBytes)
	}
	subRow, err := r.GetSubscription(sub.ID)
	if err != nil || subRow == nil {
		t.Fatalf("load subscription: %v", err)
	}
	if subRow.UploadBytes != 3000 || subRow.DownloadBytes != 7376031 {
		t.Fatalf("subscription totals after mid-poll rebuild = %d/%d, want 3000/7376031", subRow.UploadBytes, subRow.DownloadBytes)
	}
	if subRow.AppliedActive != 0 {
		t.Fatalf("subscription applied_active = %d, want 0 (cutoff never landed)", subRow.AppliedActive)
	}

	// 换代后首个新 epoch 样本：不得再丢（flip-flop 回响面）。
	mock.setTraffic(100, 200, 50, 60)
	h.refreshNodeKernelStatus(nodeID)
	row, _ = r.GetNodeInbound(inbound2.ID)
	if row.UploadBytes != 4100 || row.DownloadBytes != 7376231 {
		t.Fatalf("B-NEW echo: inbound totals after post-rebuild poll = %d/%d, want 4100/7376231", row.UploadBytes, row.DownloadBytes)
	}
	subRow, _ = r.GetSubscription(sub.ID)
	if subRow.UploadBytes != 3050 || subRow.DownloadBytes != 7376091 {
		t.Fatalf("subscription totals after post-rebuild poll = %d/%d, want 3050/7376091", subRow.UploadBytes, subRow.DownloadBytes)
	}
}
