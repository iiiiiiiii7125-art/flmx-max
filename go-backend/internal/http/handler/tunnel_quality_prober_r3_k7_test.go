package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Round 3 K7 回归（B07-001/B07-002）：
//   - B07-001：最优出口确认新鲜度窗口必须覆盖一个完整探测周期
//     （间隔 + 整轮耗时）；慢轮节奏下连续确认可累积并在第 3 轮
//     切换，陈旧确认仍被作废（B07-003 语义不回退）。
//   - B07-002：最优出口与质量/诊断面的探测目标解析收敛为同一
//     实现，两入口对全部地址形态逐项等价，不得再分叉。

// B07-001：窗口推导 = 2×(间隔 + 上一整轮耗时)；尚无整轮记录时
// 退化为旧形态 2×间隔（默认间隔 1s → 2s）。
func TestR3K7PendingFreshWindowCoversFullPeriod(t *testing.T) {
	p := &tunnelQualityProber{} // handler 为 nil：probeInterval 回退默认 1s
	if got := p.pendingFreshWindow(); got != 2*time.Second {
		t.Fatalf("no round recorded: window = %v, want 2s (2x default interval)", got)
	}
	atomic.StoreInt64(&p.lastRoundDurationNs, int64(2*time.Second))
	if got := p.pendingFreshWindow(); got != 6*time.Second {
		t.Fatalf("2s round recorded: window = %v, want 6s (2x(1s interval + 2s round))", got)
	}
	atomic.StoreInt64(&p.lastRoundDurationNs, int64(5500*time.Millisecond))
	if got := p.pendingFreshWindow(); got != 13*time.Second {
		t.Fatalf("5.5s round recorded: window = %v, want 13s (2x(1s + 5.5s))", got)
	}
}

// B07-001：慢轮节奏（周期 3s = 1s 间隔 + 2s 整轮）下，按新推导
// 注入的窗口（6s）必须让确认累积、第 3 轮达成切换——这正是修前
// 注入窗口 2s 时永不切换的场景（对照见 lanes/B07/r3 的 overlay
// 实证）。同时断言陈旧语义保留：间隔拉大到超过窗口仍作废重数。
func TestR3K7BestExitConfirmsAcrossSlowRounds(t *testing.T) {
	candidate := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 20, NodeName: "cand"}, 10, 0, 10, 0)
	current := scoreBestExitCandidate(1, chainNodeRecord{NodeID: 10, NodeName: "cur"}, 30, 0, 40, 0)
	scores := []bestExitCandidateScore{current, candidate}

	p := &tunnelQualityProber{}
	atomic.StoreInt64(&p.lastRoundDurationNs, int64(2*time.Second))
	window := p.pendingFreshWindow()

	m := newBestExitManager()
	m.setPendingFreshWindow(window)
	key := bestExitOwnerKey{TunnelID: 7, OwnerNodeID: 1}
	t0 := time.Now()
	m.setApplied(key, 10, t0)
	base := t0.Add(bestExitSwitchCooldown + time.Second)

	const period = 3 * time.Second // 1s 间隔 + 2s 整轮，修前恒大于注入窗口 2s
	if dec := m.observeScores(key, scores, base); dec.Switch {
		t.Fatalf("round 1 must not switch: %+v", dec)
	}
	if got := m.decisions[key].PendingCount; got != 1 {
		t.Fatalf("round 1: pending count = %d, want 1", got)
	}
	if dec := m.observeScores(key, scores, base.Add(period)); dec.Switch {
		t.Fatalf("round 2 must not switch: %+v", dec)
	}
	if got := m.decisions[key].PendingCount; got != 2 {
		t.Fatalf("round 2: pending count = %d, want 2 (accumulates across slow rounds)", got)
	}
	dec := m.observeScores(key, scores, base.Add(2*period))
	if !dec.Switch || dec.ExitNodeID != 20 {
		t.Fatalf("round 3 must switch to 20 under the period-covering window, got %+v", dec)
	}

	// 陈旧确认仍作废：新一串确认中出现超过窗口的间隔 → 重数。
	m.setApplied(key, 10, t0)
	stale := base.Add(10 * period)
	m.observeScores(key, scores, stale)
	if dec := m.observeScores(key, scores, stale.Add(window+time.Second)); dec.Switch {
		t.Fatalf("stale confirmation must not splice into a switch: %+v", dec)
	}
	if got := m.decisions[key].PendingCount; got != 1 {
		t.Fatalf("stale gap must reset pending count to 1, got %d", got)
	}
}

// B07-001：probeAll 跑完一轮后须把整轮耗时记入 lastRoundDurationNs，
// 供下一轮 probeBestExitOwners 推导窗口（计时落点回归）。
func TestR3K7ProbeAllRecordsRoundDuration(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 307, "quality-r3-k7-round", "speed.example.com", 8443)

	p := newTunnelQualityProber(h)
	// probeAll 的派发循环读 p.ctx（生产由 Start 注入）；直调须自备，
	// 否则 nil ctx 在 select 处 panic（首跑实测教训）。
	p.ctx = context.Background()
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		time.Sleep(60 * time.Millisecond)
		return 10, 0, nil
	}
	p.probeAll()

	got := time.Duration(atomic.LoadInt64(&p.lastRoundDurationNs))
	if got < 60*time.Millisecond {
		t.Fatalf("probeAll must record the round duration (>=60ms probe), got %v", got)
	}
	if window := p.pendingFreshWindow(); window <= 2*time.Second {
		t.Fatalf("window must grow beyond 2x interval once a round is recorded, got %v", window)
	}
}

