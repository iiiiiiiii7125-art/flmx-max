package handler

import "testing"

// F-016 (B4): 节点端口规格支持逗号多段（parsePortRangeSpec 的集合语义），
// 但 validateLocalNodePort 此前把规格压成 min-max 区间校验，段间空隙
// 端口被误放行——自动分配永不选它、agent 侧按规格也不认，转发建得出
// 起不来。本用例直接钉住集合语义。

func TestValidateLocalNodePortMultiSegmentSpec(t *testing.T) {
	node := &nodeRecord{Name: "multi", PortRange: "10000-10010,20000-20010"}
	for _, port := range []int{10000, 10005, 10010, 20000, 20005, 20010} {
		if err := validateLocalNodePort(node, port); err != nil {
			t.Fatalf("port %d is inside the spec and must pass: %v", port, err)
		}
	}
	for _, port := range []int{9999, 15000, 10011, 19999, 20011} {
		if err := validateLocalNodePort(node, port); err == nil {
			t.Fatalf("F-016: gap port %d must be rejected by the segment set, not the min-max span", port)
		}
	}

	// 单段规格行为不变；远程节点与空规格保持放行（既有语义）。
	single := &nodeRecord{Name: "single", PortRange: "30000-30010"}
	if err := validateLocalNodePort(single, 30005); err != nil {
		t.Fatalf("single-segment in-range port must pass: %v", err)
	}
	if err := validateLocalNodePort(single, 30011); err == nil {
		t.Fatal("single-segment out-of-range port must fail")
	}
	remote := &nodeRecord{Name: "remote", PortRange: "10000-10010,20000-20010", IsRemote: 1}
	if err := validateLocalNodePort(remote, 15000); err != nil {
		t.Fatalf("remote nodes are validated by the peer, must stay permissive: %v", err)
	}
	empty := &nodeRecord{Name: "empty", PortRange: ""}
	if err := validateLocalNodePort(empty, 15000); err != nil {
		t.Fatalf("empty spec must stay permissive: %v", err)
	}
}
