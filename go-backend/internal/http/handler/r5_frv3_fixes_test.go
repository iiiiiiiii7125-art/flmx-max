package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// Round 5 回头看 R-REV3 修正（FRV3）的回归测试：
//   - RV3-01：流式转发诊断与非流式同口径——nft 转发先经共用
//     prepare 层（prepareForwardDiagnosisPlan）出规则状态项、
//     applied 后走真实探测，不再直走 prepareForwardDiagnosis
//     绕过 nft 分支（规则缺失时流式面曾只剩与规则无关的直拨
//     探测、可全绿，与非流式结论矛盾）。
//   - RV3-02 的测试在 r5_fe_fixes_test.go 的
//     TestR5FEL9Type1DetachedExitPortLifecycle（③ 段改回拒绝断言
//     ＋①b 创建路撞口）。

type frv3StreamEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type frv3StreamItemData struct {
	Index    int                    `json:"index"`
	Result   map[string]interface{} `json:"result"`
	Progress diagnosisProgress      `json:"progress"`
}

func frv3ParseStreamEvents(t *testing.T, body string) []frv3StreamEvent {
	t.Helper()
	var events []frv3StreamEvent
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev frv3StreamEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse stream line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func frv3RunForwardStream(t *testing.T, h *Handler, plan *forwardDiagnosisPlan) []frv3StreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), diagnosisRequestTimeout)
	defer cancel()
	rec := httptest.NewRecorder()
	if err := h.streamForwardDiagnosisRuntime(ctx, cancel, rec, map[string]interface{}{"forwardName": plan.name}, plan); err != nil {
		t.Fatalf("stream forward diagnosis: %v", err)
	}
	return frv3ParseStreamEvents(t, rec.Body.String())
}

func frv3StreamItems(t *testing.T, events []frv3StreamEvent) []frv3StreamItemData {
	t.Helper()
	var items []frv3StreamItemData
	for _, ev := range events {
		if ev.Type != "item" {
			continue
		}
		var data frv3StreamItemData
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("parse item event: %v", err)
		}
		items = append(items, data)
	}
	return items
}

func frv3StreamStart(t *testing.T, events []frv3StreamEvent) (int, []map[string]interface{}) {
	t.Helper()
	for _, ev := range events {
		if ev.Type != "start" {
			continue
		}
		var data struct {
			Total int                      `json:"total"`
			Items []map[string]interface{} `json:"items"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("parse start event: %v", err)
		}
		return data.Total, data.Items
	}
	t.Fatalf("no start event in stream: %#v", events)
	return 0, nil
}

func frv3StreamDone(t *testing.T, events []frv3StreamEvent) diagnosisProgress {
	t.Helper()
	for _, ev := range events {
		if ev.Type != "done" {
			continue
		}
		var data struct {
			Progress diagnosisProgress `json:"progress"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			t.Fatalf("parse done event: %v", err)
		}
		return data.Progress
	}
	t.Fatalf("no done event in stream: %#v", events)
	return diagnosisProgress{}
}

// ---- RV3-01：nft 规则未下发——流式面出状态失败项而非探测全绿 ----

func TestRV3NftStreamDiagnosisMissingBindingEmitsStatusItem(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "nft-stream-missing", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	// 不种任何 binding：规则未下发。

	plan, err := h.prepareForwardDiagnosisPlan(forward)
	if err != nil {
		t.Fatalf("prepare plan: %v", err)
	}
	if len(plan.workItems) != 0 {
		t.Fatalf("RV3-01: nft forward with missing rule must not fall back to gost probe items, got %d", len(plan.workItems))
	}
	if len(plan.statusItems) != 1 {
		t.Fatalf("expected exactly one rule-status item, got %#v", plan.statusItems)
	}
	statusItem := plan.statusItems[0]
	if statusItem["success"] != false || statusItem["nftRuleStatus"] != "missing" {
		t.Fatalf("status item must be a missing-rule failure, got %#v", statusItem)
	}

	events := frv3RunForwardStream(t, h, plan)
	total, startItems := frv3StreamStart(t, events)
	if total != 1 || len(startItems) != 1 {
		t.Fatalf("start must announce the single status item, got total=%d items=%#v", total, startItems)
	}
	// 占位项与结果项同标识（前端按四元组对位替换；JSON 回读的
	// 数值是 float64，按值归一比较）。
	for _, key := range []string{"description", "nodeId", "targetIp"} {
		if asString(startItems[0][key]) != asString(statusItem[key]) {
			t.Fatalf("start placeholder %s must match the status item, got %v vs %v", key, startItems[0][key], statusItem[key])
		}
	}
	if asInt(startItems[0]["targetPort"], 0) != asInt(statusItem["targetPort"], 0) {
		t.Fatalf("start placeholder targetPort must match the status item, got %v vs %v", startItems[0]["targetPort"], statusItem["targetPort"])
	}
	items := frv3StreamItems(t, events)
	if len(items) != 1 {
		t.Fatalf("expected exactly one streamed item, got %d", len(items))
	}
	got := items[0]
	if got.Index != 0 || got.Result["success"] != false || got.Result["nftRuleStatus"] != "missing" {
		t.Fatalf("streamed item must be the missing-rule failure, got %#v", got)
	}
	if !strings.Contains(asString(got.Result["message"]), "未下发") {
		t.Fatalf("streamed status item must carry the rule-state reason, got %#v", got.Result["message"])
	}
	if got.Progress.Total != 1 || got.Progress.Completed != 1 || got.Progress.Failed != 1 || got.Progress.Success != 0 {
		t.Fatalf("streamed progress must count the status item, got %+v", got.Progress)
	}
	done := frv3StreamDone(t, events)
	if done.Total != 1 || done.Completed != 1 || done.Failed != 1 || done.Success != 0 {
		t.Fatalf("done progress must match, got %+v", done)
	}

	// 与非流式面对同一转发同口径：结果集逐项一致。
	payload, err := h.diagnoseForwardRuntime(context.Background(), forward)
	if err != nil {
		t.Fatalf("diagnose forward (non-stream): %v", err)
	}
	results, ok := payload["results"].([]map[string]interface{})
	if !ok || len(results) != 1 {
		t.Fatalf("non-stream results shape: %#v", payload["results"])
	}
	for _, key := range []string{"success", "nftRuleStatus", "message", "description"} {
		if results[0][key] != got.Result[key] {
			t.Fatalf("stream/non-stream divergence on %s: %v vs %v", key, results[0][key], got.Result[key])
		}
	}
}