// B07-002：双解析器对照——同一地址样本集在 resolveChainProbeTarget
// 与 resolveBestExitProbeTarget 下 (host, port, 是否出错) 必须逐项
// 一致，且关键样本的期望值钉死（防两边一起改坏）。
func TestR3K7ProbeTargetResolversAgree(t *testing.T) {
	v4From := &nodeRecord{ID: 1, Name: "from", ServerIP: "192.0.2.1", ServerIPv4: "192.0.2.1"}
	v6From := &nodeRecord{ID: 2, Name: "from6", ServerIP: "2001:db8::1", ServerIPv6: "2001:db8::1"}

	cases := []struct {
		name         string
		from         *nodeRecord
		to           *nodeRecord
		preferred    int
		ipPreference string
		connectIP    string
		wantHost     string // "" 表示只校验两解析器一致（且同成败）
		wantPort     int    // 0 表示不钉端口
		wantErr      bool
	}{
		{
			name:     "bare v4 ServerIP",
			from:     v4From,
			to:       &nodeRecord{ID: 30, ServerIP: "203.0.113.10"},
			wantHost: "203.0.113.10", wantPort: 443,
		},
		{
			name:     "ported ServerIP stripped by pick",
			from:     v4From,
			to:       &nodeRecord{ID: 30, ServerIP: "203.0.113.10:6366"},
			wantHost: "203.0.113.10", wantPort: 443,
		},
		{
			name:      "preferred port wins",
			from:      v4From,
			to:        &nodeRecord{ID: 30, ServerIP: "203.0.113.10:6366"},
			preferred: 30030,
			wantHost:  "203.0.113.10", wantPort: 30030,
		},
		{
			name:     "bracketed v6 with port",
			from:     v6From,
			to:       &nodeRecord{ID: 30, ServerIP: "[2001:db8::30]:6366"},
			wantHost: "2001:db8::30", wantPort: 443,
		},
		{
			name:     "hostname ServerIP",
			from:     v4From,
			to:       &nodeRecord{ID: 30, ServerIP: "node.example.com"},
			wantHost: "node.example.com", wantPort: 443,
		},
		{
			// B07-002 本体：v4 偏好下 selectTunnelDialHost 报错
			// （ServerIPv6 在场使 v4 不受支持），回退剥端口。
			name:         "fallback strips ported ServerIP on dial-host error",
			from:         v4From,
			to:           &nodeRecord{ID: 30, ServerIP: "10.0.0.30:8443", ServerIPv6: "2001:db8::30"},
			ipPreference: "v4",
			wantHost:     "10.0.0.30", wantPort: 443,
		},
		{
			name:     "nil from falls back to stripped ServerIP",
			from:     nil,
			to:       &nodeRecord{ID: 30, ServerIP: "203.0.113.10:6366"},
			wantHost: "203.0.113.10", wantPort: 443,
		},
		{
			name:      "ported connectIP stripped",
			from:      v4From,
			to:        &nodeRecord{ID: 30, ServerIP: "203.0.113.10"},
			connectIP: "198.51.100.7:8443",
			wantHost:  "198.51.100.7", wantPort: 443,
		},
		{
			name:     "port range first port when no preference",
			from:     v4From,
			to:       &nodeRecord{ID: 30, ServerIP: "203.0.113.10", PortRange: "45000-45010"},
			wantHost: "203.0.113.10", wantPort: 45000,
		},
		{
			name:    "no address at all fails in both",
			from:    v4From,
			to:      &nodeRecord{ID: 30},
			wantErr: true,
		},
		{
			name:    "nil target fails in both",
			from:    v4From,
			to:      nil,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chainHost, chainPort, chainErr := resolveChainProbeTarget(tc.from, tc.to, tc.preferred, tc.ipPreference, tc.connectIP)
			bestHost, bestPort, bestErr := resolveBestExitProbeTarget(tc.from, tc.to, tc.preferred, tc.ipPreference, tc.connectIP)
			if (chainErr != nil) != (bestErr != nil) {
				t.Fatalf("error divergence: chain err=%v, best-exit err=%v", chainErr, bestErr)
			}
			if chainHost != bestHost || chainPort != bestPort {
				t.Fatalf("result divergence: chain=%q:%d, best-exit=%q:%d", chainHost, chainPort, bestHost, bestPort)
			}
			if tc.wantErr {
				if chainErr == nil {
					t.Fatalf("expected resolution failure, got %q:%d", chainHost, chainPort)
				}
				return
			}
			if chainErr != nil {
				t.Fatalf("unexpected resolution failure: %v", chainErr)
			}
			if tc.wantHost != "" && chainHost != tc.wantHost {
				t.Fatalf("host = %q, want %q", chainHost, tc.wantHost)
			}
			if tc.wantPort != 0 && chainPort != tc.wantPort {
				t.Fatalf("port = %d, want %d", chainPort, tc.wantPort)
			}
		})
	}
}
