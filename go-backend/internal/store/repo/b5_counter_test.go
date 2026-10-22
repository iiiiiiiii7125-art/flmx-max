package repo

import (
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// ─── B5 计费/流量批（audit FINAL §5-B5）───

// TestApplySubscriptionCounterSamplesDuplicateInBatchF075: 同一批次内
// 同 (subscription, direction) 出现两条递增样本时，增量只能结算一次
// （末值−旧基线）。修复前 prev 不随批次推进，两条样本各按同一旧
// 基线算 delta，总增量被重复计入。
func TestApplySubscriptionCounterSamplesDuplicateInBatchF075(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "b5-sub-counter.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{Name: "b5-sub", Token: "b5-token", Kind: "managed", Enabled: 1}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	// 第一批：建立基线 100（epoch e1）。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 100},
	}, 1000); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// 第二批：同键重复样本 150、260 —— 正确增量只有 260−100=160。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 150},
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 260},
	}, 2000); err != nil {
		t.Fatalf("apply duplicate batch: %v", err)
	}
	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	// FIX132-C（A3-04）语义订正：基线批只建基线、不计增量（旧断言
	// 把基线批 100 也算进累计，编码的正是基线缺失全量计的缺陷语义）。
	// 总上传 = 第二批增量 160。
	if got.UploadBytes != 160 {
		t.Fatalf("F-075: upload_bytes = %d, want 160 (duplicate sample in one batch double-counted)", got.UploadBytes)
	}
}

// TestApplyInboundCounterSamplesDuplicateInBatchF075: 入站侧同模式。
func TestApplyInboundCounterSamplesDuplicateInBatchF075(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "b5-inbound-counter.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inbound := model.NodeInbound{Name: "b5-inbound", Protocol: "vless", Port: 443}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 500},
	}, 1000); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 700},
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 900},
	}, 2000); err != nil {
		t.Fatalf("apply duplicate batch: %v", err)
	}
	var got model.NodeInbound
	if err := r.DB().First(&got, inbound.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	// R2X-033 语义订正（断言更新登记 #1）：基线批只建基线、不计
	// 增量（旧断言把基线批 500 也算进累计，编码的正是无基线全量
	// 计的缺陷语义，与订阅管线 FIX132-C 的订正同型）。
	// 总上传 = 第二批增量 (900−500) = 400。
	if got.UploadBytes != 400 {
		t.Fatalf("F-075: inbound upload_bytes = %d, want 400 (duplicate sample in one batch double-counted)", got.UploadBytes)
	}
}

// TestDeleteUserCascadeClearsNftCounterStatesF076: 删除用户必须一并
// 清理其转发的 nft_counter_state 基线行。修复前 DeleteUserCascade
// 只删 forward_port 与 forward 本体，计数状态永久残留（孤儿行）。
// （对照：单删/隧删/节点删路径均已内联清理，唯独用户级联漏。）
func TestDeleteUserCascadeClearsNftCounterStatesF076(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "b5-user-cascade.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := int64(1700000000000)
	userID, err := r.CreateUser("b5-deluser", "hash", 1, now+86400000, 1, 1, 100, 1, 0, now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, in_flow, out_flow, created_time, updated_time, status, inx)
		VALUES(61, ?, 'b5-deluser', 'b5-fwd', 1, '203.0.113.1:443', 'fifo', 0, 0, ?, ?, 1, 0)`, userID, now, now).Error; err != nil {
		t.Fatalf("insert forward: %v", err)
	}
	if err := r.UpsertNftCounterStates([]NftCounterStateInput{{
		NodeID: 11, ForwardID: 61, Protocol: "tcp", Direction: "to-target",
		RuleHash: "hash-b5", Bytes: 1234, Packets: 12, CollectedTime: now,
	}}, now); err != nil {
		t.Fatalf("seed counter state: %v", err)
	}
	states, err := r.GetNftCounterStatesByNode(11)
	if err != nil || len(states) != 1 {
		t.Fatalf("seed check: states=%v err=%v", states, err)
	}

	if err := r.DeleteUserCascade(userID); err != nil {
		t.Fatalf("delete user cascade: %v", err)
	}
	states, err = r.GetNftCounterStatesByNode(11)
	if err != nil {
		t.Fatalf("get states after delete: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("F-076: %d orphan nft_counter_state rows remain after user deletion", len(states))
	}
}
