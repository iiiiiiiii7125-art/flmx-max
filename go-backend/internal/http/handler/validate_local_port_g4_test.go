package handler

import (
	"strings"
	"testing"
)

// G4 / B3（W10 复审 A1）：节点端口规格的真实语义是转发类入口端口的
// 可分配端口池，内核入站监听端口不在其口径内。处理方式是把报错
// 文案的约束范围写明（校验行为本身不变），本用例钉住文案口径与
// 既有校验行为：池内放行、池外拒绝且文案含「允许范围」（契约测试
// f015 依赖）与「入口端口」范围说明。

func TestG4ValidateLocalNodePortErrorScopesRange(t *testing.T) {
	node := &nodeRecord{Name: "g4-node", PortRange: "1000-19999"}
	if err := validateLocalNodePort(node, 15000); err != nil {
		t.Fatalf("in-pool port must pass: %v", err)
	}
	err := validateLocalNodePort(node, 24601)
	if err == nil {
		t.Fatal("out-of-pool entry port must be rejected")
	}
	for _, want := range []string{"允许范围", "入口端口", "24601"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must scope the range to entry ports, missing %q: %v", want, err)
		}
	}
}
