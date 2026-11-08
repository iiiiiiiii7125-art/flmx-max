package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// Round 3 K6 隧道转发运行语义永久回归：
//   - B06-001：隧道更新的非入口段现口沿用身份 =（段类型，节点），位置
//     移位（跳序对调/前插/删首段）不得静默重挑；凡未沿用存量口的重挑
//     一律经回执 warnings 点名。入口段不经选口路径、保持不变。
//   - B06-002：连同转发删除的 replace 目标存在性+禁用校验在统一执行点
//     先于 ForwardCount 判断，无转发时同样执行（单条/批量六例）。
//   - B08-001：恢复/批量恢复/批量重部署/配额释放在禁用隧道上一律被
//     隧道禁用门拦下（不改 status、不下发）。
//   - B08-002：批量恢复 nft 转发的 Num 上限按批内预留计数，同批多条
//     不得共用批前计数越限（混合引擎同批一致）。
//   - B09-002：syncUserMaxConnForwards 枚举用户全部转发（含暂停态），
//     暂停转发也被重下发（节点侧收敛的契约面见 tests/contract 的
//     TestK6PausedForwardMaxConnResyncsOnUpdate）。

// ---------- B06-001 ----------

type k6ChainRow struct {
	chainType string
	nodeID    int64
	port      interface{}
	inx       int
}

