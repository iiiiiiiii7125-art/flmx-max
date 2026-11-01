package handler

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

// Round 5 L4/L9 修复簇（FE）的回归测试：诊断真实性与覆盖面
// （L4-04/05/06/07）、中转创建 fail-closed（L4-02）、删节点唯一
// 入口/出口点名（L4-03）、订阅计量上限守卫（L4-08）、隧道游离段
// 口沿用与回执（L9 观察 1）、撞口回执按来源措辞（L9 观察 2）。

func openHandlerRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "r5-fe.sqlite"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// ---- L4-02：createRelayForward 节点记录读失败必须 fail-closed ----

// 哨兵式回归（与 port_occupancy_sentinel_test 同传统）：该分叉是
// 内联校验循环、无可注入的读失败缝，锁定源码形态——getNodeRecord
// 出错分支必须 return 0, nodeErr，不得再出现 continue 跳过校验。
func TestR5FEL4RelayForwardNodeReadFailClosedSentinel(t *testing.T) {
	src, err := os.ReadFile("kernel_relay.go")
	if err != nil {
		t.Fatalf("read kernel_relay.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (h *Handler) createRelayForward")
	if start < 0 {
		t.Fatalf("createRelayForward not found")
	}
	body = body[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	loop := strings.Index(body, "node, nodeErr := h.getNodeRecord(nodeID)")
	if loop < 0 {
		t.Fatalf("validation loop getNodeRecord call not found")
	}
	branch := body[loop:]
	if idx := strings.Index(branch, "validateRemoteNodePort"); idx > 0 {
		branch = branch[:idx]
	}
	// 剥注释行后判定（注释里出现 continue 一词不算代码分支）。
	var code strings.Builder
	for _, line := range strings.Split(branch, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		code.WriteString(line)
		code.WriteString("\n")
	}
	if strings.Contains(code.String(), "continue") {
		t.Fatalf("L4-02 regression: createRelayForward still skips port validation on node read failure:\n%s", branch)
	}
	if !strings.Contains(code.String(), "return 0, nodeErr") {
		t.Fatalf("L4-02: createRelayForward must fail closed with return 0, nodeErr, got:\n%s", branch)
	}
}

// ---- L4-04：nft applied 后果断真实探测——探测成功才报成功，数值来自探测 ----

func TestR5FEL4NftDiagnosisSuccessComesFromProbe(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	tunnelID := seedTunnelForNftables(t, h, "nft-diag-probe-tunnel", fixture.nodeID)
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

	payload, err := h.diagnoseForwardRuntime(context.Background(), &forwardRecord{
		ID: forward.ID, Name: forward.Name, TunnelID: tunnelID, RemoteAddr: "203.0.113.9:8080",
	})
	if err != nil {
		t.Fatalf("diagnose forward: %v", err)
	}
	results, ok := payload["results"].([]map[string]interface{})
	if !ok || len(results) != 2 {
		t.Fatalf("expected tcp+udp probe results, got %#v", payload["results"])
	}
	var tcpItem map[string]interface{}
	for _, item := range results {
		if item["protocol"] == "tcp" {
			tcpItem = item
		}
	}
	if tcpItem == nil {
		t.Fatalf("tcp probe result missing: %#v", results)
	}
	if tcpItem["success"] != true {
		t.Fatalf("probe success must surface as success, got %#v", tcpItem)
	}
	if got := asFloat(tcpItem["averageTime"], -1); got != 12.5 {
		t.Fatalf("averageTime must come from the probe (12.5), got %v in %#v", got, tcpItem)
	}
	if tcpItem["nftRuleStatus"] != runtimenft.StatusApplied {
		t.Fatalf("rule status must ride along, got %#v", tcpItem)
	}
}

// ---- L4-04/L4-05：binding 缺失报失败状态项；多入口逐个覆盖 ----

func TestR5FEL4NftDiagnosisMissingBindingAndMultiEntry(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	now := time.Now().UnixMilli()

	// 第二个入口节点（同为 nftables 模式）。
	if _, err := r.CreateNode("nft-node-2", "secret", "198.51.100.11", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "nftables"); err != nil {
		t.Fatalf("create node2: %v", err)
	}
	node2, err := r.GetNodeRecord(2)
	if err != nil || node2 == nil {
		t.Fatalf("get node2: %v", err)
	}

	tunnelID := seedTunnelForNftables(t, h, "nft-diag-multi", fixture.nodeID)
	// 给隧道加第二个入口行。
	tx := r.BeginTx()
	if err := r.CreateChainTunnelTx(tx, tunnelID, "1", node2.ID, sql.NullInt64{}, "", 2, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("add entry chain row: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit chain row: %v", err)
	}
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	// 仅 node2 有 applied binding；node1 无 binding。
	if err := r.UpsertNftRuleBinding(repo.NftRuleBindingInput{
		ForwardID: forward.ID, NodeID: node2.ID, InPort: 20000,
		Protocols: "tcp,udp", TargetAddr: "203.0.113.9:8080", RuleHash: "hash-b",
		Status: runtimenft.StatusApplied,
	}, now); err != nil {
		t.Fatalf("seed node2 binding: %v", err)
	}

	payload, err := h.diagnoseForwardRuntime(context.Background(), &forwardRecord{
		ID: forward.ID, Name: forward.Name, TunnelID: tunnelID, RemoteAddr: "203.0.113.9:8080",
	})
	if err != nil {
		t.Fatalf("diagnose forward: %v", err)
	}
	results, ok := payload["results"].([]map[string]interface{})
	if !ok {
		t.Fatalf("results shape: %#v", payload["results"])
	}
	byNode := map[string][]map[string]interface{}{}
	for _, item := range results {
		byNode[asString(item["nodeId"])] = append(byNode[asString(item["nodeId"])], item)
	}
	node1Items := byNode["1"]
	if len(node1Items) != 1 {
		t.Fatalf("L4-05: entry node1 must be diagnosed too, got %#v", results)
	}
	if node1Items[0]["success"] != false || node1Items[0]["nftRuleStatus"] != "missing" {
		t.Fatalf("missing binding must be a failed status item, got %#v", node1Items[0])
	}
	if !strings.Contains(asString(node1Items[0]["message"]), "未下发") {
		t.Fatalf("missing binding message must say 未下发, got %#v", node1Items[0]["message"])
	}
	node2Items := byNode["2"]
	if len(node2Items) != 2 {
		t.Fatalf("applied entry node2 must yield tcp+udp probe items, got %#v", node2Items)
	}
	for _, item := range node2Items {
		if item["nftRuleStatus"] != runtimenft.StatusApplied || item["forwardMode"] != "nftables" {
			t.Fatalf("node2 probe items must carry nft fields, got %#v", item)
		}
	}
}

// ---- L4-06：gost 目标腿双协议探测 ----

func TestR5FEL4ForwardDiagnosisTargetLegProbesBothProtocols(t *testing.T) {
	fixture := setupNftablesHandler(t)
	h := fixture.handler
	r := h.repo
	now := time.Now().UnixMilli()

	// type-1：入口→目标腿。
	tunnelID := seedTunnelForNftables(t, h, "diag-t1", fixture.nodeID)
	forward := seedForwardForNftables(t, h, tunnelID, fixture.nodeID, "203.0.113.9:8080", 20000)
	_, items, err := h.prepareForwardDiagnosis(forward)
	if err != nil {
		t.Fatalf("prepare type1 diagnosis: %v", err)
	}
	protos := map[string]bool{}
	for _, item := range items {
		protos[item.protocol] = true
		if !strings.HasSuffix(item.description, "· "+strings.ToUpper(item.protocol)) {
			t.Fatalf("target leg description must name its protocol, got %q", item.description)
		}
	}
	if len(items) != 2 || !protos["tcp"] || !protos["udp"] {
		t.Fatalf("L4-06: type1 target leg must probe tcp+udp, got %#v", items)
	}

	// type-2：出口→目标腿同口径。
	tx := r.BeginTx()
	tunnel2ID, err := r.CreateTunnelTx(tx, "diag-t2", 2, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create type2 tunnel: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnel2ID, "1", fixture.nodeID, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain entry: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnel2ID, "3", fixture.nodeID, sql.NullInt64{Int64: 21000, Valid: true}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain exit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit type2: %v", err)
	}
	forward2 := seedForwardForNftables(t, h, tunnel2ID, fixture.nodeID, "203.0.113.9:8080", 20001)
	_, items2, err := h.prepareForwardDiagnosis(forward2)
	if err != nil {
		t.Fatalf("prepare type2 diagnosis: %v", err)
	}
	targetLegProtos := map[string]bool{}
	for _, item := range items2 {
		if item.hasChainHop {
			continue
		}
		targetLegProtos[item.protocol] = true
	}
	if !targetLegProtos["tcp"] || !targetLegProtos["udp"] {
		t.Fatalf("L4-06: type2 exit->target leg must probe tcp+udp, got %#v", items2)
	}
}

// ---- L4-07：链路腿探测口只认真实监听口 ----

func TestR5FEL4ResolveChainProbeTargetRejectsFabricatedPorts(t *testing.T) {
	from := &nodeRecord{ID: 1, Name: "from"}
	target := &nodeRecord{ID: 2, Name: "to", PortRange: "20000-29999", ServerIP: "203.0.113.5"}

	host, port, err := resolveChainProbeTarget(from, target, 23456, "", "")
	if err != nil || host != "203.0.113.5" || port != 23456 {
		t.Fatalf("real port must pass through, got host=%q port=%d err=%v", host, port, err)
	}
	// 旧行为：port≤0 回退范围首口（20000）、再回退 443——都与真实
	// 监听无关。新行为：显式报错，调用方呈现为该腿失败＋原因。
	if _, _, err := resolveChainProbeTarget(from, target, 0, "", ""); err == nil {
		t.Fatalf("L4-07: portless chain hop must not be probed at a fabricated port")
	}
}

// ---- L9 观察 2：撞口回执按占用来源措辞 ----

func TestR5FEL9ConflictMessageBySource(t *testing.T) {
	h := &Handler{repo: openHandlerRepo(t)}
	node := &nodeRecord{ID: 9, Name: "test-node"}
	now := time.Now().UnixMilli()
	if err := h.repo.CreateNodeInbound(&model.NodeInbound{
		KernelID: 1, NodeID: 9, Name: "vless-in", Protocol: "vless",
		Port: 23456, CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	err := h.validateForwardPortAvailability(node, 23456, 0)
	if err == nil {
		t.Fatalf("expected occupancy error")
	}
	if !strings.Contains(err.Error(), "已被入站") || strings.Contains(err.Error(), "其他转发") {
		t.Fatalf("L9 obs2: inbound occupancy must be attributed to the inbound, got %q", err.Error())
	}

	// 纯函数面：四源措辞逐一核对。
	cases := []struct {
		source string
		want   string
	}{
		{repo.PortSourceForward, "已被其他转发占用"},
		{repo.PortSourceInbound, "已被入站"},
		{repo.PortSourceChain, "链路段占用"},
		{repo.PortSourcePeer, "联邦共享预留占用"},
	}
	for _, c := range cases {
		msg := forwardPortConflictMessage(node, 1000, &repo.ForwardPortConflict{Source: c.source, Name: "x"})
		if !strings.Contains(msg, c.want) {
			t.Fatalf("source %s: want %q in %q", c.source, c.want, msg)
		}
	}
}

// ---- L4-03：删节点点名唯一入口/出口 ----

func TestR5FEL4DeleteNodeNamesSoleEntryExit(t *testing.T) {
	r := openHandlerRepo(t)
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	mkNode := func(name, ip string) int64 {
		id, err := r.CreateNode(name, "secret", ip, nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "")
		if err != nil {
			t.Fatalf("create node %s: %v", name, err)
		}
		return id
	}
	entryNode := mkNode("entry-only", "198.51.100.21")
	exitNode := mkNode("exit-only", "198.51.100.22")

	tx := r.BeginTx()
	tunnelID, err := r.CreateTunnelTx(tx, "sole-tunnel", 2, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create tunnel: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnelID, "1", entryNode, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain entry: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnelID, "3", exitNode, sql.NullInt64{Int64: 21000, Valid: true}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain exit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit tunnel: %v", err)
	}

	sole, err := r.ListSoleSegmentTunnelsForNode(entryNode)
	if err != nil || len(sole) != 1 || sole[0].TunnelID != tunnelID || sole[0].ChainType != 1 {
		t.Fatalf("entry node must be sole entry of the tunnel, got %+v err=%v", sole, err)
	}
	sole, err = r.ListSoleSegmentTunnelsForNode(exitNode)
	if err != nil || len(sole) != 1 || sole[0].ChainType != 3 {
		t.Fatalf("exit node must be sole exit of the tunnel, got %+v err=%v", sole, err)
	}

	_, warnings, err := h.deleteNodeByID(entryNode)
	if err != nil {
		t.Fatalf("deleteNodeByID: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "sole-tunnel") || !strings.Contains(warnings[0], "唯一入口") {
		t.Fatalf("L4-03: delete receipt must name the orphaned tunnel and role, got %v", warnings)
	}
}

// ---- L9 观察 1：type-1 游离出口段口——缺省沿用、被占重挑＋记录、显式撞口拒绝 ----

func seedType1TunnelWithExit(t *testing.T, h *Handler, nodeID int64, exitPort int) int64 {
	t.Helper()
	r := h.repo
	now := time.Now().UnixMilli()
	tx := r.BeginTx()
	tunnelID, err := r.CreateTunnelTx(tx, "t1-detached", 1, 1, 1, now, 1, nil, 1, "", "", 0)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create tunnel: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnelID, "1", nodeID, sql.NullInt64{}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain entry: %v", err)
	}
	if err := r.CreateChainTunnelTx(tx, tunnelID, "3", nodeID, sql.NullInt64{Int64: int64(exitPort), Valid: true}, "", 1, "tls", ""); err != nil {
		_ = tx.Rollback()
		t.Fatalf("chain exit: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit tunnel: %v", err)
	}
	return tunnelID
}

func TestR5FEL9Type1DetachedExitPortLifecycle(t *testing.T) {
	r := openHandlerRepo(t)
	h := New(r, "test-secret")
	now := time.Now().UnixMilli()
	nodeID, err := r.CreateNode("t1-node", "secret", "198.51.100.31", nil, nil, "1000-65535", nil, nil, nil, nil, nil, 0, 0, 0, now, 1, "", "", 1, 0, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	tunnelID := seedType1TunnelWithExit(t, h, nodeID, 20282)

	newReq := func() map[string]interface{} {
		return map[string]interface{}{
			"inNodeId":  []interface{}{map[string]interface{}{"nodeId": nodeID}},
			"outNodeId": []interface{}{map[string]interface{}{"nodeId": nodeID}},
		}
	}
	prepare := func(req map[string]interface{}) *tunnelCreateState {
		tx := r.BeginTx()
		defer func() { _ = tx.Rollback() }()
		state, err := h.prepareTunnelCreateState(tx, req, 1, tunnelID)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		return state
	}

	// ① 省略 port：沿用现口 20282，无 PortChanges（旧行为在落库兜底
	// 随机重挑且无记录）。
	req := newReq()
	state := prepare(req)
	key := tunnelChainPortKey{chainType: 3, inx: 1, nodeID: nodeID}
	if got := state.SegmentPorts[key]; got != 20282 {
		t.Fatalf("omitted port must reuse persisted 20282, got %d (SegmentPorts=%v)", got, state.SegmentPorts)
	}
	if len(state.PortChanges) != 0 {
		t.Fatalf("clean reuse must not record port changes, got %+v", state.PortChanges)
	}
	applyTunnelPortsToRequest(req, state)
	outItems := asMapSlice(req["outNodeId"])
	if len(outItems) != 1 || asInt(outItems[0]["port"], 0) != 20282 {
		t.Fatalf("resolved port must be written back into the request, got %v", req["outNodeId"])
	}

	// ② 现口被入站占用：重挑＋PortChanges 点名旧口。
	if err := r.CreateNodeInbound(&model.NodeInbound{
		KernelID: 1, NodeID: nodeID, Name: "squatter", Protocol: "vless",
		Port: 20282, CreatedTime: now, UpdatedTime: now,
	}); err != nil {
		t.Fatalf("create squatter inbound: %v", err)
	}
	state = prepare(newReq())
	if got := state.SegmentPorts[key]; got == 20282 || got <= 0 {
		t.Fatalf("occupied port must be re-picked, got %d", got)
	}
	if len(state.PortChanges) != 1 || state.PortChanges[0].OldPort != 20282 {
		t.Fatalf("re-pick must be recorded for the receipt warning, got %+v", state.PortChanges)
	}

	// ③ 显式指定被占口：与 type-2 同口径拒绝（旧行为无校验直达落库）。
	req = newReq()
	asMapSlice(req["outNodeId"])[0]["port"] = 20282
	tx := r.BeginTx()
	defer func() { _ = tx.Rollback() }()
	if _, err := h.prepareTunnelCreateState(tx, req, 1, tunnelID); err == nil {
		t.Fatalf("explicit occupied detached port must be rejected")
	}
}

// ---- L4-08：计量上限守卫——视图点名＋边界 ----

func TestR5FEL4MeteringCapacityGuard(t *testing.T) {
	r := openHandlerRepo(t)
	h := New(r, "test-secret")
	now := time.Now()

	over := &model.Subscription{ID: 70000, Name: "over-cap", Kind: model.SubscriptionShare, Enabled: 1}
	if view := h.subscriptionView(over, nil, now, ""); !view.MeteringUnsupported {
		t.Fatalf("L4-08: subscription id 70000 must be flagged meteringUnsupported")
	}
	under := &model.Subscription{ID: 100, Name: "under-cap", Kind: model.SubscriptionShare, Enabled: 1}
	if view := h.subscriptionView(under, nil, now, ""); view.MeteringUnsupported {
		t.Fatalf("subscription id 100 must stay countable")
	}
	// 警示去重不得 panic、重复调用无副作用。
	warnUncountableSubscription(over)
	warnUncountableSubscription(over)
	warnUncountableSubscription(under)
}
