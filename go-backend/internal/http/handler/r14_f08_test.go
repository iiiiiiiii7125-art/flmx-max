package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R14-F08 回归：三项后端 P3（A4-001 / A2-3 / A5-08）。先红后绿——
// 本文件在修复前跑：A4-001 与 A2-3 五格 FAIL；A5-08 保存侧因
// CheckChainWith 默认分支已拒（见 R14-FIX-F08-REPORT §A5-08），
// 其红灯体现为拒绝文案带 "出站代理：" 前缀、与渲染口径逐字不一
// 致；毒化行下发侧 FAIL（整内核下发被毒化行阻塞）。

func seedR14F08Node(t *testing.T, r *repo.Repository) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := r.CreateNode("r14f08-node", "secret", "198.51.100.10", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "xray")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return id
}

func seedR14F08Kernel(t *testing.T, r *repo.Repository, nodeID int64, kernelType, version string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	k := &model.NodeKernel{NodeID: nodeID, KernelType: kernelType, Version: version, Enabled: 1, CreatedTime: now, UpdatedTime: now}
	if err := r.CreateNodeKernel(k); err != nil {
		t.Fatalf("create kernel %s: %v", kernelType, err)
	}
	got, err := r.GetNodeKernelByType(nodeID, kernelType)
	if err != nil || got == nil {
		t.Fatalf("get kernel %s: %v %+v", kernelType, err, got)
	}
	return got.ID
}

func seedR14F08Inbound(t *testing.T, r *repo.Repository, kernelID, nodeID int64, name, protocol string, port int, settings string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	in := &model.NodeInbound{
		KernelID: kernelID, NodeID: nodeID, Name: name, Protocol: protocol,
		Listen: "::", Port: port,
		Transport: `{"type":"tcp"}`, Security: `{"type":"none"}`,
		Settings: settings, Enabled: 1, CreatedTime: now, UpdatedTime: now,
	}
	if err := r.CreateNodeInbound(in); err != nil {
		t.Fatalf("create inbound %s: %v", name, err)
	}
	return in.ID
}

func decodeR14F08Envelope(t *testing.T, res *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, res.Body.String())
	}
	return payload.Code, payload.Msg
}

// A4-001：批量恢复在「数据面已成功、DB 写失败」分支的失败原因必须
// 是真实错误（statusErr），不是恒 nil 的函数级 err（→「未知错误」）。
func TestR14F08BatchResumeDbWriteFailureReportsRealReason(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	manager := &fakeNftablesManager{}
	h.nftablesManager = newSerializedNftManager(manager)
	seedNftablesSSHConfig(t, h, fixture.nodeID)
	tunnelID := seedTunnelForNftables(t, h, "r14f08-tunnel", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20001)

	now := time.Now().UnixMilli()
	if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, now); err != nil {
		t.Fatalf("pause forward: %v", err)
	}
	// 恢复 reconcile 期间并发删除把行标记为 pending（R14-A4 探针同型竞态）：
	// 数据面已成功，随后 ActivateForwardIfNotPending 返回哨兵错误（statusErr）。
	manager.reconcileHook = func() {
		if err := h.repo.MarkForwardPendingDelete(forward.ID, fixture.nodeID, time.Now()); err != nil {
			t.Errorf("mark pending delete in reconcile hook: %v", err)
		}
	}

	req := newAuthenticatedJSONRequest(t, map[string]any{"ids": []int64{forward.ID}})
	res := httptest.NewRecorder()
	h.forwardBatchResume(res, req)

	var payload struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount int `json:"successCount"`
			FailCount    int `json:"failCount"`
			Failures     []struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			} `json:"failures"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, res.Body.String())
	}
	if payload.Code != 0 {
		t.Fatalf("batch resume envelope code=%d body=%s", payload.Code, res.Body.String())
	}
	if payload.Data.SuccessCount != 0 || payload.Data.FailCount != 1 || len(payload.Data.Failures) != 1 {
		t.Fatalf("counts wrong: success=%d fail=%d failures=%v body=%s",
			payload.Data.SuccessCount, payload.Data.FailCount, payload.Data.Failures, res.Body.String())
	}
	if got := payload.Data.Failures[0].Reason; got != "forward is pending delete" {
		t.Fatalf("failure reason = %q, want %q (statusErr must be reported, not nil err)",
			got, "forward is pending delete")
	}
}

// A2-3（BC）：login / backupExport / updateAnnouncement 五处参数错误
// code=500 改为 ErrDefault（-1），与全仓 697 处口径一致。
func TestR14F08ParamErrorsUseErrDefault(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r14f08-a23.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")

	post := func(fn func(http.ResponseWriter, *http.Request), body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		res := httptest.NewRecorder()
		fn(res, req)
		return decodeR14F08Envelope(t, res)
	}

	cases := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		body string
	}{
		{"login 非法 JSON", h.login, `not json`},
		{"login 空用户名", h.login, `{"username":"","password":"x"}`},
		{"login 空密码", h.login, `{"username":"admin","password":""}`},
		{"backupExport 非法 JSON", h.backupExport, `not json`},
		{"updateAnnouncement 非法 JSON", h.updateAnnouncement, `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := post(tc.fn, tc.body)
			if code != -1 {
				t.Fatalf("code=%d msg=%q, want code=-1 (ErrDefault)", code, msg)
			}
		})
	}
}