func seedK6TunnelWithRows(t *testing.T, h *Handler, tunnelID int64, name string, rows []k6ChainRow) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(?, ?, 1, 2, 'tls', 1, ?, ?, 1, ?, '', '', 0)
	`, tunnelID, name, now, now, tunnelID).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	for _, row := range rows {
		if err := h.repo.DB().Exec(`
			INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
			VALUES(?, ?, ?, ?, 'round', ?, 'tls')
		`, tunnelID, row.chainType, row.nodeID, row.port, row.inx).Error; err != nil {
			t.Fatalf("insert chain row: %v", err)
		}
	}
}

func k6ChainPortByNode(t *testing.T, h *Handler, tunnelID int64, chainType string, nodeID int64) int {
	t.Helper()
	var port int
	if err := h.repo.DB().Raw(`SELECT COALESCE(port, 0) FROM chain_tunnel WHERE tunnel_id = ? AND chain_type = ? AND node_id = ?`, tunnelID, chainType, nodeID).Scan(&port).Error; err != nil {
		t.Fatalf("read chain port: %v", err)
	}
	return port
}

func k6WarningsJoined(env k9Envelope) string {
	warnings, _ := env.Data["warnings"].([]interface{})
	parts := make([]string, 0, len(warnings))
	for _, w := range warnings {
		if s, ok := w.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ";")
}

func k6TwoHopRows() []k6ChainRow {
	return []k6ChainRow{
		{"1", 10, nil, 1},
		{"2", 12, 36008, 1},
		{"2", 14, 36508, 2},
		{"3", 11, 35009, 1},
	}
}

func k6UpdatePayload(id int64, name string, hops ...int64) map[string]interface{} {
	chain := make([]interface{}, 0, len(hops))
	for _, nodeID := range hops {
		chain = append(chain, []map[string]interface{}{{"nodeId": nodeID, "protocol": "tls"}})
	}
	return map[string]interface{}{
		"id": id, "name": name, "type": 2, "status": 1, "flow": 1, "trafficRatio": 1,
		"inNodeId":   []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
		"chainNodes": chain,
		"outNodeId":  []map[string]interface{}{{"nodeId": 11, "protocol": "tls"}},
	}
}

func TestK6TunnelUpdateHopSwapKeepsPortsByNode(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Node(t, h.repo, 14, "chain-b", "10.0.0.5", "36500-36510")
	seedK6TunnelWithRows(t, h, 90, "k6-swap", k6TwoHopRows())

	// 仅交换两跳顺序、port 缺省：口必须跟节点走，不得按槽位重挑。
	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(90, "k6-swap", 14, 12))
	if env.Code != 0 {
		t.Fatalf("swap update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := k6ChainPortByNode(t, h, 90, "2", 12); got != 36008 {
		t.Fatalf("B06-001: node12 port must follow the node across reorder, got %d, want 36008", got)
	}
	if got := k6ChainPortByNode(t, h, 90, "2", 14); got != 36508 {
		t.Fatalf("B06-001: node14 port must follow the node across reorder, got %d, want 36508", got)
	}
	if got := k6ChainPortByNode(t, h, 90, "3", 11); got != 35009 {
		t.Fatalf("B06-001: exit port must stay 35009, got %d", got)
	}
	if joined := k6WarningsJoined(env); joined != "" {
		t.Fatalf("B06-001: no port moved, receipt must not warn, got %q", joined)
	}
	// 入口段不变断言：入口行仍是 node10、无端口、inx 不动。
	var entryCount int
	if err := h.repo.DB().Raw(`SELECT COUNT(*) FROM chain_tunnel WHERE tunnel_id = 90 AND chain_type = '1' AND node_id = 10 AND port IS NULL AND inx = 1`).Scan(&entryCount).Error; err != nil {
		t.Fatal(err)
	}
	if entryCount != 1 {
		t.Fatalf("entry segment must be untouched by non-entry port resolution, got %d matching rows", entryCount)
	}
}

func TestK6TunnelUpdatePrependHopKeepsExistingPorts(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Node(t, h.repo, 15, "chain-c", "10.0.0.6", "36600-36610")
	seedK6TunnelWithRows(t, h, 91, "k6-prepend", []k6ChainRow{
		{"1", 10, nil, 1},
		{"2", 12, 36008, 1},
		{"3", 11, 35009, 1},
	})

	// 头部前插新跳 node15：node12 后移一位，现口必须沿用。
	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(91, "k6-prepend", 15, 12))
	if env.Code != 0 {
		t.Fatalf("prepend update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := k6ChainPortByNode(t, h, 91, "2", 12); got != 36008 {
		t.Fatalf("B06-001: prepend must not move node12's port, got %d, want 36008", got)
	}
	if got := k6ChainPortByNode(t, h, 91, "2", 15); got < 36600 || got > 36610 {
		t.Fatalf("B06-001: genuinely new hop must get a fresh in-spec port, got %d", got)
	}
	if joined := k6WarningsJoined(env); joined != "" {
		t.Fatalf("B06-001: prepend without port moves must not warn, got %q", joined)
	}
}

func TestK6TunnelUpdateDeleteFirstHopKeepsSecondHopPort(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Node(t, h.repo, 14, "chain-b", "10.0.0.5", "36500-36510")
	seedK6TunnelWithRows(t, h, 92, "k6-del-first", k6TwoHopRows())

	// 删首跳：node14 前移一位，现口必须沿用。
	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(92, "k6-del-first", 14))
	if env.Code != 0 {
		t.Fatalf("delete-first-hop update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := k6ChainPortByNode(t, h, 92, "2", 14); got != 36508 {
		t.Fatalf("B06-001: deleting the first hop must not move node14's port, got %d, want 36508", got)
	}
	if joined := k6WarningsJoined(env); joined != "" {
		t.Fatalf("B06-001: delete-first-hop without port moves must not warn, got %q", joined)
	}
}

func TestK6TunnelUpdateShiftedNodePortTakenRepicksWithWarning(t *testing.T) {
	h := setupK9Handler(t)
	seedK9Node(t, h.repo, 14, "chain-b", "10.0.0.5", "36500-36510")
	seedK6TunnelWithRows(t, h, 93, "k6-shift-taken", k6TwoHopRows())
	// node14 的现口被一条入站占用抢占：移位后沿用判定失败，必须
	// 重挑且回执点名旧口（不许静默）。（原格以联邦预留为占用源，
	// 联邦源已随共享板块整删；链路段源有 (node_id, port) 唯一索引
	// 不能造同口撞态，改以入站源同形复证。）
	seedK9InboundOccupant(t, h, 14, 36508)

	env := k9Post(t, h, h.tunnelUpdate, "/api/v1/tunnel/update", k6UpdatePayload(93, "k6-shift-taken", 14, 12))
	if env.Code != 0 {
		t.Fatalf("shift+taken update must succeed, code=%d msg=%s", env.Code, env.Msg)
	}
	if got := k6ChainPortByNode(t, h, 93, "2", 14); got == 36508 || got < 36500 || got > 36510 {
		t.Fatalf("B06-001: occupied port must be re-picked in spec, got %d", got)
	}
	if got := k6ChainPortByNode(t, h, 93, "2", 12); got != 36008 {
		t.Fatalf("B06-001: unaffected node12 must keep 36008, got %d", got)
	}
	joined := k6WarningsJoined(env)
	if !strings.Contains(joined, "36508") || !strings.Contains(joined, "变更为") {
		t.Fatalf("B06-001: re-pick of a persisted port must surface in warnings naming the old port, got %q", joined)
	}
}

// ---------- B06-002 ----------

func k6CreateType1Tunnel(t *testing.T, h *Handler, name string) int64 {
	t.Helper()
	env := k9Post(t, h, h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": name, "type": 1, "status": 1,
		"inNodeId": []map[string]interface{}{{"nodeId": 10, "protocol": "tls"}},
	})
	if env.Code != 0 {
		t.Fatalf("create tunnel %s: code=%d msg=%s", name, env.Code, env.Msg)
	}
	var id int64
	if err := h.repo.DB().Raw(`SELECT id FROM tunnel WHERE name = ?`, name).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func k6DeleteWithForwards(t *testing.T, h *Handler, payload map[string]interface{}) k9Envelope {
	t.Helper()
	res := httptest.NewRecorder()
	h.tunnelDeleteWithForwards(res, newAuthenticatedJSONRequest(t, payload))
	var env k9Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env
}

func k6BatchDeleteWithForwards(t *testing.T, h *Handler, payload map[string]interface{}) k9Envelope {
	t.Helper()
	res := httptest.NewRecorder()
	h.tunnelBatchDeleteWithForwards(res, newAuthenticatedJSONRequest(t, payload))
	var env k9Envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env
}

// 无转发源隧道 ×（目标不存在 / 已禁用 / 合法）×（单条 / 批量）六例：
// 不存在/已禁用必须点名拒绝且源隧道保留；合法目标才删除成功。
func TestK6ReplaceTargetValidatedWithoutForwards(t *testing.T) {
	targets := []struct {
		name    string
		prepare func(t *testing.T, h *Handler) int64
		wantMsg string // "" = 合法目标，期望成功
	}{
		{"nonexistent", func(t *testing.T, h *Handler) int64 { return 99999 }, "目标隧道不存在"},
		{"disabled", func(t *testing.T, h *Handler) int64 {
			id := k6CreateType1Tunnel(t, h, "k6-target-disabled")
			if err := h.repo.DB().Exec(`UPDATE tunnel SET status = 0 WHERE id = ?`, id).Error; err != nil {
				t.Fatal(err)
			}
			return id
		}, "目标隧道已禁用"},
		{"valid", func(t *testing.T, h *Handler) int64 {
			return k6CreateType1Tunnel(t, h, "k6-target-valid")
		}, ""},
	}
	for _, tc := range targets {
		for _, batch := range []bool{false, true} {
			name := tc.name + "-single"
			if batch {
				name = tc.name + "-batch"
			}
			t.Run(name, func(t *testing.T) {
				h := setupK9Handler(t)
				srcID := k6CreateType1Tunnel(t, h, "k6-src")
				targetID := tc.prepare(t, h)

				var env k9Envelope
				if batch {
					env = k6BatchDeleteWithForwards(t, h, map[string]interface{}{
						"ids": []int64{srcID}, "action": "replace", "targetTunnelId": targetID,
					})
				} else {
					env = k6DeleteWithForwards(t, h, map[string]interface{}{
						"id": srcID, "action": "replace", "targetTunnelId": targetID,
					})
				}
				_, _, srcCount := k9TunnelRow(t, h, "k6-src")
				if tc.wantMsg == "" {
					if env.Code != 0 {
						t.Fatalf("B06-002: replace to a valid target must succeed, code=%d msg=%s", env.Code, env.Msg)
					}
					if srcCount != 0 {
						t.Fatalf("B06-002: source tunnel must be deleted on success, %d rows remain", srcCount)
					}
					return
				}
				if env.Code == 0 {
					t.Fatalf("B06-002: replace to %s target without forwards must be rejected, got success", tc.name)
				}
				if !strings.Contains(env.Msg, tc.wantMsg) {
					t.Fatalf("B06-002: rejection must name %q, got msg=%q", tc.wantMsg, env.Msg)
				}
				if srcCount != 1 {
					t.Fatalf("B06-002: rejected replace must keep the source tunnel, got %d rows", srcCount)
				}
			})
		}
	}
}

// ---------- B08-001 ----------

// seedK6DisabledTunnelNftForward 在 nft 夹具上造「禁用隧道 + 暂停转发」，
// 返回 handler、夹具与转发。
func seedK6DisabledTunnelNftForward(t *testing.T) (*Handler, nftablesTestFixture, *forwardRecord) {
	t.Helper()
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	forward := seedNftForwardForDelete(t, h, fixture)
	now := time.Now().UnixMilli()
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, now); err != nil {
		t.Fatalf("pause forward: %v", err)
	}
	if err := h.repo.DB().Exec(`UPDATE tunnel SET status = 0 WHERE id = ?`, forward.TunnelID).Error; err != nil {
		t.Fatalf("disable tunnel: %v", err)
	}
	return h, fixture, forward
}

func k6ForwardStatus(t *testing.T, h *Handler, forwardID int64) int {
	t.Helper()
	fr, err := h.repo.GetForwardRecord(forwardID)
	if err != nil || fr == nil {
		t.Fatalf("get forward: %v", err)
	}
	return fr.Status
}

type k6BatchEnvelope struct {
	Code int                  `json:"code"`
	Msg  string               `json:"msg"`
	Data batchOperationResult `json:"data"`
}

func k6PostBatch(t *testing.T, fn func(http.ResponseWriter, *http.Request), payload map[string]interface{}) k6BatchEnvelope {
	t.Helper()
	res := httptest.NewRecorder()
	fn(res, newAuthenticatedJSONRequest(t, payload))
	var env k6BatchEnvelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		t.Fatalf("decode batch envelope: %v", err)
	}
	return env
}

func TestK6ForwardResumeRejectedOnDisabledTunnel(t *testing.T) {
	h, fixture, forward := seedK6DisabledTunnelNftForward(t)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	res := httptest.NewRecorder()
	h.forwardResume(res, newAuthenticatedJSONRequest(t, map[string]interface{}{"id": forward.ID}))
	code, msg := decodePendingEnvelopeCode(t, res)
	if code == 0 {
		t.Fatalf("B08-001: resume on a disabled tunnel must be rejected, got success")
	}
	if !strings.Contains(msg, "隧道已禁用") {
		t.Fatalf("B08-001: rejection must say 隧道已禁用， got %q", msg)
	}
	if got := k6ForwardStatus(t, h, forward.ID); got != model.ForwardStatusPaused {
		t.Fatalf("B08-001: rejected resume must not change status, got %d", got)
	}
	if manager.reconcileHit != 0 {
		t.Fatalf("B08-001: rejected resume must not touch the data plane, reconcile hits=%d (node %d)", manager.reconcileHit, fixture.nodeID)
	}
}

func TestK6BatchResumeRejectedOnDisabledTunnel(t *testing.T) {
	h, _, forward := seedK6DisabledTunnelNftForward(t)

	env := k6PostBatch(t, h.forwardBatchResume, map[string]interface{}{"ids": []int64{forward.ID}})
	if env.Code != 0 {
		t.Fatalf("batch resume envelope must succeed with per-item failures, code=%d msg=%s", env.Code, env.Msg)
	}
	if env.Data.SuccessCount != 0 || env.Data.FailCount != 1 {
		t.Fatalf("B08-001: batch resume on disabled tunnel must be 0 success / 1 failure, got %+v", env.Data)
	}
	if len(env.Data.Failures) != 1 || !strings.Contains(env.Data.Failures[0].Reason, "隧道已禁用") {
		t.Fatalf("B08-001: failure reason must name 隧道已禁用， got %+v", env.Data.Failures)
	}
	if got := k6ForwardStatus(t, h, forward.ID); got != model.ForwardStatusPaused {
		t.Fatalf("B08-001: rejected batch resume must not change status, got %d", got)
	}
}

func TestK6BatchRedeployRejectedOnDisabledTunnel(t *testing.T) {
	h, _, forward := seedK6DisabledTunnelNftForward(t)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	env := k6PostBatch(t, h.forwardBatchRedeploy, map[string]interface{}{"ids": []int64{forward.ID}})
	if env.Code != 0 {
		t.Fatalf("batch redeploy envelope must succeed with per-item failures, code=%d msg=%s", env.Code, env.Msg)
	}
	if env.Data.SuccessCount != 0 || env.Data.FailCount != 1 {
		t.Fatalf("B08-001: batch redeploy on disabled tunnel must be 0 success / 1 failure, got %+v", env.Data)
	}
	if len(env.Data.Failures) != 1 || !strings.Contains(env.Data.Failures[0].Reason, "隧道已禁用") {
		t.Fatalf("B08-001: failure reason must name 隧道已禁用， got %+v", env.Data.Failures)
	}
	if manager.reconcileHit != 0 {
		t.Fatalf("B08-001: rejected redeploy must not touch the data plane, reconcile hits=%d", manager.reconcileHit)
	}
}

func TestK6QuotaReleaseSkipsDisabledTunnelForward(t *testing.T) {
	h, _, forward := seedK6DisabledTunnelNftForward(t)
	manager := h.nftablesManager.Unwrap().(*fakeNftablesManager)

	h.applyUserQuotaRelease(&repo.UserQuotaRelease{
		UserID:      forward.UserID,
		UnblockUser: true,
		ForwardIDs:  []int64{forward.ID},
	}, time.Now().UnixMilli())

	if got := k6ForwardStatus(t, h, forward.ID); got != model.ForwardStatusPaused {
		t.Fatalf("B08-001: quota release must not reactivate a forward on a disabled tunnel, status=%d", got)
	}
	if manager.reconcileHit != 0 {
		t.Fatalf("B08-001: quota release on a disabled tunnel must not reconcile, hits=%d", manager.reconcileHit)
	}
}

// ---------- B08-002 ----------

// 混合引擎同批恢复：用户 Num=2、已有 1 条 active；同批提交 3 条 nft
// 暂停转发 + 1 条 gost 暂停转发（批量入口按 id 序处理，gost 条目在
// nft 之后）。只有 1 条 nft 能补满名额，其余条目——包括 gost 条目——
// 必须被同一道含批内预留的 Num 门以「转发数量已达上限」逐条拦下，
// 最终 active 数恰为 Num：nft 条目不得共用批前计数越限，gost 条目
// 也不得因 nft 名额未写库而穿透。
func TestK6BatchResumeNftNumLimitCountsBatchItself(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "k6-num-tunnel", fixture.nodeID)
	if err := h.repo.DB().Exec(`UPDATE "user" SET num = 2 WHERE id = 1`).Error; err != nil {
		t.Fatalf("set user num: %v", err)
	}
	now := time.Now().UnixMilli()

	// 1 条已 active（占 1 个名额）。
	_ = seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8081", 20001)
	// 3 条 nft 暂停转发。
	pausedNFT := make([]int64, 0, 3)
	for i, port := range []int{20002, 20003, 20004} {
		fwd := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:808"+string(rune('2'+i)), port)
		if err := h.repo.UpdateForwardStatus(fwd.ID, model.ForwardStatusPaused, now); err != nil {
			t.Fatalf("pause nft forward: %v", err)
		}
		pausedNFT = append(pausedNFT, fwd.ID)
	}
	// 1 条 gost 暂停转发（另一节点/隧道；id 最大、批内最后处理）。
	gostNode, err := h.repo.CreateNode("k6-gost-node", "secret2", "198.51.100.11", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, nil, "")
	if err != nil {
		t.Fatalf("create gost node: %v", err)
	}
	_ = gostNode
	var gostNodeID int64
	if err := h.repo.DB().Raw(`SELECT id FROM node WHERE name = 'k6-gost-node'`).Scan(&gostNodeID).Error; err != nil || gostNodeID <= 0 {
		t.Fatalf("read gost node id: %v (id=%d)", err, gostNodeID)
	}
	gostTunnelID := seedTunnelForNftables(t, h, "k6-gost-tunnel", gostNodeID)
	gostFwd := seedForwardForNftables(t, h, gostTunnelID, gostNodeID, "203.0.113.10:8080", 21000)
	if err := h.repo.UpdateForwardStatus(gostFwd.ID, model.ForwardStatusPaused, now); err != nil {
		t.Fatalf("pause gost forward: %v", err)
	}

	ids := append([]int64{gostFwd.ID}, pausedNFT...)
	env := k6PostBatch(t, h.forwardBatchResume, map[string]interface{}{"ids": ids})
	if env.Code != 0 {
		t.Fatalf("batch resume envelope: code=%d msg=%s", env.Code, env.Msg)
	}
	if env.Data.SuccessCount != 1 || env.Data.FailCount != 3 {
		t.Fatalf("B08-002: want exactly 1 success (Num headroom) / 3 failures, got %+v", env.Data)
	}
	limitFailures := 0
	for _, failure := range env.Data.Failures {
		if strings.Contains(failure.Reason, "转发数量已达上限") {
			limitFailures++
		}
	}
	if limitFailures != 3 {
		t.Fatalf("B08-002: all three over-limit items (2 nft + 1 gost) must fail with the Num limit reason, got failures %+v", env.Data.Failures)
	}
	active, err := h.repo.CountActiveForwardsByUser(1)
	if err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("B08-002: active forwards after batch must equal Num=2, got %d", active)
	}
}

// ---------- B09-002 ----------

// syncUserMaxConnForwards 必须枚举暂停转发：节点离线时同步会产生
// 「不在线/下发失败」warnings——暂停转发若未被枚举则 warnings 为空、
// 节点上的 user_conn_limit 永远停留在旧值。启用中转发作对照组。
func TestK6SyncUserMaxConnCoversPausedForwards(t *testing.T) {
	h := setupK9Handler(t)
	now := time.Now().UnixMilli()
	if err := h.repo.DB().Exec(`
		INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(95, 'k6-maxconn', 1, 1, 'tls', 1, ?, ?, 1, 95, '', '', 0)
	`, now, now).Error; err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(95, '1', 10, NULL, 'round', 1, 'tls')
	`).Error; err != nil {
		t.Fatalf("insert chain row: %v", err)
	}
	// 管理员用户 id=1 由 repo 空库 seed 提供；转发归属用户 1、节点 10
	// 无会话（离线），同步必然产生下发失败 warnings。
	if err := h.repo.DB().Exec(`
		INSERT INTO forward(id, user_id, user_name, name, tunnel_id, remote_addr, strategy, status, max_conn, created_time, updated_time)
		VALUES(9501, 1, 'admin@user', 'k6-paused-fwd', 95, '203.0.113.20:443', 'fifo', 0, 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert paused forward: %v", err)
	}
	if err := h.repo.DB().Exec(`
		INSERT INTO forward_port(forward_id, node_id, port) VALUES(9501, 10, 30005)
	`).Error; err != nil {
		t.Fatalf("insert forward port: %v", err)
	}

	warnings, err := h.syncUserMaxConnForwards(1)
	if err != nil {
		t.Fatalf("syncUserMaxConnForwards: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("B09-002: paused forward must be enumerated and synced (offline node must yield warnings), got none — paused forwards are being skipped")
	}
	// 同步不得改变暂停态。
	if got := k6ForwardStatus(t, h, 9501); got != model.ForwardStatusPaused {
		t.Fatalf("B09-002: sync must not change the paused status, got %d", got)
	}
}
