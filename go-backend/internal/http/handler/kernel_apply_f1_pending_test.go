package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/kernel"
	"go-backend/internal/security"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// F1 (R4)：errApplyConfirmPending 哨兵的分流必须覆盖全部用户直达
// 回执——已证实落地按成功收尾、未证实置 pending、绝不写「下发失
// 败」。本文件分两层：纯函数层（pushFailureMsg / reconcile
// notes / refresh notes，快速）与端到端层（真 WS 假 agent、20s
// 回执超时后 pending，逐个收据面）。

func TestPushFailureMsgPending(t *testing.T) {
	cases := []struct {
		prefix string
		err    error
		want   string
	}{
		{"已保存，但下发到节点失败", errApplyConfirmPending, "已保存，下发已发出，正在确认生效状态"},
		{"已标记删除，但下发到节点失败，后台将自动重试", errApplyConfirmPending, "已标记删除，下发已发出，正在确认生效状态，后台将自动重试"},
		{"已重置，但下发到节点失败", errApplyConfirmPending, "已重置，下发已发出，正在确认生效状态"},
		{"操作成功", errApplyConfirmPending, "操作成功，下发已发出，正在确认生效状态"},
	}
	for _, tc := range cases {
		if got := pushFailureMsg(tc.prefix, tc.err); got != tc.want {
			t.Fatalf("pushFailureMsg(%q, %v) = %q, want %q", tc.prefix, tc.err, got, tc.want)
		}
	}
	// 真失败仍缀「下发失败」并附原文；仓储错仍只留固定前缀。
	if got := pushFailureMsg("已保存，但下发到节点失败", errors.New("节点不在线")); !strings.Contains(got, "下发到节点失败") || !strings.Contains(got, "节点不在线") {
		t.Fatalf("real failure must keep 下发失败 + cause, got %q", got)
	}
	if got := pushFailureMsg("已保存，但下发到节点失败", &repo.StorageError{Err: errors.New("db boom")}); got != "已保存，但下发到节点失败" {
		t.Fatalf("repo error must keep the bare prefix, got %q", got)
	}
	// wrapped pending 也必须分流（errors.Is）。
	if got := pushFailureMsg("已保存，但下发到节点失败", errors.Join(errors.New("x"), errApplyConfirmPending)); got != "已保存，下发已发出，正在确认生效状态" {
		t.Fatalf("wrapped pending must divert, got %q", got)
	}
}

func TestWhitelistReconcileFailureNotePending(t *testing.T) {
	pending := []kernelReconcileFailure{{KernelType: kernel.TypeSingbox, Err: errApplyConfirmPending}}
	if got := whitelistReconcileFailureNote(pending); got != "白名单已保存，下发已发出，正在确认生效状态（内核「singbox」白名单下发已发出，正在确认生效状态）" {
		t.Fatalf("pending-only note = %q", got)
	}
	if strings.Contains(whitelistReconcileFailureNote(pending), "下发失败") {
		t.Fatal("pending note must never say 下发失败")
	}
	// 真失败口径不变。
	failed := []kernelReconcileFailure{{KernelType: kernel.TypeSingbox, Err: errors.New("agent 拒收")}}
	if got := whitelistReconcileFailureNote(failed); got != "白名单已保存，但部分内核下发失败（内核「singbox」白名单下发失败: agent 拒收），请检查节点内核状态" {
		t.Fatalf("failure-only note = %q", got)
	}
	// 混合：两段都点名。
	mixed := append(append([]kernelReconcileFailure{}, failed...), pending...)
	if got := whitelistReconcileFailureNote(mixed); got != "白名单已保存，但部分内核下发失败（内核「singbox」白名单下发失败: agent 拒收），部分内核下发已发出，正在确认生效状态（内核「singbox」白名单下发已发出，正在确认生效状态），请检查节点内核状态" {
		t.Fatalf("mixed note = %q", got)
	}
	// 离线/空错仍跳过；无内核类型 pending 仍渲染。
	skipped := []kernelReconcileFailure{
		{KernelType: kernel.TypeSingbox, Err: errors.New("节点不在线")},
		{KernelType: kernel.TypeSingbox, Err: nil},
	}
	if got := whitelistReconcileFailureNote(skipped); got != "" {
		t.Fatalf("offline/nil failures must render empty, got %q", got)
	}
	if got := whitelistReconcileFailureNote([]kernelReconcileFailure{{Err: errApplyConfirmPending}}); got != "白名单已保存，下发已发出，正在确认生效状态（节点内核下发已发出，正在确认生效状态）" {
		t.Fatalf("typeless pending note = %q", got)
	}
}

