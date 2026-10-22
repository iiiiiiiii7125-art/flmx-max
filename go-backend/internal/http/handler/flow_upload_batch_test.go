package handler

import (
	"path/filepath"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func TestBuildFlowUploadBatchAggregatesForwardQuotaPeerShareAndCleanupTargets(t *testing.T) {
	h := &Handler{}
	metas := map[int64]repo.FlowUploadForwardMeta{
		20: {
			ForwardID:    20,
			UserID:       2,
			UserTunnelID: 10,
			TunnelID:     1,
			TrafficRatio: 2,
			TunnelFlow:   3,
		},
	}

	batch := h.buildFlowUploadBatch([]flowItem{
		{N: "20_2_10", U: 70, D: 50},
		{N: "20_2_10_tcp", U: 40, D: 30},
		{N: "99_2_10", U: 12, D: 8},
		{N: "fed_svc_17", U: 9, D: 1},
	}, metas)

	if len(batch.flowDeltas) != 1 {
		t.Fatalf("expected 1 flow delta, got %d", len(batch.flowDeltas))
	}
	delta := batch.flowDeltas[0]
	if delta.ForwardID != 20 || delta.UserID != 2 || delta.UserTunnelID != 10 {
		t.Fatalf("unexpected flow delta identity: %#v", delta)
	}
	// N-01（方向订正）：线格式 U=远端→客户端（出）、D=客户端→远端
	// （入），见 flow_upload_batch.go 的钉死注释。合并后 U=110（出）、
	// D=80（入），ratio=2、Flow=3（≠1 双向）→ InFlow=80*2=160、
	// OutFlow=110*2=220、quota=380（总额与旧断言一致，仅方向归属
	// 对调）。旧断言 in=220/out=160 是 O6-002 按「U=入」字面假设交换
	// 后的值，在本修复下不成立——这正是区分修复前/后的断言。
	if delta.InFlow != 160 || delta.OutFlow != 220 {
		t.Fatalf("expected scaled flow in=160 out=220, got in=%d out=%d", delta.InFlow, delta.OutFlow)
	}
	if batch.quotaUsage[2] != 380 {
		t.Fatalf("expected quota usage 380, got %d", batch.quotaUsage[2])
	}
	if len(batch.policyTargets) != 1 {
		t.Fatalf("expected 1 policy target, got %d", len(batch.policyTargets))
	}
	if batch.policyTargets[0].UserID != 2 || batch.policyTargets[0].UserTunnelID != 10 {
		t.Fatalf("unexpected policy target: %#v", batch.policyTargets[0])
	}
	traffic := batch.forwardTraffic[20]
	if traffic.bytesIn != 80 || traffic.bytesOut != 110 {
		t.Fatalf("expected raw traffic in=80 out=110, got in=%d out=%d", traffic.bytesIn, traffic.bytesOut)
	}
	if _, ok := batch.orphanServices["99_2_10"]; !ok {
		t.Fatalf("expected orphan service cleanup target for 99_2_10")
	}
	if item, ok := batch.peerShareForwardItems["99_2_10"]; !ok || item.U != 12 || item.D != 8 {
		t.Fatalf("expected orphan forward to remain eligible for peer-share accounting, got %#v ok=%v", item, ok)
	}
	if item, ok := batch.peerShareForwardItems["20_2_10"]; !ok || item.U != 110 || item.D != 80 {
		t.Fatalf("expected merged peer-share forward item, got %#v ok=%v", item, ok)
	}
	if item, ok := batch.peerShareRuntimeItems[17]; !ok || item.U != 9 || item.D != 1 {
		t.Fatalf("expected merged peer-share runtime item, got %#v ok=%v", item, ok)
	}
}

// X-P3-31 / V-08 断言更新登记：原测试名
// TestApplyFlowUploadBatchContinuesPolicyAndPeerShareSideEffectsWhenQuotaBatchFails，
// 锁旧取舍「quota 段失败时总量照落、策略与 peer-share 副作用继续」。
// V-08 裁定改取舍为单事务原子入账：quota 失败整批回滚、无任何副作用，
// 故更名并反转断言（非放宽：失败面从「部分落账」收紧为「整批不落」）。
func TestApplyFlowUploadBatchRollsBackAllWhenQuotaFails(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "flow-upload-batch-quota-fail.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "flow-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "flow-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	if err := r.CreatePeerShare(&repo.PeerShare{Name: "share", NodeID: 1, Token: "token", MaxBandwidth: 0, CurrentFlow: 0, PortRangeStart: 31000, PortRangeEnd: 31010, IsActive: 1, CreatedTime: nowMs, UpdatedTime: nowMs}); err != nil {
		t.Fatalf("create peer share: %v", err)
	}
	share, err := r.GetPeerShareByToken("token")
	if err != nil || share == nil {
		t.Fatalf("load peer share: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO peer_share_runtime(share_id, node_id, reservation_id, resource_key, binding_id, role, chain_name, service_name, protocol, strategy, port, target, applied, status, created_time, updated_time)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, share.ID, 1, "svc-r1", "svc-rk1", "", "forward", "", "20_2_10", "tcp", "fifo", 31001, "", 1, 1, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert peer share runtime: %v", err)
	}
	if err := r.DB().Exec(`
		CREATE TRIGGER fail_user_quota_insert
		BEFORE INSERT ON user_quota
		BEGIN
			SELECT RAISE(FAIL, 'quota insert blocked for test');
		END;
	`).Error; err != nil {
		t.Fatalf("create quota failure trigger: %v", err)
	}

	h := &Handler{repo: r}
	applyErr := h.applyFlowUploadBatch(1, flowUploadBatch{
		flowDeltas:            []repo.FlowUploadCounterDelta{{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 80, OutFlow: 120}},
		quotaUsage:            map[int64]int64{2: 200},
		policyTargets:         []flowPolicyTarget{{UserID: 2, UserTunnelID: 10}},
		peerShareForwardItems: map[string]flowItem{"20_2_10": {N: "20_2_10", U: 120, D: 80}},
	}, now)
	if applyErr == nil {
		t.Fatalf("expected accounting error to be returned when quota write fails")
	}

	if got := mustQueryInt(t, r, `SELECT status FROM forward WHERE id = 20`); got != 1 {
		t.Fatalf("expected no policy enforcement on unaccounted batch, forward status=%d", got)
	}
	if got := mustQueryInt(t, r, `SELECT in_flow FROM forward WHERE id = 20`); got != 0 {
		t.Fatalf("expected flow deltas to roll back with quota failure, got in_flow=%d", got)
	}
	updatedShare, err := r.GetPeerShare(share.ID)
	if err != nil || updatedShare == nil {
		t.Fatalf("reload peer share: %v", err)
	}
	if updatedShare.CurrentFlow != 0 {
		t.Fatalf("expected no peer-share accounting on unaccounted batch, got %d", updatedShare.CurrentFlow)
	}
}

