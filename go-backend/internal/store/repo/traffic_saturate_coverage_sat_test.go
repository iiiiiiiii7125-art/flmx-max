package repo

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"go-backend/internal/store/model"
)

// SAT（T2 返修附带发现，B08-001 同族补修）：C06 的饱和守卫只覆盖了
// flow/upload 三面 + AddFlow + 配额用量，全仓另有三处流量字节累加器
// 仍用裸 `col + ?`——AddPeerShareCurrentFlow、入站计数样本落账
// （NodeInbound upload/download_bytes）、订阅计数样本落账
// （Subscription upload/download_bytes 及其小时桶的 OnConflict 累加）。
// 裸加在 SQLite 上溢出回绕/宽化为 REAL 落库、在 PG 上 int8 溢出报错
// 回滚整批。本文件在 SQLite 真库上逐面断言饱和语义。

func TestAddPeerShareCurrentFlowSaturates_SAT(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat-peer-share.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	share := model.PeerShare{
		Name: "sat-share", NodeID: 1, Token: "sat-token",
		CurrentFlow: math.MaxInt64 - 10,
		CreatedTime: 1700000000000, UpdatedTime: 1700000000000,
	}
	if err := r.DB().Create(&share).Error; err != nil {
		t.Fatalf("create peer share: %v", err)
	}
	if err := r.AddPeerShareCurrentFlow(share.ID, 1000); err != nil {
		t.Fatalf("add current flow: %v", err)
	}
	var got model.PeerShare
	if err := r.DB().First(&got, share.ID).Error; err != nil {
		t.Fatalf("reload peer share: %v", err)
	}
	if got.CurrentFlow != math.MaxInt64 {
		t.Fatalf("current_flow = %d, want saturation at MaxInt64 (%d)", got.CurrentFlow, int64(math.MaxInt64))
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(current_flow) FROM peer_share WHERE id = %d", share.ID)); typ != "integer" {
		t.Fatalf("typeof(current_flow) = %s, want integer (overflow widened the stored value)", typ)
	}
	// 既有跳过语义保持：delta<=0 不动计数。
	if err := r.AddPeerShareCurrentFlow(share.ID, 0); err != nil {
		t.Fatalf("add zero delta: %v", err)
	}
	if err := r.DB().First(&got, share.ID).Error; err != nil {
		t.Fatalf("reload peer share after zero delta: %v", err)
	}
	if got.CurrentFlow != math.MaxInt64 {
		t.Fatalf("current_flow after zero delta = %d, want unchanged MaxInt64", got.CurrentFlow)
	}
}

func TestApplyInboundCounterSamplesSaturates_SAT(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat-inbound.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	inbound := model.NodeInbound{
		Name: "sat-inbound", Protocol: "vless", Port: 443,
		UploadBytes: math.MaxInt64 - 10, DownloadBytes: math.MaxInt64 - 10,
	}
	if err := r.DB().Create(&inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	// 第一批：只建基线（in 100 / out 200），增量为 0。
	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 100},
		{InboundID: inbound.ID, Protocol: "vless", Direction: "out", Epoch: "e1", Bytes: 200},
	}, 1700000000000); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// 第二批：增量 1000/1000，余额 MaxInt64-10 吸收后必须饱和而非回绕。
	if err := r.ApplyInboundCounterSamples([]InboundCounterSample{
		{InboundID: inbound.ID, Protocol: "vless", Direction: "in", Epoch: "e1", Bytes: 1100},
		{InboundID: inbound.ID, Protocol: "vless", Direction: "out", Epoch: "e1", Bytes: 1200},
	}, 1700000060000); err != nil {
		t.Fatalf("apply saturating batch: %v", err)
	}
	var got model.NodeInbound
	if err := r.DB().First(&got, inbound.ID).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	if got.UploadBytes != math.MaxInt64 || got.DownloadBytes != math.MaxInt64 {
		t.Fatalf("inbound bytes = (%d, %d), want both saturated at MaxInt64", got.UploadBytes, got.DownloadBytes)
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(upload_bytes) FROM node_inbound WHERE id = %d", inbound.ID)); typ != "integer" {
		t.Fatalf("typeof(upload_bytes) = %s, want integer", typ)
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(download_bytes) FROM node_inbound WHERE id = %d", inbound.ID)); typ != "integer" {
		t.Fatalf("typeof(download_bytes) = %s, want integer", typ)
	}
}

