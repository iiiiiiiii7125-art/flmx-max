package handler

// FIX-R3 包 B（R3-MR-01）故障注入回归：repo 读失败必须让操作诚实
// 失败或走既有警告通道，禁止拿空集/零值继续成功路径。
//
// 注入手法（仓内既有范式）：GORM Query 回调按 Statement.Table /
// Dest 类型 / 计数精确命中目标读，ctx 取消 + AddError 双保险
// （r4_b7_guard_test.go 同款）；每格带注入自检断言——注入未生效
// 则用例先行失败，不许假绿。
//
// 覆盖点（台账 §2.2 MR-01 的 11 点 + 机械复推新增 3 组）：
//   #1 rotate 读入站失败 → TestR3BRotateListInboundsFailureParksApplied
//   #9 resetTraffic 视图读失败 → TestR3BResetTrafficListFailureHonestReceipt
//   #7 拉取面分配读失败 → TestR3BOpenSubscriptionAssignmentsFailure500
//   #8 remoteSourceSave 重读失败 → TestR3BRemoteSourceSaveReReadFailureNoPanic
//   #3 tunnelUpdate 入口集读失败 → TestR3BTunnelUpdateEntryReadFailureHonest
//   N3 tunnelUpdate 隧道名读失败 ×2 → TestR3BTunnelUpdateNameReadFailureKeepsName
//   #4 forwardCreate 入口集读失败 → TestR3BForwardCreateEntryReadFailureNoPersist
//   #5 forwardUpdate 入口集读失败 → TestR3BForwardUpdateEntryReadFailureNoChange
//   #6 cleanup 模式判定读失败 → TestR3BCleanupRemovedEntriesModeReadFailureWarns
//   N1 realmNodeEndpoints 隧道读失败 → TestR3BRealmEndpointsTunnelReadFailure
//   N2 forwardForceDelete 端口集读失败 → TestR3BForceDeletePortsReadFailureWarns
//   #10 nftables 绑定读失败 ×2 → TestR3BNftBindingsReadFailureLogged
// #2（tunnelUpdate 链集读失败）与 #11（flow_policy 死 helper 删除）
// 由登记门机械覆盖：#2 的到达前提是链隧道 apply 成功（需活 agent，
// 单元夹具不可达），其修复形态与 #3 同形（同函数 respondInternalError）；
// #11 是死代码删除，编译 + 门派生集即证据。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

var errR3BInjected = errors.New("r3b injected read failure")

// failQueriesMatching 注册查询回调：命中 match 的查询中，序号
// 大于 failAfter 的（即跳过前 failAfter 次）一律注入失败。
// 返回注销函数（断言 DB 状态前先注销，避免污染后续读）。
func failQueriesMatching(t *testing.T, r *repo.Repository, failAfter int, match func(tx *gorm.DB) bool) func() {
	t.Helper()
	name := "r3b_fail_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	var count atomic.Int32
	err := r.DB().Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if !match(tx) {
			return
		}
		if count.Add(1) > int32(failAfter) {
			ctx, cancel := context.WithCancel(tx.Statement.Context)
			tx.Statement.Context = ctx
			cancel()
			tx.AddError(errR3BInjected)
		}
	})
	if err != nil {
		t.Fatalf("register fault callback: %v", err)
	}
	remove := func() { _ = r.DB().Callback().Query().Remove(name) }
	t.Cleanup(remove)
	return remove
}

func isTable(table string) func(tx *gorm.DB) bool {
	return func(tx *gorm.DB) bool { return tx.Statement != nil && tx.Statement.Table == table }
}

func isDest[T any]() func(tx *gorm.DB) bool {
	return func(tx *gorm.DB) bool {
		if tx.Statement == nil {
			return false
		}
		_, ok := tx.Statement.Dest.(*T)
		return ok
	}
}