// A5-08 保存侧：Snell+Via 拒绝文案须与渲染口径逐字一致
// （render_snell.go:26「Snell 内核不支持链式出站」，无前缀）。
func TestR14F08ValidateChainVersionRejectsSnellVia(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r14f08-a508-chain.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedR14F08Node(t, r)
	xrayK := seedR14F08Kernel(t, r, nodeID, "xray", "25.9.5")
	target := seedR14F08Inbound(t, r, xrayK, nodeID, "xray-target", "vless", 24443,
		`{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`)

	row := model.NodeInbound{NodeID: nodeID, OutboundID: target}
	if err := h.validateChainVersion(row, kernel.TypeSnell, "6.0.0"); err == nil || err.Error() != "Snell 内核不支持链式出站" {
		t.Fatalf("snell+via err = %v, want exactly %q", err, "Snell 内核不支持链式出站")
	}
	// 对照：同目标在 Xray 内核上合法。
	if err := h.validateChainVersion(row, kernel.TypeXray, "25.9.5"); err != nil {
		t.Fatalf("xray+via must pass, got %v", err)
	}
	// 对照：Snell+分流（direct 规则，不走 proxy 目标校验）仍拒。
	rowSplit := model.NodeInbound{NodeID: nodeID, SplitRules: `[{"action":"direct","domains":["example.com"]}]`}
	if err := h.validateChainVersion(rowSplit, kernel.TypeSnell, "6.0.0"); err == nil || err.Error() != "Snell 内核不支持分流" {
		t.Fatalf("snell+split err = %v, want exactly %q", err, "Snell 内核不支持分流")
	}
}

// A5-08 保存侧（HTTP 线）：/inbound/split 设 Snell+Via 应明确拒绝且不落库。
func TestR14F08SnellViaSplitSaveRejected(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r14f08-a508-split.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedR14F08Node(t, r)
	snellK := seedR14F08Kernel(t, r, nodeID, "snell", "6.0.0")
	xrayK := seedR14F08Kernel(t, r, nodeID, "xray", "25.9.5")
	target := seedR14F08Inbound(t, r, xrayK, nodeID, "xray-target", "vless", 24443,
		`{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`)
	snellIn := seedR14F08Inbound(t, r, snellK, nodeID, "snell-in", "snell", 20001,
		`{"password":"test-psk-123456","snellVersion":6}`)

	req := newAuthenticatedJSONRequest(t, map[string]any{"id": snellIn, "outboundId": target})
	res := httptest.NewRecorder()
	h.inboundSplitSave(res, req)
	code, msg := decodeR14F08Envelope(t, res)
	if code == 0 {
		t.Fatalf("snell+via split save must be rejected, got code=0 body=%s", res.Body.String())
	}
	if msg != "Snell 内核不支持链式出站" {
		t.Fatalf("msg = %q, want exactly %q", msg, "Snell 内核不支持链式出站")
	}
	row, err := h.repo.GetNodeInbound(snellIn)
	if err != nil || row == nil {
		t.Fatalf("get snell inbound: %v %+v", err, row)
	}
	if row.OutboundID != 0 {
		t.Fatalf("rejected save must not land: outbound_id=%d", row.OutboundID)
	}

	// 对照：同目标在 Xray 入站上可保存（下发离线只告警，code 仍为 0）。
	xrayIn := seedR14F08Inbound(t, r, xrayK, nodeID, "xray-in", "vless", 24444,
		`{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`)
	req2 := newAuthenticatedJSONRequest(t, map[string]any{"id": xrayIn, "outboundId": target})
	res2 := httptest.NewRecorder()
	h.inboundSplitSave(res2, req2)
	code2, _ := decodeR14F08Envelope(t, res2)
	if code2 != 0 {
		t.Fatalf("xray+via split save must be accepted, got code=%d body=%s", code2, res2.Body.String())
	}
	row2, err := h.repo.GetNodeInbound(xrayIn)
	if err != nil || row2 == nil || row2.OutboundID != target {
		t.Fatalf("xray+via must land: row=%+v err=%v", row2, err)
	}
}