// X-P3-31 / V-08 断言更新登记：原测试名
// TestApplyFlowUploadBatchContinuesPeerShareSideEffectsWhenFlowBatchFails，
// 锁旧取舍「总量批失败时逐条 fallback 落成功条、quota 照加、副作用继续」。
// V-08 裁定改取舍为单事务原子入账：任一 delta 失败整批回滚，故更名
// 并反转断言（非放宽：失败面从「部分落账」收紧为「整批不落」）。
func TestApplyFlowUploadBatchRollsBackAllWhenFlowDeltaFails(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "flow-upload-batch-flow-fail.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now()
	nowMs := now.UnixMilli()
	if err := r.DB().Create(&model.User{ID: 2, User: "flow-user", Pwd: "pwd", RoleID: 1, ExpTime: 2727251700000, Flow: 99999, Num: 99999, CreatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.DB().Create(&model.Tunnel{ID: 1, Name: "tunnel-1", TrafficRatio: 1, Type: 1, Protocol: "tls", Flow: 1, CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed tunnel: %v", err)
	}
	if err := r.DB().Create(&model.UserTunnel{ID: 10, UserID: 2, TunnelID: 1, Num: 99999, Flow: 0, ExpTime: 2727251700000, Status: 1}).Error; err != nil {
		t.Fatalf("seed user tunnel: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 20, UserID: 2, UserName: "flow-user", Name: "forward-20", TunnelID: 1, RemoteAddr: "1.1.1.1:80", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed forward: %v", err)
	}
	if err := r.DB().Create(&model.Forward{ID: 21, UserID: 2, UserName: "flow-user", Name: "forward-21", TunnelID: 1, RemoteAddr: "1.1.1.1:81", Strategy: "fifo", CreatedTime: nowMs, UpdatedTime: nowMs, Status: 1}).Error; err != nil {
		t.Fatalf("seed second forward: %v", err)
	}
	if err := r.CreatePeerShare(&repo.PeerShare{Name: "share", NodeID: 1, Token: "token", MaxBandwidth: 0, CurrentFlow: 0, PortRangeStart: 31000, PortRangeEnd: 31010, IsActive: 1, CreatedTime: nowMs, UpdatedTime: nowMs}); err != nil {
		t.Fatalf("create peer share: %v", err)
	}
	share, err := r.GetPeerShareByToken("token")
	if err != nil || share == nil {
		t.Fatalf("load peer share: %v", err)
	}
	if err := r.DB().Exec(`
		INSERT INTO peer_share_runtime(share_id, node_id, reservation_id, resource_key, binding_id, role, chain_name, service_name, protocol, strategy, port, target, applied, status, created_time, updated_time)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, share.ID, 1, "svc-r1", "svc-rk1", "", "forward", "", "20_2_10", "tcp", "fifo", 31001, "", 1, 1, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert peer share runtime: %v", err)
	}
	if err := r.DB().Exec(`
		CREATE TRIGGER fail_forward_flow_update
		BEFORE UPDATE ON forward
		WHEN NEW.id = 21 AND (NEW.in_flow != OLD.in_flow OR NEW.out_flow != OLD.out_flow)
		BEGIN
			SELECT RAISE(FAIL, 'forward flow update blocked for test');
		END;
	`).Error; err != nil {
		t.Fatalf("create flow failure trigger: %v", err)
	}

	h := &Handler{repo: r}
	applyErr := h.applyFlowUploadBatch(1, flowUploadBatch{
		flowDeltas: []repo.FlowUploadCounterDelta{
			{ForwardID: 20, UserID: 2, UserTunnelID: 10, InFlow: 80, OutFlow: 120},
			{ForwardID: 21, UserID: 2, UserTunnelID: 10, InFlow: 30, OutFlow: 40},
		},
		quotaUsage:            map[int64]int64{2: 200},
		policyTargets:         []flowPolicyTarget{{UserID: 2, UserTunnelID: 10}},
		peerShareForwardItems: map[string]flowItem{"20_2_10": {N: "20_2_10", U: 120, D: 80}},
	}, now)
	if applyErr == nil {
		t.Fatalf("expected accounting error to be returned when a flow delta fails")
	}

	if got := mustQueryInt(t, r, `SELECT status FROM forward WHERE id = 20`); got != 1 {
		t.Fatalf("expected no policy enforcement on unaccounted batch, forward status=%d", got)
	}
	updatedShare, err := r.GetPeerShare(share.ID)
	if err != nil || updatedShare == nil {
		t.Fatalf("reload peer share: %v", err)
	}
	if updatedShare.CurrentFlow != 0 {
		t.Fatalf("expected no peer-share accounting on unaccounted batch, got %d", updatedShare.CurrentFlow)
	}
	if got := mustQueryInt(t, r, `SELECT in_flow FROM forward WHERE id = 20`); got != 0 {
		t.Fatalf("expected forward 20 delta to roll back with the batch, got %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT in_flow FROM forward WHERE id = 21`); got != 0 {
		t.Fatalf("expected failed forward 21 delta to remain unapplied, got %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT in_flow FROM user WHERE id = 2`); got != 0 {
		t.Fatalf("expected user totals to roll back with the batch, got %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT in_flow FROM user_tunnel WHERE id = 10`); got != 0 {
		t.Fatalf("expected user_tunnel totals to roll back with the batch, got %d", got)
	}
}

// X-P3-31 / V-08 断言更新登记：原测试名
// TestApplyFlowUploadBatchFallsBackToPerUserQuotaUpdates，锁旧取舍
// 「quota 批失败时逐用户 fallback、成功用户照落」。V-08 裁定改取舍
// 为单事务原子入账：一人失败整批回滚，故更名并反转断言（非放宽：
// 失败面从「部分落账」收紧为「整批不落」）。
func TestApplyFlowUploadBatchQuotaAtomicAcrossUsers(t *testing.T) {
	r, err := repo.Open(filepath.Join(t.TempDir(), "flow-upload-batch-quota-fallback.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	defer r.Close()

	now := time.Now()
	nowMs := now.UnixMilli()
	dayKey := int64(now.Year()*10000 + int(now.Month())*100 + now.Day())
	monthKey := int64(now.Year()*100 + int(now.Month()))
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(2, 'u2', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user 2: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(3, 'u3', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user 3: %v", err)
	}
	if err := r.DB().Exec(`INSERT INTO user_quota(user_id, daily_limit_gb, monthly_limit_gb, daily_used_bytes, monthly_used_bytes, day_key, month_key, disabled_by_quota, disabled_at, paused_forward_ids, created_time, updated_time) VALUES(2, 0, 0, 0, 0, ?, ?, 0, 0, '', ?, ?), (3, 0, 0, 0, 0, ?, ?, 0, 0, '', ?, ?)`, dayKey, monthKey, nowMs, nowMs, dayKey, monthKey, nowMs, nowMs).Error; err != nil {
		t.Fatalf("insert user quotas: %v", err)
	}
	if err := r.DB().Exec(`
		CREATE TRIGGER fail_user_3_quota_update
		BEFORE UPDATE ON user_quota
		WHEN NEW.user_id = 3 AND (NEW.daily_used_bytes != OLD.daily_used_bytes OR NEW.monthly_used_bytes != OLD.monthly_used_bytes)
		BEGIN
			SELECT RAISE(FAIL, 'quota update blocked for user 3');
		END;
	`).Error; err != nil {
		t.Fatalf("create quota fallback trigger: %v", err)
	}

	h := &Handler{repo: r}
	applyErr := h.applyFlowUploadBatch(1, flowUploadBatch{quotaUsage: map[int64]int64{2: 200, 3: 300}}, now)
	if applyErr == nil {
		t.Fatalf("expected accounting error to be returned when one user's quota write fails")
	}

	if got := mustQueryInt(t, r, `SELECT daily_used_bytes FROM user_quota WHERE user_id = 2`); got != 0 {
		t.Fatalf("expected user 2 quota to roll back with the batch, got %d", got)
	}
	if got := mustQueryInt(t, r, `SELECT daily_used_bytes FROM user_quota WHERE user_id = 3`); got != 0 {
		t.Fatalf("expected failed user 3 quota delta to remain unapplied, got %d", got)
	}
}

// TestFlowDirectionParityAcrossEngines 是 N-01 的回归测试：同一笔物理
// 流量（入=客户端→远端 inBytes、出=远端→客户端 outBytes）分别经
// gost 上报路径（线格式 U=出、D=入）与 nftables/realm 路径
// （BytesIn=入、BytesOut=出）入账时，flowDeltas、quotaUsage 与
// 隧道指标原料必须逐项一致。
// 修复前（O6-002 字面交换）gost 路径把入/出对调：场景 A 下 InFlow
// 会得到 12,583,527、quota 同值，与 nft 路径的 267 相悖，本测试 FAIL；
// 修复后两路一致，PASS。场景参数取自 w10-run/N-01 真机实证。
func TestFlowDirectionParityAcrossEngines(t *testing.T) {
	h := &Handler{}
	const fwdID, userID, userTunnelID, tunnelID = 20, 2, 10, 1

	scenarios := []struct {
		name        string
		tunnelFlow  int64
		inBytes     int64 // 客户端→远端
		outBytes    int64 // 远端→客户端
		wantInFlow  int64
		wantOutFlow int64
		wantQuota   int64
	}{
		// W10 阶段 G1/G2（gost）与 R（realm）所在隧道的真实形态：
		// Flow=1 单向。修复前 gost 按出方向 12,583,527 扣配额，
		// realm 只按入方向 11,568 扣——同隧道两引擎口径相反。
		{name: "single-direction download-heavy", tunnelFlow: 1, inBytes: 267, outBytes: 12583527, wantInFlow: 267, wantOutFlow: 0, wantQuota: 267},
		{name: "single-direction realm-shaped", tunnelFlow: 1, inBytes: 11568, outBytes: 16805264, wantInFlow: 11568, wantOutFlow: 0, wantQuota: 11568},
		{name: "bidirectional download-heavy", tunnelFlow: 2, inBytes: 267, outBytes: 12583527, wantInFlow: 267, wantOutFlow: 12583527, wantQuota: 12583794},
	}

	for _, sc := range scenarios {
		metas := map[int64]repo.FlowUploadForwardMeta{
			fwdID: {
				ForwardID:    fwdID,
				UserID:       userID,
				UserTunnelID: userTunnelID,
				TunnelID:     tunnelID,
				TrafficRatio: 1,
				TunnelFlow:   sc.tunnelFlow,
			},
		}

		// gost 路径：线格式 U=出（远端→客户端）、D=入（客户端→远端）。
		gostBatch := h.buildFlowUploadBatch([]flowItem{
			{N: "20_2_10", U: sc.outBytes, D: sc.inBytes},
		}, metas)
		// nftables/realm 路径：BytesIn=入、BytesOut=出。
		nftBatch := buildNftFlowUploadBatch([]nftTrafficDelta{
			{ForwardID: fwdID, BytesIn: sc.inBytes, BytesOut: sc.outBytes},
		}, metas)

		for label, batch := range map[string]flowUploadBatch{"gost": gostBatch, "nft": nftBatch} {
			if len(batch.flowDeltas) != 1 {
				t.Fatalf("%s/%s: expected 1 flow delta, got %d", sc.name, label, len(batch.flowDeltas))
			}
			delta := batch.flowDeltas[0]
			if delta.InFlow != sc.wantInFlow || delta.OutFlow != sc.wantOutFlow {
				t.Fatalf("%s/%s: expected flow in=%d out=%d, got in=%d out=%d",
					sc.name, label, sc.wantInFlow, sc.wantOutFlow, delta.InFlow, delta.OutFlow)
			}
			if batch.quotaUsage[userID] != sc.wantQuota {
				t.Fatalf("%s/%s: expected quota %d, got %d", sc.name, label, sc.wantQuota, batch.quotaUsage[userID])
			}
			raw := batch.forwardTraffic[fwdID]
			if raw.bytesIn != sc.inBytes || raw.bytesOut != sc.outBytes {
				t.Fatalf("%s/%s: expected raw traffic in=%d out=%d, got in=%d out=%d",
					sc.name, label, sc.inBytes, sc.outBytes, raw.bytesIn, raw.bytesOut)
			}
		}
	}
}