func TestApplySubscriptionCounterSamplesSaturates_SAT(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "sat-subscription.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	sub := model.Subscription{
		Name: "sat-sub", Token: "sat-token", Kind: "share", Enabled: 1,
		UploadBytes: math.MaxInt64 - 10, DownloadBytes: math.MaxInt64 - 10,
	}
	if err := r.DB().Create(&sub).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	const hourMs = int64(3600000)
	now1 := int64(1700000000000)
	hour1 := now1 - now1%hourMs

	// 第一批：基线 up 5 / down 5（只建基线、不计增量、无桶行）。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 5},
		{SubscriptionID: sub.ID, Direction: "down", Bytes: 5},
	}, now1); err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	// 第二批（同一小时）：样本推到 MaxInt64，增量 MaxInt64-5——订阅
	// 累计（余额 MaxInt64-10）必须饱和；小时桶新建行记 MaxInt64-5。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: math.MaxInt64},
		{SubscriptionID: sub.ID, Direction: "down", Bytes: math.MaxInt64},
	}, now1+1000); err != nil {
		t.Fatalf("apply saturating batch: %v", err)
	}
	// 第三批（同一小时、同 epoch 计数回退）：增量 = 当前样本值 100。
	// 订阅累计已在 MaxInt64，必须保持；小时桶冲突累加
	// (MaxInt64-5) + 100 必须饱和到 MaxInt64，不得回绕为负。
	if err := r.ApplySubscriptionCounterSamples(7, "e1", []SubscriptionCounterSample{
		{SubscriptionID: sub.ID, Direction: "up", Bytes: 100},
	}, now1+2000); err != nil {
		t.Fatalf("apply rollback batch: %v", err)
	}

	var got model.Subscription
	if err := r.DB().First(&got, sub.ID).Error; err != nil {
		t.Fatalf("reload subscription: %v", err)
	}
	if got.UploadBytes != math.MaxInt64 || got.DownloadBytes != math.MaxInt64 {
		t.Fatalf("subscription bytes = (%d, %d), want both saturated at MaxInt64", got.UploadBytes, got.DownloadBytes)
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(upload_bytes) FROM subscription WHERE id = %d", sub.ID)); typ != "integer" {
		t.Fatalf("typeof(subscription.upload_bytes) = %s, want integer", typ)
	}

	rows, err := r.ListSubscriptionTraffic(sub.ID, 0)
	if err != nil {
		t.Fatalf("list traffic: %v", err)
	}
	if len(rows) != 1 || rows[0].Hour != hour1 {
		t.Fatalf("want single bucket at hour %d, got %+v", hour1, rows)
	}
	if rows[0].UploadBytes != math.MaxInt64 {
		t.Fatalf("bucket upload_bytes = %d, want saturation at MaxInt64 (OnConflict add wrapped)", rows[0].UploadBytes)
	}
	if rows[0].DownloadBytes != math.MaxInt64-5 {
		t.Fatalf("bucket download_bytes = %d, want %d (single delta, no second add)", rows[0].DownloadBytes, int64(math.MaxInt64-5))
	}
	if typ := mustColumnType(t, r, fmt.Sprintf("SELECT typeof(upload_bytes) FROM subscription_traffic WHERE subscription_id = %d", sub.ID)); typ != "integer" {
		t.Fatalf("typeof(subscription_traffic.upload_bytes) = %s, want integer", typ)
	}
}

// TestSaturatingExcludedAddExprShape_SAT：小时桶饱和表达式的形态
// 锁定——与 saturatingCounterIncrExpr 同一 CASE 形态、无占位符
// （增量来自 excluded 行），边界为 int64 字面量（PG 上两侧均有
// 类型，不重蹈 C06PG 的 42725 无类型参数覆辙）。
func TestSaturatingExcludedAddExprShape_SAT(t *testing.T) {
	expr := saturatingExcludedAddExpr("subscription_traffic", "upload_bytes")
	want := "CASE WHEN excluded.upload_bytes > 0 AND subscription_traffic.upload_bytes > 9223372036854775807 - excluded.upload_bytes THEN 9223372036854775807 " +
		"WHEN excluded.upload_bytes < 0 AND subscription_traffic.upload_bytes < (-9223372036854775807 - 1) - excluded.upload_bytes THEN (-9223372036854775807 - 1) " +
		"ELSE subscription_traffic.upload_bytes + excluded.upload_bytes END"
	if expr.SQL != want {
		t.Fatalf("expr SQL =\n%s\nwant:\n%s", expr.SQL, want)
	}
	if len(expr.Vars) != 0 {
		t.Fatalf("expr Vars = %v, want none (delta must come from the excluded row)", expr.Vars)
	}
}