func TestConnlogReconcileFailureNotePending(t *testing.T) {
	pending := []kernelReconcileFailure{{KernelType: kernel.TypeXray, Err: errApplyConfirmPending}}
	if got := connlogReconcileFailureNote(pending); got != "连接日志开关已保存，下发已发出，正在确认生效状态（内核「xray」连接日志下发已发出，正在确认生效状态）" {
		t.Fatalf("pending-only note = %q", got)
	}
	failed := []kernelReconcileFailure{{KernelType: kernel.TypeXray, Err: errors.New("boom")}}
	if got := connlogReconcileFailureNote(failed); got != "连接日志开关已保存，但部分内核下发失败（内核「xray」连接日志下发失败: boom），请检查节点内核状态" {
		t.Fatalf("failure-only note = %q", got)
	}
}

func TestWhitelistRefreshNotesAndWarning(t *testing.T) {
	if got := whitelistRefreshPendingNote(nil); got != "" {
		t.Fatalf("empty pending note = %q", got)
	}
	if got := whitelistRefreshPendingNote([]int64{7}); got != "白名单刷新已发出，正在确认生效状态（入站 7）" {
		t.Fatalf("pending note = %q", got)
	}
	if strings.Contains(whitelistRefreshPendingNote([]int64{7}), "刷新失败") || strings.Contains(whitelistRefreshPendingNote([]int64{7}), "仍按旧名单") {
		t.Fatal("pending note must not claim failure or stale allow list")
	}
	if got := whitelistRefreshFailureNote(nil); got != "" {
		t.Fatalf("empty failure note = %q", got)
	}
	if got := whitelistRefreshFailureNote([]int64{7}); got != "白名单刷新失败（入站 7），内核仍按旧名单放行" {
		t.Fatalf("failure note = %q", got)
	}
	// 组合：失败带尾缀、待定另起一段。
	if got := whitelistRefreshWarning([]int64{7}, []int64{8}); got != "白名单刷新失败（入站 7），内核仍按旧名单放行，请检查节点内核状态；白名单刷新已发出，正在确认生效状态（入站 8）" {
		t.Fatalf("combined warning = %q", got)
	}
	if got := whitelistRefreshWarning(nil, []int64{8}); got != "白名单刷新已发出，正在确认生效状态（入站 8）" {
		t.Fatalf("pending-only warning = %q", got)
	}
	if got := whitelistRefreshWarning([]int64{7}, nil); got != "白名单刷新失败（入站 7），内核仍按旧名单放行，请检查节点内核状态" {
		t.Fatalf("failure-only warning = %q", got)
	}
	if got := whitelistRefreshWarning(nil, nil); got != "" {
		t.Fatalf("empty warning = %q", got)
	}
	// notes 顺序：失败在前、待定在后。
	notes := whitelistRefreshNotes([]int64{7}, []int64{8})
	if len(notes) != 2 || !strings.Contains(notes[0], "刷新失败") || !strings.Contains(notes[1], "正在确认") {
		t.Fatalf("notes order/content = %v", notes)
	}
	if got := whitelistRefreshNoteForErr(7, errApplyConfirmPending); got != "白名单刷新已发出，正在确认生效状态（入站 7）" {
		t.Fatalf("noteForErr pending = %q", got)
	}
	if got := whitelistRefreshNoteForErr(7, errors.New("boom")); got != "白名单刷新失败（入站 7），内核仍按旧名单放行" {
		t.Fatalf("noteForErr failure = %q", got)
	}
}

// --- 端到端层：真 WS 假 agent 从不回 KernelApply，20s 回执超时
// 后 KernelStatus 回不符 hash → applyKernel 返 pending。各收据
// 面须报 pending、绝不缀「下发失败」。---

