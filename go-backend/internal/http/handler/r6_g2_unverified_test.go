package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go-backend/internal/ws"
)

// R6-L4-01 / R6-L4-02 回归：UDP 诊断诚实化。
//   - 手动诊断面：已执行的 UDP 探测在组装单点（appendPathDiagnosis）
//     收口为「未验证」形态（success=false、unverified=true、延迟/丢包
//     为 null），不参与成败聚合；agent 的探测前真实失败（DNS 等）
//     保持失败、TCP 面逐字不变。
//   - 质量探测面：UDP 协议段记未测（Untested＋哨兵），不进延迟/
//     丢包聚合、不判快照失败；最优出口评分排除 UDP 出口样本。

func g2StubPingSends(t *testing.T, udpPayload, tcpPayload map[string]interface{}) {
	t.Helper()
	origUDP, origTCP := udpPingSend, tcpPingSend
	udpPingSend = func(_ *Handler, _ int64, _ interface{}, _ time.Duration) (ws.CommandResult, error) {
		return ws.CommandResult{Type: "UdpPingResponse", Success: true, Data: udpPayload}, nil
	}
	tcpPingSend = func(_ *Handler, _ int64, _ interface{}, _ time.Duration) (ws.CommandResult, error) {
		return ws.CommandResult{Type: "TcpPingResponse", Success: true, Data: tcpPayload}, nil
	}
	t.Cleanup(func() { udpPingSend, tcpPingSend = origUDP, origTCP })
}

func g2NodeCache() map[int64]*nodeRecord {
	return map[int64]*nodeRecord{
		1: {ID: 1, Name: "entry", Status: 1, ServerIP: "10.0.0.1", ServerIPv4: "10.0.0.1", TCPListenAddr: "[::]"},
		2: {ID: 2, Name: "next", Status: 1, ServerIP: "10.0.0.2", ServerIPv4: "10.0.0.2", TCPListenAddr: "[::]"},
	}
}

func assertG2UnverifiedItem(t *testing.T, item map[string]interface{}) {
	t.Helper()
	if asBool(item["success"], true) {
		t.Fatalf("unverified item must not present success: %+v", item)
	}
	if !asBool(item["unverified"], false) {
		t.Fatalf("unverified item must carry unverified=true: %+v", item)
	}
	if item["averageTime"] != nil {
		t.Fatalf("unverified averageTime must be null (unmeasured), got %v", item["averageTime"])
	}
	if item["packetLoss"] != nil {
		t.Fatalf("unverified packetLoss must be null (unmeasured), got %v", item["packetLoss"])
	}
	if item["message"] != diagnosisUDPUnverifiedMessage {
		t.Fatalf("unverified message = %v, want canonical %q", item["message"], diagnosisUDPUnverifiedMessage)
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	for _, want := range []string{`"averageTime":null`, `"packetLoss":null`, `"unverified":true`, `"success":false`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("serialized unverified item missing %s: %s", want, raw)
		}
	}
}

// 老 agent 报文（dial 成功即 success=true＋本地耗时/0 丢包）在面板
// 组装层必须收口为未验证形态——存量节点不升级 agent 也不得继续
// 呈现伪造实测（L9 S5 真机形态：不可达目标呈 0.07ms/0%）。
func TestG2UDPTargetLegLegacyAgentSuccessBecomesUnverified(t *testing.T) {
	g2StubPingSends(t, map[string]interface{}{
		"success": true, "averageTime": 0.0739, "packetLoss": 0.0,
	}, nil)
	h := &Handler{pingSlots: newNodePingSlots()}
	var results []map[string]interface{}
	h.appendPathDiagnosis(&results, g2NodeCache(), 1, "10.0.0.2", 53,
		"entry (1) -> 目标 (10.0.0.2:53)",
		map[string]interface{}{"nodeId": int64(1), "targetIp": "10.0.0.2", "targetPort": 53, "protocol": "udp"},
		"udp", diagnosisExecOptions{})
	if len(results) != 1 {
		t.Fatalf("expected one result item, got %+v", results)
	}
	assertG2UnverifiedItem(t, results[0])
	if results[0]["protocol"] != "udp" {
		t.Fatalf("target leg protocol must stay udp, got %v", results[0]["protocol"])
	}
}

