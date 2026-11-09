package kernel

import (
	"testing"
)

// 原格式回归保护（源 P2-4）：零选项时 4 种格式的输出必须与规则引擎
// 引入前逐字节一致。基准用 golden 文件固化（testdata/ 下），运行此
// 测试即做 diff 比对。2.1.2 订阅处理/规则删除后，这就是订阅输出
// 「回归原格式」的逐字对账面。
func TestSubscriptionOriginalFormatRegression(t *testing.T) {
	eps := shareFixtures(t)
	opts := SubOptions{}
	cases := []struct {
		format string
		golden string
	}{
		{FormatClash, "p2-clash-rulesoff.golden.yaml"},
		{FormatShadowrocket, "p2-shadowrocket-rulesoff.golden.yaml"},
		{FormatLoon, "p2-loon-rulesoff.golden.txt"},
		{FormatQX, "p2-qx-rulesoff.golden.txt"},
	}
	for _, c := range cases {
		body, _, err := Subscription(c.format, eps, opts)
		if err != nil {
			t.Fatalf("%s: %v", c.format, err)
		}
		assertGolden(t, c.golden, body)
	}
}
