package handler

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go-backend/internal/ws"
)

// B07 回归：质量面消费 TcpPing 报文必须看 success 字段；切换确认
// 计数必须带新鲜度。以下面的真实 agent 全失败报文形态为桩
// （websocket_reporter.go handleTcpPing：全失败时 Success=false、
// AverageTime/PacketLoss 为零值、命令层 err=nil）。

func b07AgentFailPayload() map[string]interface{} {
	return map[string]interface{}{
		"ip":           "198.51.100.7",
		"port":         float64(443),
		"success":      false,
		"averageTime":  float64(0),
		"packetLoss":   float64(0),
		"errorMessage": "所有TCP连接尝试都失败",
	}
}

func b07StubProber(t *testing.T, payloadFor func(nodeID int64, data map[string]interface{}) map[string]interface{}) *tunnelQualityProber {
	t.Helper()
	orig := tcpPingSend
	tcpPingSend = func(_ *Handler, nodeID int64, data interface{}, _ time.Duration) (ws.CommandResult, error) {
		m, _ := data.(map[string]interface{})
		return ws.CommandResult{Type: "TcpPingResponse", Success: true, Data: payloadFor(nodeID, m)}, nil
	}
	t.Cleanup(func() { tcpPingSend = orig })

	h := &Handler{pingSlots: newNodePingSlots()}
	p := newTunnelQualityProber(h)
	p.roundActive = true
	p.roundNodes = map[int64]*nodeRecord{
		7:  {ID: 7, Name: "n7", Status: 1},
		10: {ID: 10, Name: "entry", Status: 1},
		30: {ID: 30, Name: "exit-a", Status: 1},
		31: {ID: 31, Name: "exit-b", Status: 1},
	}
	p.roundNodeErrs = map[int64]error{}
	return p
}

// B07-001 第一腿：tcpPingNode 对 success=false 报文必须返回 error，
// 不得产出 lat=0/loss=0/err=nil 的「完美样本」；success=true 与
// 缺失 success 字段的对照一并锁定。
func TestB07TcpPingNodeAgentAllFailPayloadReturnsError(t *testing.T) {
	p := b07StubProber(t, func(int64, map[string]interface{}) map[string]interface{} {
		return b07AgentFailPayload()
	})
	lat, loss, err := p.tcpPingNode(7, "198.51.100.7", 443, diagnosisExecOptions{})
	if err == nil {
		t.Fatalf("success=false payload must be a probe failure, got lat=%v loss=%v err=nil", lat, loss)
	}
	_ = loss
}

func TestB07TcpPingNodeSuccessPayloadPassesThrough(t *testing.T) {
	p := b07StubProber(t, func(int64, map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"ip":          "198.51.100.7",
			"port":        float64(443),
			"success":     true,
			"averageTime": float64(25),
			"packetLoss":  float64(0),
		}
	})
	lat, loss, err := p.tcpPingNode(7, "198.51.100.7", 443, diagnosisExecOptions{})
	if err != nil || lat != 25 || loss != 0 {
		t.Fatalf("success=true payload must pass values through, got lat=%v loss=%v err=%v", lat, loss, err)
	}
}

func TestB07TcpPingNodeMissingSuccessFieldIsFailure(t *testing.T) {
	p := b07StubProber(t, func(int64, map[string]interface{}) map[string]interface{} {
		return map[string]interface{}{
			"ip":          "198.51.100.7",
			"port":        float64(443),
			"averageTime": float64(0),
			"packetLoss":  float64(0),
		}
	})
	if _, _, err := p.tcpPingNode(7, "198.51.100.7", 443, diagnosisExecOptions{}); err == nil {
		t.Fatalf("payload without success field must not be read as a perfect sample")
	}
}