func allOf(matchers ...func(tx *gorm.DB) bool) func(tx *gorm.DB) bool {
	return func(tx *gorm.DB) bool {
		for _, m := range matchers {
			if !m(tx) {
				return false
			}
		}
		return true
	}
}

// ---- #1 凭据轮换：入站读失败 → AppliedActive 停 -1 + applyErr ----

func TestR3BRotateListInboundsFailureParksApplied(t *testing.T) {
	r, h := r2b2Open(t)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "r3b-rotate", Kind: model.SubscriptionShare,
		Token: "r3b-rotate-token", SourceAll: 1, Enabled: 1,
	})
	failQueriesMatching(t, r, 0, isDest[[]model.NodeInbound]())
	// 注入自检：目标读必须真的失败。
	if _, err := h.repo.ListNodeInbounds(); err == nil {
		t.Fatal("injection self-check: ListNodeInbounds must fail")
	}
	_, applyErr, err := h.rotateSubscriptionCredentials(sub)
	if err != nil {
		t.Fatalf("rotation persistence must succeed, got err=%v", err)
	}
	if applyErr == nil {
		t.Fatal("list failure must surface as applyErr (receipt discloses push failure), got nil")
	}
	got, gerr := h.repo.GetSubscription(sub.ID)
	if gerr != nil || got == nil {
		t.Fatalf("reload subscription: %v", gerr)
	}
	if got.AppliedActive != -1 {
		t.Fatalf("AppliedActive = %d, want -1 (parked for sync retry; -1 is the not-applied marker)", got.AppliedActive)
	}
	if got.Token == "r3b-rotate-token" {
		t.Fatal("token must have rotated (persistence happened before the failed read)")
	}
}

// ---- #9 流量重置：视图读失败 → 回执诚实失败（重置本身已落库） ----

func TestR3BResetTrafficListFailureHonestReceipt(t *testing.T) {
	r, h := r2b2Open(t)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "r3b-reset-traffic", Kind: model.SubscriptionShare,
		Token: "r3b-reset-traffic-token", SourceAll: 1, Enabled: 1,
		UploadBytes: 12345, DownloadBytes: 67890,
	})
	failQueriesMatching(t, r, 0, isDest[[]model.NodeInbound]())
	body := r2b2AuthPost(t, h.subscriptionResetTraffic, "/api/v1/subscription/reset-traffic",
		map[string]interface{}{"id": sub.ID}, "")
	var env struct {
		Code int `json:"code"`
	}
	if jerr := json.Unmarshal([]byte(body), &env); jerr != nil {
		t.Fatalf("decode receipt: %v body=%s", jerr, body)
	}
	if env.Code == 0 {
		t.Fatalf("view read failure must not render a success receipt with an empty set: %s", body)
	}
}

// ---- #7 订阅拉取面：分配读失败 → HTTP 500 而非空订阅 ----

func TestR3BOpenSubscriptionAssignmentsFailure500(t *testing.T) {
	h, r, ids := subFixture(t)
	sub := createShareSub(t, r, &model.Subscription{
		Name: "r3b-open", Kind: model.SubscriptionShare,
		Token: "r3b-open-token", SourceAll: 1, Enabled: 1,
	})
	now := time.Now().UnixMilli()
	if err := r.DB().Exec(`INSERT INTO user(id, user, pwd, role_id, exp_time, flow, in_flow, out_flow, flow_reset_time, num, created_time, updated_time, status) VALUES(7, 'r3b-user', 'pwd', 1, 2727251700000, 99999, 0, 0, 1, 99999, ?, ?, 1)`, now, now).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := r.SetUserInboundAssignments(7, []repo.UserInboundAssignment{{InboundID: ids["hk-1"], RelayID: 0}}); err != nil {
		t.Fatalf("seed assignments: %v", err)
	}
	sig := h.subURLSigForToken(sub.Token, 7)
	fetch := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/open_api/sub/"+sub.Token+"?user=7&sig="+sig, nil)
		req.Header.Set("User-Agent", "clash-verge/1.0")
		rec := httptest.NewRecorder()
		h.openSubscription(rec, req)
		return rec.Code, rec.Body.String()
	}
	// 对照：无注入时按分配正常出订阅。
	if code, body := fetch(); code != 200 || !strings.Contains(body, "hk-1") {
		t.Fatalf("control fetch: code=%d body=%s", code, body)
	}
	failQueriesMatching(t, r, 0, isDest[[]model.UserInbound]())
	if _, err := h.repo.ListUserInboundAssignments(7); err == nil {
		t.Fatal("injection self-check: ListUserInboundAssignments must fail")
	}
	if code, body := fetch(); code != 500 {
		t.Fatalf("assignment read failure must answer 500, got code=%d body=%s", code, body)
	}
}

