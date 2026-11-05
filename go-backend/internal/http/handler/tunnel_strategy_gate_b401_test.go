package handler

import (
	"strings"
	"testing"
)

// B4-01 残面: 存量状态重放（reconstructTunnelState →
// validateTunnelRuntimeBeforeApply）此前对 chain_tunnel 存量
// strategy 零判, 非法存量值 (集外/跳行 best) 会被原样重放下发。
// 现 fail-closed 点名。本组直接对验证点构造 state。

func b401State(in, hop, out []tunnelRuntimeNode) *tunnelCreateState {
	st := &tunnelCreateState{
		Type:     2,
		InNodes:  in,
		OutNodes: out,
		Nodes:    map[int64]*nodeRecord{},
	}
	if hop != nil {
		st.ChainHops = [][]tunnelRuntimeNode{hop}
	}
	return st
}

func TestB401StoredIllegalStrategyRejectedOnRedeployValidation(t *testing.T) {
	cases := []struct {
		name  string
		state *tunnelCreateState
		want  string
	}{
		{
			name:  "集外值 banana（入口行）",
			state: b401State([]tunnelRuntimeNode{{NodeID: 1, ChainType: 1, Strategy: "banana"}}, nil, nil),
			want:  "banana",
		},
		{
			name:  "跳行 best（chainType 2 不含 best）",
			state: b401State(nil, []tunnelRuntimeNode{{NodeID: 2, ChainType: 2, Strategy: "best"}}, nil),
			want:  "best",
		},
		{
			name:  "大小写异形 FIFO",
			state: b401State(nil, nil, []tunnelRuntimeNode{{NodeID: 3, ChainType: 3, Strategy: "FIFO"}}),
			want:  "FIFO",
		},
	}
	for _, tc := range cases {
		err := validateTunnelRuntimeBeforeApply(tc.state)
		if err == nil {
			t.Fatalf("%s: illegal stored strategy must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "不合法") {
			t.Fatalf("%s: error must name the offending value, got %v", tc.name, err)
		}
	}
}

func TestB401LegalStoredStrategiesPassTheGate(t *testing.T) {
	// 空组: 策略门通过后其余检查无迭代, 整体应放行。逐段合法值
	// (含出口 best 与空串=缺省 round) 都不得被策略门拦下——用
	// 「错误不来自策略门」判读 (后续配对检查因节点表为空报错
	// 与本门无关), 故把节点两两配对面留空: 只放单段单节点且
	// 无相邻段时配对循环不执行。
	st := b401State([]tunnelRuntimeNode{{NodeID: 1, ChainType: 1, Strategy: "best"}}, nil, nil)
	if err := validateTunnelRuntimeBeforeApply(st); err != nil {
		t.Fatalf("legal in-node strategy (best, chainType 1) must pass, got %v", err)
	}
	st = b401State(nil, []tunnelRuntimeNode{{NodeID: 2, ChainType: 2, Strategy: ""}}, nil)
	if err := validateTunnelRuntimeBeforeApply(st); err != nil {
		t.Fatalf("empty stored strategy (default round) must pass, got %v", err)
	}
	// 非链式隧道 (Type 1) 不在本门范围。
	st = b401State([]tunnelRuntimeNode{{NodeID: 1, ChainType: 1, Strategy: "banana"}}, nil, nil)
	st.Type = 1
	if err := validateTunnelRuntimeBeforeApply(st); err != nil {
		t.Fatalf("type 1 tunnels are out of scope, got %v", err)
	}
}