// 新 agent 报文（success=false＋unverified 标记）同样收口，且不得
// 把 agent 回的 0 值当实测带出。
func TestG2UDPTargetLegNewAgentUnverifiedBecomesUnverified(t *testing.T) {
	g2StubPingSends(t, map[string]interface{}{
		"success": false, "unverified": true, "averageTime": 0.0, "packetLoss": 0.0,
		"errorMessage": "UDP 无通用回声协议，无法实测连通性",
	}, nil)
	h := &Handler{pingSlots: newNodePingSlots()}
	var results []map[string]interface{}
	h.appendPathDiagnosis(&results, g2NodeCache(), 1, "10.0.0.2", 53,
		"entry (1) -> 目标 (10.0.0.2:53)",
		map[string]interface{}{"nodeId": int64(1), "targetIp": "10.0.0.2", "targetPort": 53, "protocol": "udp"},
		"udp", diagnosisExecOptions{})
	if len(results) != 1 {
		t.Fatalf("expected one result item, got %+v", results)
	}
	assertG2UnverifiedItem(t, results[0])
}

// 防过修对照：UDP 探测前真实失败（DNS 解析失败，success=false、
// packetLoss=100、无 unverified 标记）是与协议无关的目标错误，
// 必须保持失败形态、不得被未验证豁免吞掉。
func TestG2UDPTargetLegPreProbeFailureStaysFailure(t *testing.T) {
	g2StubPingSends(t, map[string]interface{}{
		"success": false, "averageTime": 0.0, "packetLoss": 100.0,
		"errorMessage": "dial udp: lookup gone.invalid: no such host",
	}, nil)
	h := &Handler{pingSlots: newNodePingSlots()}
	var results []map[string]interface{}
	h.appendPathDiagnosis(&results, g2NodeCache(), 1, "10.0.0.2", 53,
		"entry (1) -> 目标 (gone.invalid:53)",
		map[string]interface{}{"nodeId": int64(1), "targetIp": "10.0.0.2", "targetPort": 53, "protocol": "udp"},
		"udp", diagnosisExecOptions{})
	if len(results) != 1 {
		t.Fatalf("expected one result item, got %+v", results)
	}
	item := results[0]
	if asBool(item["success"], true) || asBool(item["unverified"], false) {
		t.Fatalf("pre-probe failure must stay a plain failure: %+v", item)
	}
	if got := asFloat(item["packetLoss"], -1); got != 100 {
		t.Fatalf("pre-probe failure packetLoss = %v, want 100", got)
	}
	if msg := asString(item["message"]); !strings.Contains(msg, "no such host") {
		t.Fatalf("pre-probe failure message = %q, want the agent error preserved", msg)
	}
}

// TCP 对照：行为逐字不变（成功＋实测数值、无 unverified 键）。
func TestG2TCPTargetLegUnchanged(t *testing.T) {
	g2StubPingSends(t, nil, map[string]interface{}{
		"success": true, "averageTime": 12.5, "packetLoss": 0.0,
	})
	h := &Handler{pingSlots: newNodePingSlots()}
	var results []map[string]interface{}
	h.appendPathDiagnosis(&results, g2NodeCache(), 1, "10.0.0.2", 443,
		"entry (1) -> 目标 (10.0.0.2:443)",
		map[string]interface{}{"nodeId": int64(1), "targetIp": "10.0.0.2", "targetPort": 443, "protocol": "tcp"},
		"tcp", diagnosisExecOptions{})
	if len(results) != 1 {
		t.Fatalf("expected one result item, got %+v", results)
	}
	item := results[0]
	if !asBool(item["success"], false) {
		t.Fatalf("tcp success must pass through: %+v", item)
	}
	if _, ok := item["unverified"]; ok {
		t.Fatalf("tcp item must not carry unverified: %+v", item)
	}
	if got := asFloat(item["averageTime"], -1); got != 12.5 {
		t.Fatalf("tcp averageTime = %v, want 12.5", got)
	}
	if item["message"] != "连接成功" {
		t.Fatalf("tcp message = %v, want 连接成功", item["message"])
	}
}