// ---- RV3-01：nft 规则 applied——流式面含真实探测结果（装饰生效） ----

func TestRV3NftStreamDiagnosisAppliedIncludesProbeResults(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "nft-stream-applied", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	now := time.Now().UnixMilli()
	if err := h.repo.UpsertNftRuleBinding(repo.NftRuleBindingInput{
		ForwardID: forward.ID, NodeID: fixture.nodeID, InPort: 20000,
		Protocols: "tcp,udp", TargetAddr: "203.0.113.9:8080", RuleHash: "hash-a",
		Status: runtimenft.StatusApplied,
	}, now); err != nil {
		t.Fatalf("seed nft binding: %v", err)
	}

	orig := tcpPingSend
	tcpPingSend = func(*Handler, int64, interface{}, time.Duration) (ws.CommandResult, error) {
		return ws.CommandResult{Success: true, Data: map[string]interface{}{
			"success": true, "averageTime": 12.5, "packetLoss": 0.0, "message": "连接成功",
		}}, nil
	}
	t.Cleanup(func() { tcpPingSend = orig })

	plan, err := h.prepareForwardDiagnosisPlan(forward)
	if err != nil {
		t.Fatalf("prepare plan: %v", err)
	}
	if len(plan.statusItems) != 0 || len(plan.workItems) != 2 {
		t.Fatalf("applied binding must plan tcp+udp probes and no status items, got status=%d probes=%d", len(plan.statusItems), len(plan.workItems))
	}

	events := frv3RunForwardStream(t, h, plan)
	total, startItems := frv3StreamStart(t, events)
	if total != 2 || len(startItems) != 2 {
		t.Fatalf("start must announce both probe items, got total=%d items=%#v", total, startItems)
	}
	items := frv3StreamItems(t, events)
	if len(items) != 2 {
		t.Fatalf("expected tcp+udp streamed probe items, got %d", len(items))
	}
	var tcpItem *frv3StreamItemData
	for i := range items {
		if items[i].Result["protocol"] == "tcp" {
			tcpItem = &items[i]
		}
		if items[i].Result["nftRuleStatus"] != runtimenft.StatusApplied || items[i].Result["forwardMode"] != "nftables" {
			t.Fatalf("streamed probe items must carry nft fields (decorate), got %#v", items[i].Result)
		}
	}
	if tcpItem == nil {
		t.Fatalf("tcp probe item missing: %#v", items)
	}
	if tcpItem.Result["success"] != true {
		t.Fatalf("probe success must surface in the stream, got %#v", tcpItem.Result)
	}
	if got := asFloat(tcpItem.Result["averageTime"], -1); got != 12.5 {
		t.Fatalf("averageTime must come from the probe (12.5), got %v", got)
	}
	if !strings.Contains(asString(tcpItem.Result["message"]), "nftables 规则已下发") {
		t.Fatalf("streamed probe message must carry the rule-state prefix, got %#v", tcpItem.Result["message"])
	}
	done := frv3StreamDone(t, events)
	if done.Total != 2 || done.Completed != 2 {
		t.Fatalf("done progress must cover both probes, got %+v", done)
	}
}

// ---- RV3-01：非 nft 转发流式行为不变（无 status 项、走 gost 探测腿） ----

func TestRV3NonNftStreamDiagnosisUnchanged(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	now := time.Now().UnixMilli()
	gostNodeID, err := r.CreateNode("gost-node", "secret", "198.51.100.12", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("create gost node: %v", err)
	}
	tunnelID := seedTunnelForNftables(t, h, "gost-stream-tunnel", gostNodeID)
	forward := seedForwardForNftables(t, h, tunnelID, gostNodeID, "203.0.113.9:8080", 20001)

	plan, err := h.prepareForwardDiagnosisPlan(forward)
	if err != nil {
		t.Fatalf("prepare plan: %v", err)
	}
	if len(plan.statusItems) != 0 {
		t.Fatalf("non-nft forward must have no status items, got %#v", plan.statusItems)
	}
	if len(plan.workItems) != 2 {
		t.Fatalf("non-nft type-1 forward must plan tcp+udp target legs, got %d", len(plan.workItems))
	}

	events := frv3RunForwardStream(t, h, plan)
	items := frv3StreamItems(t, events)
	if len(items) != 2 {
		t.Fatalf("expected two streamed probe items, got %d", len(items))
	}
	for _, item := range items {
		if _, ok := item.Result["forwardMode"]; ok {
			t.Fatalf("non-nft streamed items must not carry nft fields, got %#v", item.Result)
		}
	}
	done := frv3StreamDone(t, events)
	if done.Total != 2 || done.Completed != 2 {
		t.Fatalf("done progress must cover both legs, got %+v", done)
	}
}
