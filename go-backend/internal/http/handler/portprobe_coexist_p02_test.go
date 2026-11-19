package handler

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"go-backend/internal/kernel"
)

// F2-PORT-01 P0-2：两条自动选口盲路的活探针回归测试。
//
//   - (a) 入站创建 Port==0：定口后探活，命中换口重试（上限 5 次），
//     探针失败 fail-open（离线节点回落 DB，不拒单——由既有
//     TestR1B7InboundUpdateWithoutPortKeepsStoredPort 的创建分支覆盖，
//     该用例节点无 agent、创建仍成功）。
//   - (b) 隧道创建自动段口：验证段定型即最终端口，间隙探活、命中
//     换口重试（上限 5 次），探针失败 fail-open。
//
// 探针面统一用真 WS 假 agent（nodeSwapFakeAgent）回 CheckPortListening。

// p02FirstHitReply 是首探命中、之后空闲的探针回执：与
// TestForwardCreateRetriesOnLivePortCollision 同形。返回的计数器与
// 首探端口由调用方断言（重试真实发生、落库口不是首探命中的口）。
func p02FirstHitReply(t *testing.T, a *nodeSwapFakeAgent) (*sync.Mutex, *int, *int) {
	t.Helper()
	var mu sync.Mutex
	probeCalls := 0
	firstProbed := 0
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			mu.Lock()
			probeCalls++
			n := probeCalls
			mu.Unlock()
			var body struct {
				Ranges []struct {
					Lo int `json:"lo"`
					Hi int `json:"hi"`
				} `json:"ranges"`
			}
			_ = json.Unmarshal(command.Data, &body)
			probed := 0
			if len(body.Ranges) > 0 {
				probed = body.Ranges[0].Lo
			}
			if n == 1 {
				mu.Lock()
				firstProbed = probed
				mu.Unlock()
				return true, "", map[string]interface{}{"listening": []interface{}{probed}}
			}
			return true, "", map[string]interface{}{"listening": []interface{}{}}
		}
		return true, "", nil
	}
	return &mu, &probeCalls, &firstProbed
}

func TestCoexistP02InboundCreateRetriesOnLivePortCollision(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	mu, probeCalls, firstProbed := p02FirstHitReply(t, a)

	box := seedChainKernel(t, a.h.repo, 1, kernel.TypeSingbox)
	entry := seedChainInbound(t, a.h.repo, box, "p02-template", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	createBody := inboundRequest{
		NodeID: 1, KernelType: kernel.TypeSingbox, Name: "p02-created",
		Protocol: old.Protocol, Settings: old.Settings, Security: old.Security,
	}
	raw, err := json.Marshal(createBody)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, a.h, false, string(raw))
	if out.Code != 0 {
		t.Fatalf("inbound create must succeed after live-port retry: %+v", out)
	}
	if got := len(a.commandsOfType("CheckPortListening")); got < 2 {
		t.Fatalf("live probe must run at least twice (retry), got %d CheckPortListening commands", got)
	}
	mu.Lock()
	probeCallsN := *probeCalls
	firstProbedN := *firstProbed
	mu.Unlock()
	if probeCallsN < 2 {
		t.Fatalf("live probe must run at least twice (retry), got %d calls", probeCallsN)
	}
	var port int
	if err := a.h.repo.DB().Raw(`SELECT port FROM node_inbound WHERE name = 'p02-created'`).Scan(&port).Error; err != nil {
		t.Fatalf("read created port: %v", err)
	}
	if port < 20000 || port >= 60000 {
		t.Fatalf("created port = %d, want within 20000-59999", port)
	}
	if firstProbedN != 0 && port == firstProbedN {
		t.Fatalf("created port %d equals the first live-occupied probe target %d: retry did not switch ports", port, firstProbedN)
	}
}

func TestCoexistP02InboundCreateExhaustsOnLiveCollision(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	// 假 agent 永远上报所探口在监听：5 次换口均撞，创建须以碰撞文案拒绝、
	// 且不落库。
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			var body struct {
				Ranges []struct {
					Lo int `json:"lo"`
					Hi int `json:"hi"`
				} `json:"ranges"`
			}
			_ = json.Unmarshal(command.Data, &body)
			probed := 0
			if len(body.Ranges) > 0 {
				probed = body.Ranges[0].Lo
			}
			return true, "", map[string]interface{}{"listening": []interface{}{probed}}
		}
		return true, "", nil
	}

	box := seedChainKernel(t, a.h.repo, 1, kernel.TypeSingbox)
	entry := seedChainInbound(t, a.h.repo, box, "p02-template-x", kernel.Inbound{Protocol: kernel.ProtocolShadowsocks, Port: 8388}, 0)
	old, err := inboundFromModel(*entry)
	if err != nil {
		t.Fatal(err)
	}
	createBody := inboundRequest{
		NodeID: 1, KernelType: kernel.TypeSingbox, Name: "p02-created-exhaust",
		Protocol: old.Protocol, Settings: old.Settings, Security: old.Security,
	}
	raw, err := json.Marshal(createBody)
	if err != nil {
		t.Fatal(err)
	}
	out := callInboundSave(t, a.h, false, string(raw))
	if out.Code == 0 {
		t.Fatalf("inbound create must fail after 5 live-port collisions, got success: %+v", out)
	}
	if !strings.Contains(out.Msg, "已被节点上其他进程占用") {
		t.Fatalf("exhaustion error must name the live collision, got msg=%q", out.Msg)
	}
	if got := len(a.commandsOfType("CheckPortListening")); got != forwardPortRetryAttempts {
		t.Fatalf("live probe must run exactly %d times (retry cap), got %d", forwardPortRetryAttempts, got)
	}
	var count int
	if err := a.h.repo.DB().Raw(`SELECT COUNT(*) FROM node_inbound WHERE name = 'p02-created-exhaust'`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("exhausted create must not persist, got %d rows", count)
	}
}