// 链路段腿：kcp 段经同一组装点收口，且结果项补上 protocol 字段
// （链腿 metadata 原本不带 protocol，与 TCP 腿不可区分是 L4-01 的
// 枚举缺口之一）。
func TestG2ChainHopUDPSegmentBecomesUnverifiedWithProtocol(t *testing.T) {
	g2StubPingSends(t, map[string]interface{}{
		"success": true, "averageTime": 0.05, "packetLoss": 0.0,
	}, nil)
	h := &Handler{pingSlots: newNodePingSlots()}
	var results []map[string]interface{}
	h.appendChainHopDiagnosis(&results, g2NodeCache(), 1,
		chainNodeRecord{NodeID: 2, NodeName: "next", Port: 30030, Protocol: "kcp"},
		"entry (1) -> next (2)", nil, "auto", "kcp", diagnosisExecOptions{})
	if len(results) != 1 {
		t.Fatalf("expected one result item, got %+v", results)
	}
	assertG2UnverifiedItem(t, results[0])
	if results[0]["protocol"] != "kcp" {
		t.Fatalf("chain hop item protocol = %v, want backfilled kcp", results[0]["protocol"])
	}
}

// 聚合排除（端到端）：TCP 成功＋UDP 未验证同批执行，进度必须是
// Success=1/Failed=0/Unverified=1——UDP 项不判失败。
func TestG2RunDiagnosisWorkItemsExcludesUnverifiedFromVerdict(t *testing.T) {
	g2StubPingSends(t,
		map[string]interface{}{"success": true, "averageTime": 0.07, "packetLoss": 0.0},
		map[string]interface{}{"success": true, "averageTime": 12.5, "packetLoss": 0.0},
	)
	h := setupProbeTargetTunnelHandler(t)
	workItems := []diagnosisWorkItem{
		{
			fromNodeID: 10, targetIP: "10.0.0.2", targetPort: 443,
			description:  "entry (10) -> 目标 TCP",
			metadata:     map[string]interface{}{"nodeId": int64(10), "targetIp": "10.0.0.2", "targetPort": 443, "protocol": "tcp"},
			ipPreference: "auto", protocol: "tcp",
		},
		{
			fromNodeID: 10, targetIP: "10.0.0.2", targetPort: 53,
			description:  "entry (10) -> 目标 UDP",
			metadata:     map[string]interface{}{"nodeId": int64(10), "targetIp": "10.0.0.2", "targetPort": 53, "protocol": "udp"},
			ipPreference: "auto", protocol: "udp",
		},
	}
	var lastProgress diagnosisProgress
	results := h.runDiagnosisWorkItems(context.Background(), workItems,
		func(_ int, _ map[string]interface{}, p diagnosisProgress) { lastProgress = p })
	if len(results) != 2 {
		t.Fatalf("expected two results, got %+v", results)
	}
	want := diagnosisProgress{Total: 2, Completed: 2, Success: 1, Failed: 0, Unverified: 1}
	if lastProgress != want {
		t.Fatalf("final progress = %+v, want %+v", lastProgress, want)
	}
	if got := summarizeDiagnosisProgress(results); got != want {
		t.Fatalf("summarized progress = %+v, want %+v", got, want)
	}
}