func f1SeedVLESS(t *testing.T, r *repo.Repository, k *model.NodeKernel, name string, port int) int64 {
	t.Helper()
	in := seedChainInbound(t, r, k, name, kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	return in.ID
}

func f1PendingHandler(t *testing.T) (*Handler, *repo.Repository, int64) {
	t.Helper()
	return newF2APPLY01Handler(t, func() string { return "not-the-dispatched-hash" })
}

func f1AssertMsg(t *testing.T, body, want string) {
	t.Helper()
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode receipt: %v (body=%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("receipt code = %d, want 0 (body=%s)", env.Code, body)
	}
	if env.Msg != want {
		t.Fatalf("receipt msg = %q, want %q", env.Msg, want)
	}
	if strings.Contains(env.Msg, "下发失败") {
		t.Fatalf("pending receipt must never say 下发失败: %q", env.Msg)
	}
}

func TestF1PendingInboundDeleteReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id := f1SeedVLESS(t, r, k, "f1-del", 24401)
	body := r2b2AuthPost(t, h.inboundDelete, "/api/v1/inbound/delete", map[string]interface{}{"id": id}, "")
	f1AssertMsg(t, body, "已删除，下发已发出，正在确认生效状态")
}

func TestF1PendingInboundToggleReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id := f1SeedVLESS(t, r, k, "f1-toggle", 24402)
	body := r2b2AuthPost(t, h.inboundToggle, "/api/v1/inbound/toggle", map[string]interface{}{"id": id, "enabled": false}, "")
	f1AssertMsg(t, body, "已保存，下发已发出，正在确认生效状态")
}

func TestF1PendingInboundBatchDeleteReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id1 := f1SeedVLESS(t, r, k, "f1-bdel-1", 24403)
	id2 := f1SeedVLESS(t, r, k, "f1-bdel-2", 24404)
	body := r2b2AuthPost(t, h.inboundBatchDelete, "/api/v1/inbound/batch-delete", map[string]interface{}{"ids": []int64{id1, id2}}, "")
	f1AssertMsg(t, body, "已删除，下发已发出，正在确认生效状态")
}

func TestF1PendingInboundBatchToggleReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id1 := f1SeedVLESS(t, r, k, "f1-btog-1", 24405)
	id2 := f1SeedVLESS(t, r, k, "f1-btog-2", 24406)
	body := r2b2AuthPost(t, h.inboundBatchToggle, "/api/v1/inbound/batch-toggle", map[string]interface{}{"ids": []int64{id1, id2}, "enabled": false}, "")
	f1AssertMsg(t, body, "已保存，下发已发出，正在确认生效状态")
}

func TestF1PendingInboundRegenerateReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id := f1SeedVLESS(t, r, k, "f1-regen", 24407)
	body := r2b2AuthPost(t, h.inboundRegenerate, "/api/v1/inbound/regenerate", map[string]interface{}{"id": id}, "")
	f1AssertMsg(t, body, "已重新生成，下发已发出，正在确认生效状态")
}

func TestF1PendingInboundSplitSaveReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	id := f1SeedVLESS(t, r, k, "f1-split", 24408)
	body := r2b2AuthPost(t, h.inboundSplitSave, "/api/v1/inbound/split-save", map[string]interface{}{"id": id, "outboundId": 0, "splitRules": []interface{}{}}, "")
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode receipt: %v (body=%s)", err, body)
	}
	if env.Code != 0 {
		t.Fatalf("receipt code = %d, want 0 (body=%s)", env.Code, body)
	}
	if env.Msg != "已保存，但下发已发出，正在确认生效状态" {
		t.Fatalf("receipt msg = %q, want %q", env.Msg, "已保存，但下发已发出，正在确认生效状态")
	}
	if strings.Contains(env.Msg, "下发失败") {
		t.Fatalf("pending receipt must never say 下发失败: %q", env.Msg)
	}
}