// ---- #8 remoteSourceSave：保存后重读失败 → 不 panic、回执正常 ----

func TestR3BRemoteSourceSaveReReadFailureNoPanic(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-remote.db")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	now := time.Now().UnixMilli()
	src := &model.RemoteSource{
		Name: "文件源", Kind: model.RemoteSourceFile, RelayOnly: 0,
		Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.SaveRemoteSource(src); err != nil {
		t.Fatalf("save source: %v", err)
	}
	// 首次 GetRemoteSource（保存入口的按 ID 加载）放行，第二次
	// （保存后 !fetch 分支的重读）注入失败。
	failQueriesMatching(t, r, 1, isDest[model.RemoteSource]())
	payload, _ := json.Marshal(map[string]interface{}{
		"id": src.ID, "name": "文件源", "kind": "file", "relayOnly": false,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/remote-source/save", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	// 修复前：重读 (nil, err) 覆盖 src → remoteSourceViewOf 解引用
	// panic，用例直接崩（红）。修复后：保留刚落库的 src 正常回执。
	h.remoteSourceSave(rec, req)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if jerr := json.Unmarshal(rec.Body.Bytes(), &out); jerr != nil {
		t.Fatalf("decode save response: %v (%s)", jerr, rec.Body.String())
	}
	if out.Code != 0 {
		t.Fatalf("save must succeed despite the re-read failure: code=%d msg=%s body=%s", out.Code, out.Msg, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "文件源") {
		t.Fatalf("receipt must render the saved source view: %s", rec.Body.String())
	}
}

// ---- N3 tunnelUpdate：隧道名回填/现名读失败 → 名不被写空 ----

func TestR3BTunnelUpdateNameReadFailureKeepsName(t *testing.T) {
	namePluck := allOf(isTable("tunnel"), isDest[string]())
	post := func(t *testing.T, h *Handler, id int64) (int, string) {
		return f1PostTunnelUpdate(t, h,
			`{"id":`+strconv.FormatInt(id, 10)+`,"type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	}
	// A：全部名读失败 → 回填读（第一处）即应诚实失败、名保留。
	t.Run("backfill read", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedProbeTargetTunnel(t, h, 91, "r3b-keep-a", "", 0)
		remove := failQueriesMatching(t, h.repo, 0, namePluck)
		code, _ := post(t, h, 91)
		remove()
		if code == 0 {
			t.Fatal("name backfill read failure must fail the update honestly")
		}
		if name, err := h.repo.GetTunnelName(91); err != nil || name != "r3b-keep-a" {
			t.Fatalf("tunnel name = %q err=%v, want r3b-keep-a intact", name, err)
		}
	})
	// B：仅第二处（现名读）失败 → 同样诚实失败，不得误判改名续行。
	t.Run("current name read", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedProbeTargetTunnel(t, h, 92, "r3b-keep-b", "", 0)
		remove := failQueriesMatching(t, h.repo, 1, namePluck)
		code, _ := post(t, h, 92)
		remove()
		if code == 0 {
			t.Fatal("current-name read failure must fail the update honestly")
		}
		if name, err := h.repo.GetTunnelName(92); err != nil || name != "r3b-keep-b" {
			t.Fatalf("tunnel name = %q err=%v, want r3b-keep-b intact", name, err)
		}
	})
}

// ---- #3 tunnelUpdate：提交后入口集读失败 → 诚实失败 ----

func TestR3BTunnelUpdateEntryReadFailureHonest(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 93, "r3b-entry", "", 0)
	// chain_tunnel 上的 []int64 Pluck 共三发（探针实测序列）：
	// 旧入口集读（查错）、事务段一读、提交后新入口集读（本点）。
	// 前两发放行，第三发注入失败。
	failQueriesMatching(t, h.repo, 2, allOf(isTable("chain_tunnel"), isDest[[]int64]()))
	code, msg := f1PostTunnelUpdate(t, h,
		`{"id":93,"name":"r3b-entry","type":1,"status":1,"inNodeId":[{"nodeId":10,"protocol":"tls"}]}`)
	if code == 0 {
		t.Fatalf("post-commit entry read failure must fail honestly, got success msg=%s", msg)
	}
}

// ---- #4 forwardCreate：入口集读失败 → 不落库 ----

func TestR3BForwardCreateEntryReadFailureNoPersist(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-fwd-create.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.11")
	tunnelID := seedTunnelForNftables(t, h, "r3b-create-tunnel", nodeID)
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	// chain_tunnel 的 []int64 Pluck 第一发即创建校验段的入口集
	// 直读（本点；探针实测：命中它时日志 op 为 forwardCreate:
	// tunnelEntryNodeIDs，第二发才是 tunnelUsesNftables 的查错
	// 读）。单发命中第一发。
	var count atomic.Int32
	cbName := "r3b_fail_create_entry"
	cbErr := r.DB().Callback().Query().Before("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "chain_tunnel" {
			return
		}
		if _, ok := tx.Statement.Dest.(*[]int64); !ok {
			return
		}
		if count.Add(1) == 1 {
			ctx, cancel := context.WithCancel(tx.Statement.Context)
			tx.Statement.Context = ctx
			cancel()
			tx.AddError(errR3BInjected)
		}
	})
	if cbErr != nil {
		t.Fatalf("register fault callback: %v", cbErr)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(cbName) })

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"tunnelId": tunnelID, "name": "r3b-fwd", "remoteAddr": "203.0.113.5:443", "inPort": 30005,
	})
	res := httptest.NewRecorder()
	h.forwardCreate(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if jerr := json.Unmarshal(res.Body.Bytes(), &payload); jerr != nil {
		t.Fatalf("decode response: %v body=%s", jerr, res.Body.String())
	}
	// 判别口径（探针校准）：本夹具形态下，基线的空集会被下游端口
	// 守卫挡成业务拒绝（code -1「转发入口端口不存在」）——同样不
	// 落库，但把存储故障伪装成业务错误、误导排障。修复口径是
	// 存储失败走内部错误封套（code -2），故断言错误类别而非仅
	// 「非成功」。
	if payload.Code != -2 {
		t.Fatalf("entry read failure must surface as internal error (code -2), got code=%d body=%s", payload.Code, res.Body.String())
	}
	var n int64
	if err := r.DB().Raw(`SELECT COUNT(*) FROM forward`).Scan(&n).Error; err != nil {
		t.Fatalf("count forwards: %v", err)
	}
	if n != 0 {
		t.Fatalf("no forward may be persisted on entry read failure, found %d", n)
	}
}

// ---- #5 forwardUpdate：入口集读失败 → 转发不变 ----

func TestR3BForwardUpdateEntryReadFailureNoChange(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-fwd-update.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.12")
	tunnelID := seedTunnelForNftables(t, h, "r3b-update-tunnel", nodeID)
	if err := r.DB().Exec(`
		INSERT INTO user_tunnel(user_id, tunnel_id, speed_id, num, flow, in_flow, out_flow, flow_reset_time, exp_time, status)
		VALUES(1, ?, NULL, 99999, 99999, 0, 0, 1, 2727251700000, 1)`, tunnelID).Error; err != nil {
		t.Fatalf("seed user_tunnel: %v", err)
	}
	fwd := seedForwardForNftables(t, h, tunnelID, nodeID, "203.0.113.5:443", 30006)
	// 与 create 同款单发命中：第二发 chain_tunnel 查询失败。
	var count atomic.Int32
	cbName := "r3b_fail_update_entry"
	cbErr := r.DB().Callback().Query().Before("gorm:query").Register(cbName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "chain_tunnel" {
			return
		}
		if count.Add(1) == 2 {
			ctx, cancel := context.WithCancel(tx.Statement.Context)
			tx.Statement.Context = ctx
			cancel()
			tx.AddError(errR3BInjected)
		}
	})
	if cbErr != nil {
		t.Fatalf("register fault callback: %v", cbErr)
	}
	t.Cleanup(func() { _ = r.DB().Callback().Query().Remove(cbName) })

	req := newAuthenticatedJSONRequest(t, map[string]interface{}{
		"id": fwd.ID, "tunnelId": tunnelID, "name": "r3b-fwd-upd",
		"remoteAddr": "203.0.113.9:8443", "inPort": 30006,
	})
	res := httptest.NewRecorder()
	h.forwardUpdate(res, req)
	var payload struct {
		Code int `json:"code"`
	}
	if jerr := json.Unmarshal(res.Body.Bytes(), &payload); jerr != nil {
		t.Fatalf("decode response: %v body=%s", jerr, res.Body.String())
	}
	if payload.Code == 0 {
		t.Fatalf("entry read failure must fail forwardUpdate honestly: %s", res.Body.String())
	}
	got, gerr := h.getForwardRecord(fwd.ID)
	if gerr != nil || got == nil {
		t.Fatalf("reload forward: %v", gerr)
	}
	if got.RemoteAddr != "203.0.113.5:443" {
		t.Fatalf("forward remote addr changed despite the failed update: %q", got.RemoteAddr)
	}
}

// ---- #6 cleanup：模式判定读失败 → warning 跳过清理，不走 gost 误清 ----

func TestR3BCleanupRemovedEntriesModeReadFailureWarns(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-cleanup.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	entryNode := seedRelayTargetNode(t, r, "stay", "198.51.100.13")
	removedNode := seedRelayTargetNode(t, r, "gone", "198.51.100.14")
	tunnelID := seedTunnelForNftables(t, h, "r3b-cleanup-tunnel", entryNode)
	seedForwardForNftables(t, h, tunnelID, removedNode, "203.0.113.5:443", 30007)
	failQueriesMatching(t, r, 0, isTable("chain_tunnel"))
	if _, err := h.repo.TunnelEntryNodeIDs(tunnelID); err == nil {
		t.Fatal("injection self-check: TunnelEntryNodeIDs must fail")
	}
	warnings := h.cleanupTunnelForwardRuntimesOnRemovedEntryNodes(tunnelID,
		[]int64{entryNode, removedNode}, []int64{entryNode})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "读取隧道模式出错") {
		t.Fatalf("mode read failure must yield exactly the skip-cleanup warning, got %v", warnings)
	}
}

// ---- N1 realmNodeEndpoints：隧道读失败 → 回错而非静默剔除 ----

func TestR3BRealmEndpointsTunnelReadFailure(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-realm.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "realm-node", "198.51.100.15")
	tunnelID := seedTunnelForNftables(t, h, "r3b-realm-tunnel", nodeID)
	if err := r.DB().Exec(`UPDATE tunnel SET engine = 'realm' WHERE id = ?`, tunnelID).Error; err != nil {
		t.Fatal(err)
	}
	seedForwardForNftables(t, h, tunnelID, nodeID, "203.0.113.5:443", 30008)
	// 对照：无注入时端点正常渲染。
	endpoints, _, err := h.realmNodeEndpoints(nodeID, nil)
	if err != nil || len(endpoints) == 0 {
		t.Fatalf("control: endpoints=%v err=%v", endpoints, err)
	}
	failQueriesMatching(t, r, 0, isDest[model.Tunnel]())
	if _, _, err := h.realmNodeEndpoints(nodeID, nil); err == nil {
		t.Fatal("tunnel read failure must surface as an error, not a silently dropped forward")
	}
}

// ---- N2 forwardForceDelete：旧端口集读失败 → 清理未确认警告 ----

func TestR3BForceDeletePortsReadFailureWarns(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r3b-force-delete.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedRelayTargetNode(t, r, "entry", "198.51.100.16")
	tunnelID := seedTunnelForNftables(t, h, "r3b-force-tunnel", nodeID)
	fwd := seedForwardForNftables(t, h, tunnelID, nodeID, "203.0.113.5:443", 30009)
	// forward_port 的 []model.ForwardPort 读共两发（探针实测）：
	// 残留提示读（第一发）放行，删除后的主读（第二发，本点）注入
	// 失败。注：repo.ListForwardPorts 的 GORM Dest 是模型行切片
	// []model.ForwardPort 而非返回型的 Record 切片。
	failQueriesMatching(t, r, 1, isDest[[]model.ForwardPort]())
	req := newAuthenticatedJSONRequest(t, map[string]interface{}{"id": fwd.ID})
	res := httptest.NewRecorder()
	h.forwardForceDelete(res, req)
	body := res.Body.String()
	if !strings.Contains(body, "清理未确认") {
		t.Fatalf("ports read failure must surface the cleanup-unconfirmed warning, got %s", body)
	}
	if _, gerr := h.getForwardRecord(fwd.ID); gerr == nil {
		t.Fatal("forward row must be deleted (force-delete semantics preserved)")
	}
}

// ---- #10 nftables 同步：绑定读失败（成功/失败两支）→ 点名日志 ----

func TestR3BNftBindingsReadFailureLogged(t *testing.T) {
	captureLog := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		old := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(old) })
		return &buf
	}
	setup := func(t *testing.T, reconcileErr error) (*Handler, int64) {
		fixture := setupNftablesHandler(t)
		h := fixture.handler
		seedNftablesSSHConfig(t, h, fixture.nodeID)
		tunnelID := seedTunnelForNftables(t, h, "r3b-nft-tunnel", fixture.nodeID)
		seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
		if err := h.repo.DB().Exec(`DROP TABLE nft_rule_binding`).Error; err != nil {
			t.Fatalf("drop bindings table: %v", err)
		}
		manager := &fakeNftablesManager{reconcileErr: reconcileErr}
		h.nftablesManager = newSerializedNftManager(manager)
		return h, fixture.nodeID
	}
	t.Run("reconcile failure branch", func(t *testing.T) {
		buf := captureLog(t)
		h, nodeID := setup(t, errors.New("r3b reconcile boom"))
		err := h.syncNftablesNode(nodeID)
		if err == nil {
			t.Fatal("sync must return the reconcile error")
		}
		if !strings.Contains(buf.String(), "error marking failed") {
			t.Fatalf("bindings read failure on the error branch must be logged by name, log=%s", buf.String())
		}
	})
	t.Run("success branch sweep", func(t *testing.T) {
		buf := captureLog(t)
		h, nodeID := setup(t, nil)
		if err := h.syncNftablesNode(nodeID); err != nil {
			t.Fatalf("sync must succeed despite the bindings read failure: %v", err)
		}
		if !strings.Contains(buf.String(), "stale sweep failed") {
			t.Fatalf("bindings read failure on the success branch must be logged by name, log=%s", buf.String())
		}
	})
}
