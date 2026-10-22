package handler

// R1 审计修复批回归（R1-B-01 / R1-B-02 / R1-B-04）：
//   - 入站页手动「全量检测」（空 ids）不得累加或清除 inbound_down
//     告警 streak（DB-4 在入站面的空 ids 后门）；
//   - 中转页「全部探测」（空 ids）的延迟预刷必须收口到中转入口节点
//     集合，不得触发全量延迟轮与延迟告警计数；
//   - setNodeLatencyTargets 空 ids 在 handler 入口已被拒绝（同族
//     收口现状钉住，R1-B-02 附注经复核为入口守卫已在位）；
//   - 内核单独重启（节点保持在线）的状态轮询检测（noteKernelStart）。

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"go-backend/internal/store/model"
)

func externalStreakState(h *Handler, id int64) (int, bool) {
	h.externalProbes.mu.Lock()
	defer h.externalProbes.mu.Unlock()
	return h.externalProbes.failStreak[id], h.externalProbes.down[id]
}

// R1-B-01（累加面）：定时轮把某导入入站打到 down 后，入站页手动
// 全量点击（空 ids）反复触发，streak 与 down 标记必须纹丝不动；
// 定时轮自身则继续计数（行为不变）。
func TestInboundProbeEmptyIDsDoesNotCountStreak(t *testing.T) {
	h, r := setupLatencyHandler(t, "")
	ctx := context.Background()
	row := &model.NodeInbound{Name: "manual-full", Protocol: "ss", Listen: "127.0.0.1", Port: 9,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	for i := 0; i < notifyInboundDownStreak; i++ {
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}
	streak, down := externalStreakState(h, row.ID)
	if streak != notifyInboundDownStreak || !down {
		t.Fatalf("scheduled rounds must mark down: streak=%d down=%v", streak, down)
	}
	for i := 0; i < 3; i++ {
		res := postJSONToHandler(t, h.inboundProbe, map[string]interface{}{"ids": []int64{}})
		if out := decodeCode(t, res.Body.String()); out.Code != 0 {
			t.Fatalf("manual full probe must succeed: %s", res.Body.String())
		}
	}
	streak, down = externalStreakState(h, row.ID)
	if streak != notifyInboundDownStreak || !down {
		t.Fatalf("manual full probes touched alert state: streak=%d down=%v", streak, down)
	}
	if p, _ := h.externalProbes.get(row.ID); p.OK {
		t.Fatal("manual probe must still refresh the cache (target keeps failing)")
	}
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	streak, _ = externalStreakState(h, row.ID)
	if streak != notifyInboundDownStreak+1 {
		t.Fatalf("scheduled rounds must keep counting: streak=%d", streak)
	}
}

// R1-B-01（清除面）：down 之后目标恢复，手动全量点击只能刷新缓存，
// 不得产生恢复跃迁（清 streak/down 与问题指纹）；恢复跃迁仍归定时轮。
func TestInboundProbeEmptyIDsDoesNotClearDown(t *testing.T) {
	probeAddr := probeFixtureLocalAddr(t)
	h, r := setupLatencyHandler(t, "")
	ctx := context.Background()
	ln, err := net.Listen("tcp", probeAddr+":0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	row := &model.NodeInbound{Name: "manual-clear", Protocol: "ss", Listen: probeAddr, Port: port,
		Enabled: 1, CreatedTime: time.Now().UnixMilli(), UpdatedTime: time.Now().UnixMilli()}
	if err := r.DB().Create(row).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	if p, _ := h.externalProbes.get(row.ID); !p.OK {
		t.Fatalf("live target must probe OK, got %+v", p)
	}
	_ = ln.Close()
	for i := 0; i < notifyInboundDownStreak; i++ {
		if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
			t.Fatal(err)
		}
	}
	streak, down := externalStreakState(h, row.ID)
	if streak != notifyInboundDownStreak || !down {
		t.Fatalf("scheduled rounds must mark down: streak=%d down=%v", streak, down)
	}
	ln2, err := net.Listen("tcp", probeAddr+":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("relisten: %v", err)
	}
	defer ln2.Close()
	res := postJSONToHandler(t, h.inboundProbe, map[string]interface{}{"ids": []int64{}})
	if out := decodeCode(t, res.Body.String()); out.Code != 0 {
		t.Fatalf("manual full probe must succeed: %s", res.Body.String())
	}
	if p, _ := h.externalProbes.get(row.ID); !p.OK {
		t.Fatal("manual probe must refresh the cache to OK")
	}
	streak, down = externalStreakState(h, row.ID)
	if streak != notifyInboundDownStreak || !down {
		t.Fatalf("manual full probe cleared alert state: streak=%d down=%v", streak, down)
	}
	if _, err := h.probeExternalInbounds(ctx, nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	streak, down = externalStreakState(h, row.ID)
	if streak != 0 || down {
		t.Fatalf("scheduled round must produce the recovery transition: streak=%d down=%v", streak, down)
	}
}

// R1-B-02：中转页「全部探测」（空 ids）的延迟预刷只许测中转入口
// 节点，且不计延迟告警 streak；无关节点不得被测、不得有缓存条目。
// 定时全量延迟轮（probeNodeLatency 的 probeRoundScheduled 轮）的计数行为不变。
func TestRelayProbeEmptyIDsStaysTargeted(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	entry := mustCreateRelayNode(t, r, "entry", "198.51.100.1")
	other := mustCreateRelayNode(t, r, "other", "198.51.100.2")
	mustCreateRelayLine(t, r, "line", 1, "203.0.113.1:443", entry, nil, 0)
	var mu sync.Mutex
	var probed []int64
	stubLatencyPing(t, func(nodeID int64, _ string) (map[string]interface{}, error) {
		mu.Lock()
		probed = append(probed, nodeID)
		mu.Unlock()
		return nil, errors.New("测试点连接失败")
	})
	for i := 0; i < latencyAlertStreak+1; i++ {
		res := postJSONToHandler(t, h.inboundRelayProbe, map[string]interface{}{"ids": []int64{}})
		if out := decodeCode(t, res.Body.String()); out.Code != 0 {
			t.Fatalf("relay probe-all must succeed: %s", res.Body.String())
		}
	}
	mu.Lock()
	probedCopy := append([]int64(nil), probed...)
	mu.Unlock()
	if len(probedCopy) == 0 {
		t.Fatal("the relay entry node must be pre-measured")
	}
	for _, n := range probedCopy {
		if n != entry {
			t.Fatalf("relay probe-all measured an unrelated node: %d", n)
		}
	}
	if _, ok := h.nodeLatency.get(other); ok {
		t.Fatal("unrelated node gained a latency cache entry from relay probe-all")
	}
	h.latencyAlerts.mu.Lock()
	streakKeys, downKeys := len(h.latencyAlerts.streak), len(h.latencyAlerts.down)
	h.latencyAlerts.mu.Unlock()
	if streakKeys != 0 || downKeys != 0 {
		t.Fatalf("manual relay probe-all counted latency alert state: streak=%d down=%d", streakKeys, downKeys)
	}
	if _, err := h.probeNodeLatency(context.Background(), nil, probeRoundScheduled); err != nil {
		t.Fatal(err)
	}
	h.latencyAlerts.mu.Lock()
	streakKeys = len(h.latencyAlerts.streak)
	h.latencyAlerts.mu.Unlock()
	if streakKeys != 2 {
		t.Fatalf("scheduled full latency round must count both nodes, streak keys=%d", streakKeys)
	}
}

// R1-B-02 同族：setNodeLatencyTargets 的空 ids 在 handler 入口即被
// 拒绝（请求参数错误），不落库、不触发任何探测轮——钉住现状，防
// 后续重构把守卫弄丢后以空集调 probeNodeLatency 触发全量轮+计数。
func TestNodeLatencyTargetsSetEmptyIDsRejected(t *testing.T) {
	h, r := setupLatencyHandler(t, `[{"name":"电信","host":"ct.example.com","port":80,"enabled":true}]`)
	node := mustCreateLatencyNode(t, r, "n1", 1, 0)
	res := postJSONToHandler(t, h.nodeLatencyTargetsSet, map[string]interface{}{"ids": []int64{}, "custom": false})
	if out := decodeCode(t, res.Body.String()); out.Code == 0 {
		t.Fatalf("empty ids must be rejected: %s", res.Body.String())
	}
	if _, ok := h.nodeLatency.get(node); ok {
		t.Fatal("empty-ids save triggered a latency probe round")
	}
	nodes, err := r.ListMonitorNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes: %v %v", nodes, err)
	}
	if nodes[0].LatencyTargets != "" {
		t.Fatalf("empty-ids save wrote targets: %q", nodes[0].LatencyTargets)
	}
}

