package handler

import (
	"encoding/json"
	"testing"
	"time"
)

// TestNodeListAttachesLatency — the node-latency display deadlock:
//
// the scheduled probe rounds store results in h.nodeLatency, but
// /node/list never carried them, so the node page could only show
// latency after a manual re-probe — whose button only exists once a
// result is already rendered. nodeList must attach, per node:
//
//   - latency: the latest store result, only when it has points;
//     shape identical to the manual /node/latency/probe response.
//   - latencyTargets: only when the node has an explicit stored
//     choice (including an explicit empty one, rendered as the
//     "不测延迟" pill). No choice means "follow the global defaults"
//     and the key must be omitted — the frontend treats a present
//     field as "自定义".
//   - No online/remote filtering in the handler: renderLatency on
//     the frontend owns that gate.
func TestNodeListAttachesLatency(t *testing.T) {
	targets := `[{"name":"广东电信","host":"gd-ct-v4.ip.zstaticcdn.com","port":80,"enabled":true,"pinned":true},{"name":"广东联通","host":"gd-cu-v4.ip.zstaticcdn.com","port":80,"enabled":true},{"name":"广东移动","host":"gd-cm-v4.ip.zstaticcdn.com","port":80,"enabled":true}]`
	h, r := setupLatencyHandler(t, targets)

	idA := mustCreateLatencyNode(t, r, "node-a", 1, 0)
	idB := mustCreateLatencyNode(t, r, "node-b", 1, 0)
	idC := mustCreateLatencyNode(t, r, "node-c", 1, 0)
	idD := mustCreateLatencyNode(t, r, "node-d", 0, 0)

	if err := r.SetNodesLatencyTargets([]int64{idB}, `{"points":["广东电信"],"pinned":["广东电信"]}`); err != nil {
		t.Fatalf("set choice B: %v", err)
	}
	if err := r.SetNodesLatencyTargets([]int64{idC}, `{"points":[],"pinned":[]}`); err != nil {
		t.Fatalf("set choice C: %v", err)
	}

	now := time.Now().UnixMilli()
	h.nodeLatency.replace(map[int64]externalProbe{
		idA: {
			OK: true, LatencyMs: 23, CheckedAt: now,
			Points: []latencyPoint{
				{Name: "广东电信", Pinned: true, OK: true, LatencyMs: 23},
				{Name: "广东联通", OK: false, Error: "节点响应超时"},
			},
		},
		idD: {
			OK: true, LatencyMs: 41, CheckedAt: now,
			Points: []latencyPoint{
				{Name: "广东电信", Pinned: true, OK: true, LatencyMs: 41},
			},
		},
	}, false)

	res := postJSONToHandler(t, h.nodeList, map[string]interface{}{})
	var env struct {
		Code int                      `json:"code"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, res.Body.String())
	}
	if env.Code != 0 {
		t.Fatalf("node list code=%d body=%s", env.Code, res.Body.String())
	}
	byID := make(map[int64]map[string]interface{}, len(env.Data))
	for _, item := range env.Data {
		id, _ := item["id"].(float64)
		byID[int64(id)] = item
	}
	mustHave := func(id int64) map[string]interface{} {
		t.Helper()
		item, ok := byID[id]
		if !ok {
			t.Fatalf("node %d missing from list", id)
		}
		return item
	}

	// A: result attached, no explicit choice -> no latencyTargets key.
	itemA := mustHave(idA)
	latA, ok := itemA["latency"].(map[string]interface{})
	if !ok {
		t.Fatalf("A: latency missing: %v", itemA)
	}
	if latA["ok"] != true || latA["checkedAt"] != float64(now) {
		t.Fatalf("A: latency = %v, want ok=true checkedAt=%d", latA, now)
	}
	pointsA, _ := latA["points"].([]interface{})
	if len(pointsA) != 2 {
		t.Fatalf("A: points = %v, want 2", latA["points"])
	}
	if p0, _ := pointsA[0].(map[string]interface{}); p0["name"] != "广东电信" || p0["latencyMs"] != float64(23) {
		t.Fatalf("A: point 0 = %v, want 广东电信/23ms", p0)
	}
	if _, hasTargets := itemA["latencyTargets"]; hasTargets {
		t.Fatalf("A: latencyTargets must be omitted without an explicit choice: %v", itemA["latencyTargets"])
	}

	// B: explicit choice attached, no result -> no latency key.
	itemB := mustHave(idB)
	tgtB, ok := itemB["latencyTargets"].(map[string]interface{})
	if !ok {
		t.Fatalf("B: latencyTargets missing: %v", itemB)
	}
	ptsB, _ := tgtB["points"].([]interface{})
	if len(ptsB) != 1 || ptsB[0] != "广东电信" {
		t.Fatalf("B: latencyTargets.points = %v, want [广东电信]", tgtB["points"])
	}
	pinB, _ := tgtB["pinned"].([]interface{})
	if len(pinB) != 1 || pinB[0] != "广东电信" {
		t.Fatalf("B: latencyTargets.pinned = %v, want [广东电信]", tgtB["pinned"])
	}
	if _, hasLatency := itemB["latency"]; hasLatency {
		t.Fatalf("B: latency must be omitted without a result: %v", itemB["latency"])
	}

	// C: explicit empty choice attached (the "不测延迟" pill case).
	itemC := mustHave(idC)
	tgtC, ok := itemC["latencyTargets"].(map[string]interface{})
	if !ok {
		t.Fatalf("C: latencyTargets missing for explicit empty choice: %v", itemC)
	}
	ptsC, _ := tgtC["points"].([]interface{})
	if len(ptsC) != 0 {
		t.Fatalf("C: latencyTargets.points = %v, want []", tgtC["points"])
	}
	if _, hasLatency := itemC["latency"]; hasLatency {
		t.Fatalf("C: latency must be omitted without a result")
	}

	// D: offline node still gets its result attached; the frontend
	// owns the online gate, the handler must not fork it.
	itemD := mustHave(idD)
	if _, ok := itemD["latency"].(map[string]interface{}); !ok {
		t.Fatalf("D: latency missing for offline node: %v", itemD)
	}
}