func TestF1PendingChainReferrersReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	target := seedChainInbound(t, r, k, "f1-target", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24409,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	referrer := seedChainInbound(t, r, k, "f1-referrer", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24410,
		Security: kernel.Security{Type: kernel.SecurityNone}}, target.ID)
	_ = referrer
	refs, err := h.chainReferrers(target.ID)
	if err != nil || len(refs) == 0 {
		t.Fatalf("chainReferrers: %v refs=%d", err, len(refs))
	}
	warnings := h.applyChainReferrersFromRefs(refs)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %v", warnings)
	}
	if warnings[0] != "经它出站的「f1-referrer」下发已发出，正在确认生效状态" {
		t.Fatalf("warning = %q", warnings[0])
	}
	if strings.Contains(warnings[0], "下发失败") {
		t.Fatalf("pending warning must never say 下发失败: %q", warnings[0])
	}
}

func TestF1PendingWhitelistedTargetReceipt(t *testing.T) {
	h, r, kID := f1PendingHandler(t)
	k, err := r.GetNodeKernel(kID)
	if err != nil || k == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	target := seedChainInbound(t, r, k, "f1-wl-target", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24411,
		Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
	target.RelayWhitelist = 1
	if err := r.SaveNodeInbound(target); err != nil {
		t.Fatalf("mark whitelisted: %v", err)
	}
	warnings := h.applyWhitelistedTarget(target.ID)
	if len(warnings) != 1 {
		t.Fatalf("want 1 warning, got %v", warnings)
	}
	if warnings[0] != "出站节点「f1-wl-target」的白名单下发已发出，正在确认生效状态" {
		t.Fatalf("warning = %q", warnings[0])
	}
	if strings.Contains(warnings[0], "下发失败") {
		t.Fatalf("pending warning must never say 下发失败: %q", warnings[0])
	}
}

// --- applyKernelsForInbounds 优先级：真失败压过 pending，无论
// 顺序。假 agent 对第 N 次 KernelApply 回 success=false（真
// 失败、立即返回），其余次静默（20s 超时后 pending）。---

func f1PrecedenceHandler(t *testing.T, applyErrorOn int) (*Handler, *repo.Repository, int64, int64) {
	t.Helper()
	r, err := repo.Open(t.TempDir() + "/f1prec.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	const secret = "f1prec-node-secret"
	nodeID := seedRelayTargetNode(t, r, "f1prec-node", "127.0.0.1")
	kA := seedB8Kernel(t, r, nodeID, kernel.TypeSingbox, "1.14.3")
	kB := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "26.11.9")
	for _, kv := range []struct {
		k *model.NodeKernel
		v string
	}{{kA, "sing-box 1.14.3"}, {kB, "xray 26.11.9"}} {
		// InstalledVersion 须含 Version 子串，否则 applyKernel 把
		// 预算升到 300s 安装档（测试只等一轮 20s 回执超时）。
		if err := r.UpdateNodeKernelFields(kv.k.ID, map[string]interface{}{
			"installed_version": kv.v,
		}); err != nil {
			t.Fatal(err)
		}
	}

	server := ws.NewServer(r, "test-jwt-secret")
	online := make(chan struct{})
	server.SetNodeOnlineHook(func(int64) { close(online) })
	serverDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(serverDone)
		server.ServeHTTP(w, req)
	}))
	t.Cleanup(ts.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"?type=1&secret="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	crypto, err := security.NewAESCrypto(secret)
	if err != nil {
		t.Fatal(err)
	}
	applySeen := 0
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var envelope struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Errorf("decode command envelope: %v", err)
				return
			}
			if envelope.Encrypted {
				payload, err = crypto.Decrypt(envelope.Data)
				if err != nil {
					t.Errorf("decrypt command: %v", err)
					return
				}
			}
			var command cleanupAgentCommand
			if err := json.Unmarshal(payload, &command); err != nil {
				t.Errorf("decode command: %v", err)
				return
			}
			if command.Type == "KernelApply" {
				applySeen++
				if applySeen == applyErrorOn {
					resp := map[string]interface{}{
						"type": command.Type, "requestId": command.RequestID,
						"success": false, "message": "agent 拒收",
					}
					if err := conn.WriteJSON(resp); err != nil {
						return
					}
				}
				continue
			}
			resp := map[string]interface{}{
				"type": command.Type, "requestId": command.RequestID,
				"success": true, "message": "",
			}
			if command.Type == "KernelStatus" {
				resp["data"] = map[string]interface{}{
					"kernels": []map[string]interface{}{{
						"type": kernel.TypeSingbox, "running": true, "enabled": true,
						"configHash": "not-the-dispatched-hash", "version": "sing-box 1.14.3",
					}},
				}
			}
			if err := conn.WriteJSON(resp); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-readerDone
		<-serverDone
	})
	select {
	case <-online:
	case <-time.After(3 * time.Second):
		t.Fatal("fake agent did not come online")
	}
	h := New(r, "unit-test-secret")
	h.wsServer = server
	inA := f1SeedVLESS(t, r, kA, "f1-prec-a", 24412)
	inB := f1SeedVLESS(t, r, kB, "f1-prec-b", 24413)
	return h, r, inA, inB
}

