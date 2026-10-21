package handler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 契约（3.3.28 双栈并入，我方口径）：旧 agent 忽略 family 参数。
// 新面板对旧 agent 发 {"host","family":6}，旧 agent 的结构体没有该字段
// （Go json 默认忽略未知键），照常以 IPv4 回答——面板必须丢弃这条假 v6
// 腿：不挂 IPv6 子结构、不产出 route_change、不置 failed。
func TestTraceNodeRoutesOldAgentDegradesWithoutIPv6(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	node := mustCreateLatencyNode(t, r, "old-agent", 1, 0)
	old := nodeTraceroute
	t.Cleanup(func() { nodeTraceroute = old })
	// 旧 agent：无视 family，一律回 IPv4 结果。
	nodeTraceroute = func(_ *Handler, _ int64, _ string, _ int) (map[string]interface{}, error) {
		return map[string]interface{}{"ip": "203.0.113.9", "reached": true,
			"hops": []interface{}{map[string]interface{}{"ttl": 1.0, "ip": "59.43.180.1"}}}, nil
	}

	first, err := h.traceNodeRoutes(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	got := first[node]
	if got.failed {
		t.Fatalf("old agent trace must not be marked failed: %+v", got)
	}
	if got.Points[0].IPv6 != nil {
		t.Fatalf("old agent IPv4 answer must be dropped as IPv6 leg: %+v", got.Points[0].IPv6)
	}
	p := h.withRoutes(node, latencyProbe([]latencyPoint{{Name: "电信", OK: true}}, 1))
	if p.Points[0].RouteV6 != "" {
		t.Fatalf("old agent must not produce routeV6: %+v", p.Points[0])
	}

	second, err := h.traceNodeRoutes(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if changes := routeChanges(first[node], second[node]); len(changes) != 0 {
		t.Fatalf("old agent must not produce route changes, got %v", changes)
	}
}

// 契约（3.3.28 双栈并入，铁律口径）：v6 腿失败对上层完全静默。
// 无 IPv6 的节点每轮都会「失败」一次 v6 腿——若并入 r.failed，这类节点
// 会被打进 6h 重试档并反复空测。断言 failed 恒 false 且新鲜度仍按 24h
// 档判定（7h 时仍新鲜；用失败档 6h 判定则应为陈旧）。
func TestTraceNodeRoutesIPv6LegFailureKeepsFreshness(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	node := mustCreateLatencyNode(t, r, "v4-only", 1, 0)
	old := nodeTraceroute
	t.Cleanup(func() { nodeTraceroute = old })
	nodeTraceroute = func(_ *Handler, _ int64, _ string, family int) (map[string]interface{}, error) {
		if family == 6 {
			return nil, errors.New("节点没有 IPv6 路由")
		}
		return map[string]interface{}{"ip": "203.0.113.9", "reached": true,
			"hops": []interface{}{map[string]interface{}{"ttl": 1.0, "ip": "59.43.180.1"}}}, nil
	}

	results, err := h.traceNodeRoutes(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	got := results[node]
	if got.Points[0].IPv6 != nil {
		t.Fatalf("failed IPv6 leg must leave no leg: %+v", got.Points[0].IPv6)
	}
	if got.failed {
		t.Fatalf("IPv6 leg failure must not mark the trace failed: %+v", got)
	}
	targets := []latencyTarget{{Name: "电信", Host: "ct.example.com", Port: 80, Enabled: true}}
	checked := time.UnixMilli(got.CheckedAt)
	if !routeFresh(got, targets, got.agent, checked.Add(7*time.Hour)) {
		t.Fatal("trace with only an IPv6-leg failure must stay on the 24h freshness interval")
	}
	if routeFresh(got, targets, got.agent, checked.Add(25*time.Hour)) {
		t.Fatal("24h freshness bound must still apply")
	}
}
