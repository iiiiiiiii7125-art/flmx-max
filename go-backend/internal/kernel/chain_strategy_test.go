package kernel

import "testing"

// R1-03-M1：链路策略合法集分段钉格——hop 行 best 必拒（静默降级
// 面），出口/入口行 best 放行；集外值、大小写异形与 agent 别名
// （rr/random/ha）全段必拒（面板契约外，落库即声明≠生效）。
func TestChainStrategyLegalGrid(t *testing.T) {
	cases := []struct {
		chainType int
		strategy  string
		want      bool
	}{
		{3, "fifo", true}, {3, "round", true}, {3, "rand", true}, {3, "best", true},
		{2, "fifo", true}, {2, "round", true}, {2, "rand", true}, {2, "best", false},
		{1, "fifo", true}, {1, "round", true}, {1, "rand", true}, {1, "best", true},
		{3, "hash", false}, {2, "hash", false}, {1, "hash", false},
		{3, "banana", false}, {2, "banana", false}, {1, "banana", false},
		{3, "FIFO", false}, {2, "FIFO", false},
		{3, "rr", false}, {2, "random", false}, {3, "ha", false},
		{3, "", false}, {2, "", false},
		{3, " fifo ", true}, {2, " best ", false}, {3, " best ", true},
		{0, "fifo", true}, {0, "best", false}, {9, "best", false}, {9, "round", true},
	}
	for _, c := range cases {
		if got := ChainStrategyLegal(c.chainType, c.strategy); got != c.want {
			t.Errorf("ChainStrategyLegal(%d, %q) = %v, want %v", c.chainType, c.strategy, got, c.want)
		}
	}
}