func TestF1ApplyKernelsRealFailureBeatsPending(t *testing.T) {
	// 真失败在先：第一次 apply 即拒收、第二次静默 pending——
	// 返回的必须是真失败。
	h, _, inA, inB := f1PrecedenceHandler(t, 1)
	err := h.applyKernelsForInbounds([]int64{inA, inB})
	if err == nil || !strings.Contains(err.Error(), "agent 拒收") {
		t.Fatalf("real failure must win over pending, got %v", err)
	}
	if errors.Is(err, errApplyConfirmPending) {
		t.Fatalf("must not return pending when a real failure exists, got %v", err)
	}
}

func TestF1ApplyKernelsRealFailureBeatsPendingLate(t *testing.T) {
	// 真失败在后：第一次 apply 静默 pending、第二次拒收——
	// 返回的仍必须是真失败。
	h, _, inA, inB := f1PrecedenceHandler(t, 2)
	err := h.applyKernelsForInbounds([]int64{inB, inA})
	if err == nil || !strings.Contains(err.Error(), "agent 拒收") {
		t.Fatalf("real failure must win over pending, got %v", err)
	}
	if errors.Is(err, errApplyConfirmPending) {
		t.Fatalf("must not return pending when a real failure exists, got %v", err)
	}
}

// F-R5-APPLY-01-OBS：干净停机（节点离线）时建入站，回执须说
// 「节点离线，下发待节点重连后自动重推」，绝不缀「下发失败」；
// 内核状态须为 pending（重连后 onNodeOnline 自动重推收敛）。
// 与冻结（SIGSTOP）区分：冻结是「已发出、正在确认」，停机是
// 「未送达、待重连」——两种待定各有其文案，互不冒充。
func TestF1OfflineInboundCreateReceipt(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/f1-offline-create.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	nodeID := seedRelayTargetNode(t, r, "f1-offline-node", "198.51.100.24")
	// 干净停机形态：DB 记在线、agent 无 live 会话（与 F 路真机
	// SIGTERM 观测一致——建入站时 send 直接报「节点不在线」）。
	if err := r.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, nodeID).Error; err != nil {
		t.Fatal(err)
	}
	box := seedChainKernel(t, r, nodeID, kernel.TypeSingbox)
	entry := seedChainInbound(t, r, box, "f1-offline-seed", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	body := inboundRequest{
		NodeID: nodeID, KernelType: kernel.TypeSingbox, Name: "f1-offline-new",
		Protocol: kernel.ProtocolShadowsocks, Settings: old.Settings, Security: old.Security,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, h, false, string(raw))
	if out.Code != 0 {
		t.Fatalf("offline create receipt code = %d, want 0 (body=%+v)", out.Code, out)
	}
	if out.Msg != "已保存，但节点离线，下发待节点重连后自动重推" {
		t.Fatalf("offline create receipt msg = %q, want %q", out.Msg, "已保存，但节点离线，下发待节点重连后自动重推")
	}
	if strings.Contains(out.Msg, "下发失败") {
		t.Fatalf("offline receipt must never say 下发失败: %q", out.Msg)
	}
	after, err := r.GetNodeKernel(box.ID)
	if err != nil || after == nil {
		t.Fatalf("reload kernel: %v", err)
	}
	if after.Status != kernelStatusPending {
		t.Fatalf("offline kernel status = %q, want %q (reconnect converges)", after.Status, kernelStatusPending)
	}
}