// R1-B-04：noteKernelStart 的基线/重启判定——首报只立基线、状态
// 不变不触发、重启计数增长触发、agent 进程重启（计数归零、
// StartedAt 前移）仍触发、未运行不触发且基线随停转推进、老 agent
// 不报 StartedAt 时仅凭计数判定。
func TestNoteKernelStartDetectsRestarts(t *testing.T) {
	h := &Handler{}
	run := agentKernelState{Type: "sing-box", Running: true, Restarts: 1, StartedAt: 1000}
	if h.noteKernelStart(7, run) {
		t.Fatal("first report must only establish the baseline")
	}
	if h.noteKernelStart(7, run) {
		t.Fatal("unchanged report must not fire")
	}
	restarted := run
	restarted.Restarts = 2
	if !h.noteKernelStart(7, restarted) {
		t.Fatal("restart counter growth must fire")
	}
	if h.noteKernelStart(7, restarted) {
		t.Fatal("the same restarted state must not refire")
	}
	agentRestart := agentKernelState{Type: "sing-box", Running: true, Restarts: 0, StartedAt: 2000}
	if !h.noteKernelStart(7, agentRestart) {
		t.Fatal("startedAt moving (agent restart resets the counter) must fire")
	}
	stopped := agentKernelState{Type: "sing-box", Running: false, Restarts: 0, StartedAt: 2000}
	if h.noteKernelStart(7, stopped) {
		t.Fatal("a stopped kernel must not fire")
	}
	if h.noteKernelStart(8, run) {
		t.Fatal("another node's first report is a baseline")
	}
	other := agentKernelState{Type: "xray", Running: true, Restarts: 5, StartedAt: 3000}
	if h.noteKernelStart(7, other) {
		t.Fatal("another kernel's first report is a baseline")
	}
	legacy := agentKernelState{Type: "legacy", Running: true, Restarts: 1}
	if h.noteKernelStart(7, legacy) {
		t.Fatal("legacy first report is a baseline")
	}
	legacy.Restarts = 3
	if !h.noteKernelStart(7, legacy) {
		t.Fatal("restart counter alone (no startedAt reported) must fire")
	}
}