func TestCoexistP02TunnelCreateRetriesAutoSegmentPortOnLiveCollision(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	if err := a.h.repo.DB().Exec(`UPDATE node SET port = ?, status = 1 WHERE id = ?`, "36005-36007", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.h.repo.DB().Exec(`UPDATE node SET status = 1 WHERE id = ?`, 2).Error; err != nil {
		t.Fatal(err)
	}
	_, _, firstProbed := p02FirstHitReply(t, a)

	// type-1 隧道：出口段口未指定（port 0），由验证段定型、间隙探活、
	// 命中换口重试；入口节点 2 离线但库内在线（status=1），不干扰探针。
	env := k9Post(t, a.h, a.h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "p02-tun", "type": 1, "status": 1,
		"inNodeId":  []map[string]interface{}{{"nodeId": int64(2), "protocol": "tls", "strategy": "fifo"}},
		"outNodeId": []map[string]interface{}{{"nodeId": int64(1), "port": 0, "strategy": "fifo"}},
	})
	if env.Code != 0 {
		t.Fatalf("tunnel create must succeed after live-port retry: code=%d msg=%s", env.Code, env.Msg)
	}
	if got := len(a.commandsOfType("CheckPortListening")); got < 2 {
		t.Fatalf("live probe must run at least twice (retry), got %d CheckPortListening commands", got)
	}
	var tunnelID int64
	if err := a.h.repo.DB().Raw(`SELECT id FROM tunnel WHERE name = 'p02-tun'`).Scan(&tunnelID).Error; err != nil {
		t.Fatalf("read tunnel id: %v", err)
	}
	var port int
	if err := a.h.repo.DB().Raw(`SELECT COALESCE(port, 0) FROM chain_tunnel WHERE tunnel_id = ? AND chain_type = '3'`, tunnelID).Scan(&port).Error; err != nil {
		t.Fatalf("read segment port: %v", err)
	}
	if port < 36005 || port > 36007 {
		t.Fatalf("segment port = %d, want within 36005-36007", port)
	}
	if *firstProbed != 0 && port == *firstProbed {
		t.Fatalf("segment port %d equals the first live-occupied probe target %d: retry did not switch ports", port, *firstProbed)
	}
}

func TestCoexistP02TunnelCreateAutoSegmentProbeFailOpenOffline(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	// 出口节点 2 离线（无 agent 连接）、库内在线：段口探针失败须
	// fail-open，创建成功、落库口仍是 DB 选口。
	if err := a.h.repo.DB().Exec(`UPDATE node SET port = ?, status = 1 WHERE id = ?`, "36005-36007", 2).Error; err != nil {
		t.Fatal(err)
	}
	env := k9Post(t, a.h, a.h.tunnelCreate, "/api/v1/tunnel/create", map[string]interface{}{
		"name": "p02-tun-offline", "type": 1, "status": 1,
		"inNodeId":  []map[string]interface{}{{"nodeId": int64(1), "protocol": "tls", "strategy": "fifo"}},
		"outNodeId": []map[string]interface{}{{"nodeId": int64(2), "port": 0, "strategy": "fifo"}},
	})
	if env.Code != 0 {
		t.Fatalf("tunnel create with offline exit node must fail open and succeed: code=%d msg=%s", env.Code, env.Msg)
	}
	var tunnelID int64
	if err := a.h.repo.DB().Raw(`SELECT id FROM tunnel WHERE name = 'p02-tun-offline'`).Scan(&tunnelID).Error; err != nil {
		t.Fatalf("read tunnel id: %v", err)
	}
	var port int
	if err := a.h.repo.DB().Raw(`SELECT COALESCE(port, 0) FROM chain_tunnel WHERE tunnel_id = ? AND chain_type = '3'`, tunnelID).Scan(&port).Error; err != nil {
		t.Fatalf("read segment port: %v", err)
	}
	if port < 36005 || port > 36007 {
		t.Fatalf("segment port = %d, want within 36005-36007", port)
	}
}