// 汇总单点：混合项的成败排除在 summarizeDiagnosisProgress 同口径。
func TestG2SummarizeDiagnosisProgressCountsUnverifiedSeparately(t *testing.T) {
	items := []map[string]interface{}{
		{"success": true},
		{"success": false},
		{"success": false, "unverified": true},
		{"success": false, "unverified": true},
	}
	got := summarizeDiagnosisProgress(items)
	want := diagnosisProgress{Total: 4, Completed: 4, Success: 1, Failed: 1, Unverified: 2}
	if got != want {
		t.Fatalf("progress = %+v, want %+v", got, want)
	}
}

// 转发流式：statusItems（规则状态项位）与探测项的未验证计数在
// done 汇总里组合透传，不被 statusItems 前缀吞掉。
func TestG2ForwardStreamProgressCombinesUnverified(t *testing.T) {
	g2StubPingSends(t, nil,
		map[string]interface{}{"success": true, "averageTime": 9.0, "packetLoss": 0.0})
	h := setupProbeTargetTunnelHandler(t)
	statusItem := map[string]interface{}{
		"success": false, "unverified": true, "averageTime": nil, "packetLoss": nil,
		"nodeId": int64(10), "targetIp": "10.0.0.2", "targetPort": 53, "protocol": "udp",
		"description": "entry (10) -> 目标 UDP", "message": diagnosisUDPUnverifiedMessage,
	}
	plan := &forwardDiagnosisPlan{
		name:        "g2-forward",
		statusItems: []map[string]interface{}{statusItem},
		workItems: []diagnosisWorkItem{
			{
				fromNodeID: 10, targetIP: "10.0.0.2", targetPort: 443,
				description:  "entry (10) -> 目标 TCP",
				metadata:     map[string]interface{}{"nodeId": int64(10), "targetIp": "10.0.0.2", "targetPort": 443, "protocol": "tcp"},
				ipPreference: "auto", protocol: "tcp",
			},
		},
	}
	events := frv3RunForwardStream(t, h, plan)
	done := frv3StreamDone(t, events)
	want := diagnosisProgress{Total: 2, Completed: 2, Success: 1, Failed: 0, Unverified: 1}
	if done != want {
		t.Fatalf("stream done progress = %+v, want %+v", done, want)
	}
	items := frv3StreamItems(t, events)
	if len(items) != 2 {
		t.Fatalf("expected two item events, got %+v", items)
	}
	assertG2UnverifiedItem(t, items[0].Result)
}

// ---- 质量探测面（L4-02） ----