// B07-001 第二腿（P0 影响面）：死出口（agent 全失败报文）在最优出口
// 评分中必须是失败候选，连续多轮也不得被切换过去。
func TestB07DeadExitNeverWinsBestExitSwitch(t *testing.T) {
	p := b07StubProber(t, func(nodeID int64, data map[string]interface{}) map[string]interface{} {
		ip, _ := data["ip"].(string)
		if ip == "10.0.0.31" || nodeID == 31 {
			return b07AgentFailPayload()
		}
		avg := float64(30)
		if nodeID == 30 {
			avg = 40
		}
		return map[string]interface{}{
			"ip":          ip,
			"success":     true,
			"averageTime": avg,
			"packetLoss":  float64(0),
		}
	})

	owner := chainNodeRecord{NodeID: 10, NodeName: "entry"}
	exits := []chainNodeRecord{
		{NodeID: 30, NodeName: "exit-a", Port: 30030},
		{NodeID: 31, NodeName: "exit-b", Port: 30031},
	}
	nodes := map[int64]*nodeRecord{
		10: {ID: 10, Status: 1, ServerIP: "10.0.0.10", ServerIPv4: "10.0.0.10", TCPListenAddr: "[::]"},
		30: {ID: 30, Status: 1, ServerIP: "10.0.0.30", ServerIPv4: "10.0.0.30", TCPListenAddr: "[::]"},
		31: {ID: 31, Status: 1, ServerIP: "10.0.0.31", ServerIPv4: "10.0.0.31", TCPListenAddr: "[::]"},
	}
	scores := evaluateBestExitOwner(owner, exits, nodes, "", diagnosisExecOptions{}, defaultTunnelProbeTarget(), p.pingNode)
	if len(scores) != 2 {
		t.Fatalf("expected two scores, got %+v", scores)
	}
	if scores[0].ExitNodeID != 30 || !scores[0].Success {
		t.Fatalf("healthy exit must rank first and succeed, got %+v", scores)
	}
	dead := scores[1]
	if dead.ExitNodeID != 31 || dead.Success {
		t.Fatalf("dead exit must be a failed candidate, got %+v", dead)
	}

	m := newBestExitManager()
	key := bestExitOwnerKey{TunnelID: 99, OwnerNodeID: 10}
	t0 := time.Now()
	m.setApplied(key, 30, t0)
	for i := 1; i <= bestExitConfirmationRounds+2; i++ {
		dec := m.observeScores(key, scores, t0.Add(bestExitSwitchCooldown+time.Duration(i)*time.Second))
		if dec.Switch {
			t.Fatalf("round %d: must never switch to the dead exit, decision %+v", i, dec)
		}
	}
	if got := m.decisions[key].AppliedExitNodeID; got != 30 {
		t.Fatalf("applied exit must stay on the healthy exit, got %d", got)
	}
}

// B07-002：类型2 出口→目标段失败时 ExitToBing 必须写 -1/100 哨兵，
// 与入口段失败口径一致，不得留 0/0。
func TestB07Type2ExitToTargetFailureWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedQualityForwardTunnel(t, h, 91, []int{1})

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		switch fmt.Sprintf("%d|%s|%d", nodeID, ip, port) {
		case "10|10.0.0.30|30030":
			return 20, 0, nil
		default:
			return 0, 100, errors.New("bing unreachable")
		}
	}
	p.probeTunnel(91)

	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one quality snapshot, got %+v", snaps)
	}
	snap := snaps[0]
	if snap.Success {
		t.Fatalf("snapshot must be failed when exit->target fails, got %+v", snap)
	}
	if snap.EntryToExitLatency != 20 || snap.EntryToExitLoss != 0 {
		t.Fatalf("entry->exit segment must keep its real values, got %+v", snap)
	}
	if snap.ExitToBingLatency != -1 || snap.ExitToBingLoss != 100 {
		t.Fatalf("exit->target failure must write -1/100 sentinels, got lat=%v loss=%v", snap.ExitToBingLatency, snap.ExitToBingLoss)
	}
}

