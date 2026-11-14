package handler

import (
	"strings"
	"testing"
)

// F2-PORT-01：面板侧 live 探针测试。subtractPortSpans 与两路
// builder 是纯函数；checkLivePortsOccupied 经真 WS fake agent
// （nodeSwapFakeAgent）回 CheckPortListening 探针。

func TestSubtractPortSpans(t *testing.T) {
	cases := []struct {
		name    string
		spans   []portSpan
		exclude []portSpan
		want    []portSpan
	}{
		{"no overlap", []portSpan{{8080, 8080, "端口"}}, []portSpan{{9000, 9000, "端口"}}, []portSpan{{8080, 8080, "端口"}}},
		{"full cover", []portSpan{{8080, 8080, "端口"}}, []portSpan{{8080, 8080, "端口"}}, nil},
		{"left remainder", []portSpan{{8000, 9000, "端口跳跃范围"}}, []portSpan{{8500, 9000, "端口"}}, []portSpan{{8000, 8499, "端口跳跃范围"}}},
		{"right remainder", []portSpan{{8000, 9000, "端口跳跃范围"}}, []portSpan{{8000, 8500, "端口"}}, []portSpan{{8501, 9000, "端口跳跃范围"}}},
		{"middle split", []portSpan{{8000, 9000, "端口跳跃范围"}}, []portSpan{{8300, 8400, "端口"}}, []portSpan{{8000, 8299, "端口跳跃范围"}, {8401, 9000, "端口跳跃范围"}}},
		{"multiple excludes", []portSpan{{8000, 9000, "端口跳跃范围"}}, []portSpan{{8100, 8200, "端口"}, {8800, 8900, "端口"}}, []portSpan{{8000, 8099, "端口跳跃范围"}, {8201, 8799, "端口跳跃范围"}, {8901, 9000, "端口跳跃范围"}}},
	}
	for _, tc := range cases {
		got := subtractPortSpans(tc.spans, tc.exclude)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestTunnelLivePortTargetsFromRequest(t *testing.T) {
	req := map[string]interface{}{
		"inPort": 9000,
		"outNodeId": []interface{}{
			map[string]interface{}{"nodeId": int64(3), "port": 9001},
			map[string]interface{}{"nodeId": int64(4), "port": 0},
		},
		"chainNodes": []interface{}{
			[]interface{}{map[string]interface{}{"nodeId": int64(5), "port": 9002}},
		},
	}
	own := map[int64]map[int]bool{3: {9001: true}}
	targets := tunnelLivePortTargetsFromRequest(req, []int64{1, 2}, own)
	if len(targets) != 3 {
		t.Fatalf("targets = %v, want nodes 1, 2, 5", targets)
	}
	byNode := map[int64][]portSpan{}
	for _, tg := range targets {
		byNode[tg.NodeID] = tg.Spans
	}
	if len(byNode[1]) != 1 || byNode[1][0].lo != 9000 {
		t.Fatalf("node 1 spans = %v, want [9000]", byNode[1])
	}
	if len(byNode[2]) != 1 || byNode[2][0].lo != 9000 {
		t.Fatalf("node 2 spans = %v, want [9000]", byNode[2])
	}
	if len(byNode[5]) != 1 || byNode[5][0].lo != 9002 {
		t.Fatalf("node 5 spans = %v, want [9002]", byNode[5])
	}
	if _, ok := byNode[3]; ok {
		t.Fatalf("node 3 must be excluded by own ports, got %v", byNode[3])
	}
	if _, ok := byNode[4]; ok {
		t.Fatalf("node 4 (port 0, auto-pick) must not be probed, got %v", byNode[4])
	}
}

func TestTunnelLivePortTargetsFromState(t *testing.T) {
	state := &tunnelCreateState{
		InNodes:  []tunnelRuntimeNode{{NodeID: 1}, {NodeID: 2}},
		OutNodes: []tunnelRuntimeNode{{NodeID: 3, Port: 9001}},
		ChainHops: [][]tunnelRuntimeNode{
			{{NodeID: 5, Port: 9002}},
		},
		SegmentPorts: map[tunnelChainPortKey]int{
			{chainType: 2, inx: 1, nodeID: 6}: 9003,
		},
	}
	own := map[int64]map[int]bool{5: {9002: true}}
	targets := tunnelLivePortTargetsFromState(state, 9000, own)
	byNode := map[int64][]portSpan{}
	for _, tg := range targets {
		byNode[tg.NodeID] = tg.Spans
	}
	for node, want := range map[int64]int{1: 9000, 2: 9000, 3: 9001, 6: 9003} {
		if len(byNode[node]) != 1 || byNode[node][0].lo != want {
			t.Fatalf("node %d spans = %v, want [%d]", node, byNode[node], want)
		}
	}
	if _, ok := byNode[5]; ok {
		t.Fatalf("node 5 must be excluded by own ports, got %v", byNode[5])
	}
	if got := tunnelLivePortTargetsFromState(nil, 9000, nil); got != nil {
		t.Fatalf("nil state must yield nil targets, got %v", got)
	}
}

func TestCheckLivePortsOccupied(t *testing.T) {
	a := newNodeSwapFakeAgent(t)
	a.reply = func(command cleanupAgentCommand) (bool, string, map[string]interface{}) {
		if command.Type == "CheckPortListening" {
			return true, "", map[string]interface{}{"listening": []interface{}{8080}}
		}
		return true, "", nil
	}
	// fake agent 只监听 8080：命中单口、命中跳跃范围、未命中均定性。
	if msg := a.h.checkLivePortsOccupied([]livePortProbeTarget{{NodeID: 1, Spans: []portSpan{{8080, 8080, "端口"}}}}); !strings.Contains(msg, "8080") || !strings.Contains(msg, "已被节点上其他进程占用") {
		t.Fatalf("occupied single port must report, got %q", msg)
	}
	if msg := a.h.checkLivePortsOccupied([]livePortProbeTarget{{NodeID: 1, Spans: []portSpan{{8000, 9000, "端口跳跃范围"}}}}); !strings.Contains(msg, "8080") {
		t.Fatalf("occupied port inside hop range must report 8080, got %q", msg)
	}
	if msg := a.h.checkLivePortsOccupied([]livePortProbeTarget{{NodeID: 1, Spans: []portSpan{{9000, 9000, "端口"}}}}); msg != "" {
		t.Fatalf("free port must not report, got %q", msg)
	}
	// 离线节点：探针失败 fail-open，DB 结论不变（回 ""）。
	if msg := a.h.checkLivePortsOccupied([]livePortProbeTarget{{NodeID: 2, Spans: []portSpan{{8080, 8080, "端口"}}}}); msg != "" {
		t.Fatalf("offline node probe must fail open, got %q", msg)
	}
}