func TestCoexistP02TunnelAutoSegmentProbeExhaustion(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	if err := a.h.repo.DB().Exec(`UPDATE node SET port = ? WHERE id = ?`, "36005-36009", 1).Error; err != nil {
		t.Fatal(err)
	}
	// 假 agent 永远上报所探口在监听：5 次换口均撞，须回末次碰撞文案。
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			var body struct {
				Ranges []struct {
					Lo int `json:"lo"`
					Hi int `json:"hi"`
				} `json:"ranges"`
			}
			_ = json.Unmarshal(command.Data, &body)
			probed := 0
			if len(body.Ranges) > 0 {
				probed = body.Ranges[0].Lo
			}
			return true, "", map[string]interface{}{"listening": []interface{}{probed}}
		}
		return true, "", nil
	}
	state := &tunnelCreateState{
		OutNodes: []tunnelRuntimeNode{{NodeID: 1, Port: 36005, ChainType: 3}},
		SegmentPorts: map[tunnelChainPortKey]int{
			{chainType: 3, inx: 1, nodeID: 1}: 36005,
		},
	}
	req := map[string]interface{}{
		"outNodeId": []interface{}{map[string]interface{}{"nodeId": int64(1), "port": 0}},
	}
	err := a.h.probeTunnelAutoSegmentPortsLive(state, req)
	if err == nil {
		t.Fatal("probe must fail after 5 live-port collisions, got nil")
	}
	if !strings.Contains(err.Error(), "已被节点上其他进程占用") {
		t.Fatalf("exhaustion error must name the live collision, got %q", err.Error())
	}
	if got := len(a.commandsOfType("CheckPortListening")); got != forwardPortRetryAttempts {
		t.Fatalf("live probe must run exactly %d times (retry cap), got %d", forwardPortRetryAttempts, got)
	}
	if got := state.SegmentPorts[tunnelChainPortKey{chainType: 3, inx: 1, nodeID: 1}]; got == 36005 {
		t.Fatalf("exhausted state must have retargeted away from 36005, still %d", got)
	}
}

func TestCoexistP02TunnelAutoSegmentPorts(t *testing.T) {
	state := &tunnelCreateState{
		OutNodes: []tunnelRuntimeNode{
			{NodeID: 11, Port: 36005, ChainType: 3},
			{NodeID: 13, Port: 36007, ChainType: 3},
		},
		ChainHops: [][]tunnelRuntimeNode{
			{{NodeID: 12, Port: 36006, ChainType: 2, Inx: 1}},
		},
		SegmentPorts: map[tunnelChainPortKey]int{
			{chainType: 3, inx: 1, nodeID: 11}: 36005,
			{chainType: 3, inx: 2, nodeID: 13}: 36007,
			{chainType: 2, inx: 1, nodeID: 12}: 36006,
		},
	}
	req := map[string]interface{}{
		"outNodeId": []interface{}{
			map[string]interface{}{"nodeId": int64(11), "port": 0},
			map[string]interface{}{"nodeId": int64(13), "port": 36007},
		},
		"chainNodes": []interface{}{
			[]interface{}{map[string]interface{}{"nodeId": int64(12), "port": 0}},
		},
	}
	auto := tunnelAutoSegmentPorts(state, req)
	if len(auto) != 2 {
		t.Fatalf("auto segments = %v, want 2 (nodes 11, 12)", auto)
	}
	byNode := map[int64]int{}
	for _, seg := range auto {
		byNode[seg.nodeID] = seg.port
	}
	if byNode[11] != 36005 || byNode[12] != 36006 {
		t.Fatalf("auto segments = %v, want node 11:36005 node 12:36006", byNode)
	}
	if _, ok := byNode[13]; ok {
		t.Fatalf("explicit port node 13 must not be an auto segment, got %v", byNode)
	}

	// 改指须同步三处：SegmentPorts＋OutNodes/ChainHops。
	retargetTunnelSegmentPort(state, 11, 36005, 36008)
	if got := state.SegmentPorts[tunnelChainPortKey{chainType: 3, inx: 1, nodeID: 11}]; got != 36008 {
		t.Fatalf("SegmentPorts after retarget = %d, want 36008", got)
	}
	if state.OutNodes[0].Port != 36008 {
		t.Fatalf("OutNodes after retarget = %d, want 36008", state.OutNodes[0].Port)
	}
	if got := state.SegmentPorts[tunnelChainPortKey{chainType: 2, inx: 1, nodeID: 12}]; got != 36006 {
		t.Fatalf("unrelated segment must be untouched, got %d", got)
	}
	if state.OutNodes[1].Port != 36007 {
		t.Fatalf("explicit segment must be untouched, got %d", state.OutNodes[1].Port)
	}
}