func g2SeedSegmentTunnel(t *testing.T, h *Handler, tunnelID int64, tunnelProtocol string, midProtocol, exitProtocol string, withMid bool) {
	t.Helper()
	now := time.Now().UnixMilli()
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := h.repo.DB().Exec(query, args...).Error; err != nil {
			t.Fatalf("seed exec %q: %v", query, err)
		}
	}
	exec(`INSERT INTO tunnel(id, name, traffic_ratio, type, protocol, flow, created_time, updated_time, status, inx, ip_preference, probe_target_host, probe_target_port)
		VALUES(?, ?, 1, 2, ?, 1, ?, ?, 1, ?, '', '', 0)`,
		tunnelID, fmt.Sprintf("g2-quality-%d", tunnelID), tunnelProtocol, now, now, tunnelID)
	exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(?, '1', 10, 30001, 'fifo', 1, ?)`, tunnelID, tunnelProtocol)
	if withMid {
		exec(`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
			VALUES(20, 'mid', 'mid-secret', '10.0.0.20', '10.0.0.20', '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)`, now, now)
		exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
			VALUES(?, '2', 20, 30020, 'fifo', 1, ?)`, tunnelID, midProtocol)
	}
	exec(`INSERT INTO node(id, name, secret, server_ip, server_ip_v4, server_ip_v6, port, interface_name, version, http, tls, socks, created_time, updated_time, status, tcp_listen_addr, udp_listen_addr, inx)
		VALUES(30, 'exit', 'exit-secret', '10.0.0.30', '10.0.0.30', '', '30000-30100', '', 'v1', 1, 1, 1, ?, ?, 1, '[::]', '[::]', 0)`, now, now)
	exec(`INSERT INTO chain_tunnel(tunnel_id, chain_type, node_id, port, strategy, inx, protocol)
		VALUES(?, '3', 30, 30030, 'fifo', 1, ?)`, tunnelID, exitProtocol)
}

func g2DecodeDetails(t *testing.T, snap tunnelQualitySnapshot) tunnelQualityChainDetails {
	t.Helper()
	var details tunnelQualityChainDetails
	if err := json.Unmarshal([]byte(snap.ChainDetails), &details); err != nil {
		t.Fatalf("decode chain details: %v (raw %q)", err, snap.ChainDetails)
	}
	return details
}

// 全 UDP 出口段：主跳记未测、不发 TCP 探测、不判失败；快照成败由
// 出口公网腿决定，入口→出口聚合保持未测哨兵（不冒充 0/0）。
func TestG2QualityProberUDPSegmentUntested(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	g2SeedSegmentTunnel(t, h, 901, "udp", "", "udp", false)

	p := newTunnelQualityProber(h)
	var calls []string
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		calls = append(calls, fmt.Sprintf("%d|%s|%d", nodeID, ip, port))
		if ip == "www.bing.com" {
			return 40, 0, nil
		}
		return 0, 100, fmt.Errorf("unexpected probe node=%d target=%s:%d", nodeID, ip, port)
	}
	p.probeTunnel(901)

	for _, c := range calls {
		if c == "10|10.0.0.30|30030" {
			t.Fatalf("UDP segment must not be TCP-probed, calls=%+v", calls)
		}
	}
	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one snapshot, got %+v", snaps)
	}
	snap := snaps[0]
	if !snap.Success || snap.ErrorMessage != "" {
		t.Fatalf("healthy UDP chain must not read as failed: %+v", snap)
	}
	if snap.EntryToExitLatency != -1 || snap.EntryToExitLoss != 100 {
		t.Fatalf("all-untested aggregate must keep untested sentinel, got lat=%v loss=%v", snap.EntryToExitLatency, snap.EntryToExitLoss)
	}
	details := g2DecodeDetails(t, snap)
	if len(details.PrimaryPath) != 1 {
		t.Fatalf("expected one primary hop, got %+v", details.PrimaryPath)
	}
	hop := details.PrimaryPath[0]
	if !hop.Untested || hop.Latency != -1 || hop.Loss != 100 {
		t.Fatalf("primary hop must be untested sentinel, got %+v", hop)
	}
	foundCandidate := false
	for _, ch := range details.CandidateHops {
		if ch.FromNodeID == 10 && ch.ToNodeID == 30 {
			foundCandidate = true
			if !ch.Untested || ch.ErrorMessage != "" {
				t.Fatalf("candidate hop to UDP segment must be untested without error, got %+v", ch)
			}
		}
	}
	if !foundCandidate {
		t.Fatalf("candidate hop 10 -> 30 missing in %+v", details.CandidateHops)
	}
}

// 混合链（中转 tls 实测＋出口 udp 未测）：聚合只含实测跳——延迟为
// 实测跳之和、丢包不被未测跳的哨兵 100 拉满。
func TestG2QualityProberMixedSegmentsAggregateMeasuredOnly(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	g2SeedSegmentTunnel(t, h, 902, "tls", "tls", "udp", true)

	p := newTunnelQualityProber(h)
	var calls []string
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		calls = append(calls, fmt.Sprintf("%d|%s|%d", nodeID, ip, port))
		switch fmt.Sprintf("%d|%s|%d", nodeID, ip, port) {
		case "10|10.0.0.20|30020":
			return 10, 0, nil
		case "30|www.bing.com|443":
			return 40, 0, nil
		}
		return 0, 100, fmt.Errorf("unexpected probe node=%d target=%s:%d", nodeID, ip, port)
	}
	p.probeTunnel(902)

	for _, c := range calls {
		if c == "20|10.0.0.30|30030" {
			t.Fatalf("UDP exit segment must not be TCP-probed, calls=%+v", calls)
		}
	}
	snaps := p.GetAll()
	if len(snaps) != 1 || !snaps[0].Success {
		t.Fatalf("expected successful mixed snapshot, got %+v", snaps)
	}
	snap := snaps[0]
	if snap.EntryToExitLatency != 10 || snap.EntryToExitLoss != 0 {
		t.Fatalf("aggregate must contain measured hops only, got lat=%v loss=%v", snap.EntryToExitLatency, snap.EntryToExitLoss)
	}
	details := g2DecodeDetails(t, snap)
	if len(details.PrimaryPath) != 2 {
		t.Fatalf("expected two primary hops, got %+v", details.PrimaryPath)
	}
	if details.PrimaryPath[0].Untested || details.PrimaryPath[0].Latency != 10 {
		t.Fatalf("tls hop must stay measured, got %+v", details.PrimaryPath[0])
	}
	if !details.PrimaryPath[1].Untested {
		t.Fatalf("udp hop must be untested, got %+v", details.PrimaryPath[1])
	}
}

// 评分排除：UDP 出口不产评分样本、不发探测；全 UDP 时样本为空；
// 段协议为空时回退隧道协议判定（chainSegmentProtocol 口径）。
func TestG2BestExitOwnerExcludesUDPExitsFromScores(t *testing.T) {
	nodes := map[int64]*nodeRecord{
		10: {ID: 10, Status: 1, ServerIP: "10.0.0.10", ServerIPv4: "10.0.0.10", TCPListenAddr: "[::]"},
		30: {ID: 30, Status: 1, ServerIP: "10.0.0.30", ServerIPv4: "10.0.0.30", TCPListenAddr: "[::]"},
		31: {ID: 31, Status: 1, ServerIP: "10.0.0.31", ServerIPv4: "10.0.0.31", TCPListenAddr: "[::]"},
	}
	owner := chainNodeRecord{NodeID: 10, NodeName: "entry"}
	var pingedIPs []string
	ping := func(_ int64, ip string, _ int, _ diagnosisExecOptions) (float64, float64, error) {
		pingedIPs = append(pingedIPs, ip)
		return 25, 0, nil
	}

	scores := evaluateBestExitOwner(owner, []chainNodeRecord{
		{NodeID: 30, NodeName: "exit-udp", Port: 30030, Protocol: "udp"},
		{NodeID: 31, NodeName: "exit-tls", Port: 30031, Protocol: "tls"},
	}, nodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), ping, "")
	if len(scores) != 1 || scores[0].ExitNodeID != 31 || !scores[0].Success {
		t.Fatalf("only the tls exit may be scored, got %+v", scores)
	}
	for _, ip := range pingedIPs {
		if ip == "10.0.0.30" {
			t.Fatalf("UDP exit must not be probed for scoring, pinged=%+v", pingedIPs)
		}
	}

	scores = evaluateBestExitOwner(owner, []chainNodeRecord{
		{NodeID: 30, NodeName: "exit-udp", Port: 30030, Protocol: "kcp"},
	}, nodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), ping, "")
	if len(scores) != 0 {
		t.Fatalf("all-UDP exits must yield no scores, got %+v", scores)
	}

	scores = evaluateBestExitOwner(owner, []chainNodeRecord{
		{NodeID: 30, NodeName: "exit-fallback", Port: 30030},
	}, nodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), ping, "udp")
	if len(scores) != 0 {
		t.Fatalf("empty segment protocol must fall back to tunnel protocol (udp -> excluded), got %+v", scores)
	}
	scores = evaluateBestExitOwner(owner, []chainNodeRecord{
		{NodeID: 30, NodeName: "exit-fallback", Port: 30030},
	}, nodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), ping, "")
	if len(scores) != 1 {
		t.Fatalf("empty segment protocol with non-UDP tunnel must be scored, got %+v", scores)
	}
}