// A5-08 毒化行：已落库的 Snell+Via 行不得阻塞整内核后续下发。
// 下发入口（applyKernelWith）在渲染前先行清理毒化行（改直连、记日志），
// 毒化行存在时后续下发仍能继续；清理后再次构建即成功。
func TestR14F08SnellPoisonedRowHealedOnApply(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/r14f08-a508-heal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "test-secret")
	nodeID := seedR14F08Node(t, r)
	snellK := seedR14F08Kernel(t, r, nodeID, "snell", "6.0.0")
	xrayK := seedR14F08Kernel(t, r, nodeID, "xray", "25.9.5")
	target := seedR14F08Inbound(t, r, xrayK, nodeID, "xray-target", "vless", 24443,
		`{"uuid":"0b6e4c38-8f0e-4a3e-9d2d-7a1c2b3c4d5e"}`)
	// 毒化行：绕过保存校验直接落库（模拟备份导入/库外写入的历史毒化行）。
	poisoned := seedR14F08Inbound(t, r, snellK, nodeID, "snell-poisoned", "snell", 20001,
		`{"password":"test-psk-123456","snellVersion":6}`)
	poisonedRow, err := h.repo.GetNodeInbound(poisoned)
	if err != nil || poisonedRow == nil {
		t.Fatalf("get poisoned row: %v %+v", err, poisonedRow)
	}
	poisonedRow.OutboundID = target
	if err := h.repo.SaveNodeInbound(poisonedRow); err != nil {
		t.Fatalf("plant poisoned row: %v", err)
	}
	healthy := seedR14F08Inbound(t, r, snellK, nodeID, "snell-healthy", "snell", 20002,
		`{"password":"test-psk-abcdef","snellVersion":6}`)

	k, err := h.repo.GetNodeKernel(snellK)
	if err != nil || k == nil {
		t.Fatalf("get snell kernel: %v %+v", err, k)
	}
	// 1) 毒化行存在时，构建即被渲染拒绝（阻塞整内核下发的复现）。
	if _, _, err := h.buildKernelApplyPayload(k); err == nil || !strings.Contains(err.Error(), "Snell 内核不支持链式出站") {
		t.Fatalf("poisoned build err = %v, want Snell render rejection", err)
	}
	// 2) 下发入口先清理毒化行再下发：本次因节点离线推不出去是预期的，
	// 但错误不得再是 Snell 毒化错误（清理已发生）。
	applyErr := h.applyKernelWith(snellK, false, nil)
	if applyErr == nil {
		t.Fatalf("expected offline error from apply (node has no live agent), got nil")
	}
	if strings.Contains(applyErr.Error(), "Snell") {
		t.Fatalf("poisoned row still blocks deploy: %v", applyErr)
	}
	// 3) 库中毒化行已被清理为直连。
	healed, err := h.repo.GetNodeInbound(poisoned)
	if err != nil || healed == nil {
		t.Fatalf("get healed row: %v %+v", err, healed)
	}
	if healed.OutboundID != 0 || strings.TrimSpace(healed.SplitRules) != "" {
		t.Fatalf("poisoned row not healed: outbound_id=%d split_rules=%q", healed.OutboundID, healed.SplitRules)
	}
	// 4) 后续下发继续：再次构建成功，且两入站都在端口表里。
	payload, hash, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatalf("build after heal: %v", err)
	}
	if hash == "" {
		t.Fatalf("build after heal: empty hash")
	}
	ports, ok := payload["ports"].([]map[string]interface{})
	if !ok {
		t.Fatalf("payload ports shape unexpected: %T", payload["ports"])
	}
	wantPorts := map[int]bool{20001: true, 20002: true}
	for _, p := range ports {
		if pn, ok := p["port"].(int); ok {
			delete(wantPorts, pn)
		}
	}
	if len(wantPorts) != 0 {
		t.Fatalf("ports after heal missing %v (healthy inbound id=%d): payload ports=%v", wantPorts, healthy, ports)
	}
}