// B07-002：类型1 入口→目标失败同口径写 -1/100 哨兵。
func TestB07Type1TargetFailureWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 92, "quality-b07", "speed.example.com", 8443)

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 0, 100, errors.New("target unreachable")
	}
	p.probeTunnel(92)

	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one quality snapshot, got %+v", snaps)
	}
	snap := snaps[0]
	if snap.Success {
		t.Fatalf("snapshot must be failed, got %+v", snap)
	}
	if snap.ExitToBingLatency != -1 || snap.ExitToBingLoss != 100 {
		t.Fatalf("type1 failure must write -1/100 sentinels, got lat=%v loss=%v", snap.ExitToBingLatency, snap.ExitToBingLoss)
	}
}

// B07-003：相邻确认间隔超过新鲜度窗口时旧计数作废，本轮按第一轮
// 重数；窗口内连续 3 轮仍可正常确认切换。
func TestB07BestExitPendingExpiresBeyondFreshWindow(t *testing.T) {
	candidate := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 20, NodeName: "cand"}, 10, 0, 10, 0)
	current := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 10, NodeName: "cur"}, 30, 0, 40, 0)
	scores := []bestExitCandidateScore{current, candidate}

	m := newBestExitManager()
	key := bestExitOwnerKey{TunnelID: 99, OwnerNodeID: 1}
	t0 := time.Now()
	m.setApplied(key, 10, t0)
	base := t0.Add(bestExitSwitchCooldown + time.Second)

	if dec := m.observeScores(key, scores, base); dec.Switch {
		t.Fatalf("round 1 must not switch: %+v", dec)
	}
	// Gap beyond the fresh window: the stale confirmation is discarded.
	stale := base.Add(bestExitPendingFreshWindow + time.Second)
	if dec := m.observeScores(key, scores, stale); dec.Switch {
		t.Fatalf("stale round must restart the count, not switch: %+v", dec)
	}
	if got := m.decisions[key].PendingCount; got != 1 {
		t.Fatalf("stale round must reset pending count to 1, got %d", got)
	}
	if dec := m.observeScores(key, scores, stale.Add(time.Second)); dec.Switch {
		t.Fatalf("round 2 of the fresh sequence must not switch: %+v", dec)
	}
	dec := m.observeScores(key, scores, stale.Add(2*time.Second))
	if !dec.Switch || dec.ExitNodeID != 20 {
		t.Fatalf("round 3 of the fresh sequence must switch to 20, got %+v", dec)
	}
}

// B07-003：全失败轮清零待确认计数，故障前的 2 轮确认不得与恢复
// 后的 1 轮拼接成切换。
func TestB07BestExitAllFailedRoundClearsPending(t *testing.T) {
	candidate := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 20, NodeName: "cand"}, 10, 0, 10, 0)
	current := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 10, NodeName: "cur"}, 30, 0, 40, 0)
	scores := []bestExitCandidateScore{current, candidate}
	failed := []bestExitCandidateScore{
		failedBestExitCandidate(1, chainNodeRecord{NodeID: 10, NodeName: "cur"}, "down"),
		failedBestExitCandidate(1, chainNodeRecord{NodeID: 20, NodeName: "cand"}, "down"),
	}

	m := newBestExitManager()
	key := bestExitOwnerKey{TunnelID: 99, OwnerNodeID: 1}
	t0 := time.Now()
	m.setApplied(key, 10, t0)
	base := t0.Add(bestExitSwitchCooldown + time.Second)

	m.observeScores(key, scores, base)
	m.observeScores(key, scores, base.Add(time.Second))
	if got := m.decisions[key].PendingCount; got != 2 {
		t.Fatalf("expected pending count 2 before the failed round, got %d", got)
	}
	m.observeScores(key, failed, base.Add(2*time.Second))
	if got := m.decisions[key].PendingCount; got != 0 {
		t.Fatalf("all-failed round must clear pending count, got %d", got)
	}
	if dec := m.observeScores(key, scores, base.Add(3*time.Second)); dec.Switch {
		t.Fatalf("first post-failure win must not splice with pre-failure counts: %+v", dec)
	}
	m.observeScores(key, scores, base.Add(4*time.Second))
	if dec := m.observeScores(key, scores, base.Add(5*time.Second)); !dec.Switch || dec.ExitNodeID != 20 {
		t.Fatalf("third consecutive post-failure win must switch, got %+v", dec)
	}
}
