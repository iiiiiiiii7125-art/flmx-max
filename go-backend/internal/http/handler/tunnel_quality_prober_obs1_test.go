package handler

import (
	"context"
	"errors"
	"testing"
	"time"
)

// OBS-1 (R3-C)：隧道质量探测在断网期无退避——默认 1 秒间隔＋
// 约 6 秒整轮，每探针烧 2 秒 DNS＋5 秒拨号超时，agent 日志几
// 全是探针噪声。本测试钉死两点：退避延迟按失败连败指数拉长
// （有 5 分钟上限），且 probeAll 如实回报本轮成败（全败轮才
// 计入连败、成功轮清零）。

func TestTunnelQualityProbeBackoffDelay(t *testing.T) {
	p := &tunnelQualityProber{} // handler 为 nil：probeInterval 回退默认 1s
	cases := []struct {
		streak int
		want   time.Duration
	}{
		{streak: 0, want: 1 * time.Second},
		{streak: 1, want: 2 * time.Second},
		{streak: 2, want: 4 * time.Second},
		{streak: 3, want: 8 * time.Second},
		{streak: 7, want: 128 * time.Second},
		{streak: 8, want: 256 * time.Second},
		{streak: 9, want: 5 * time.Minute},
		{streak: 20, want: 5 * time.Minute},
	}
	for _, c := range cases {
		if got := p.backoffDelay(c.streak); got != c.want {
			t.Fatalf("failStreak=%d: backoffDelay = %v, want %v", c.streak, got, c.want)
		}
	}
}

func TestTunnelQualityProbeAllReportsRoundVerdict(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 308, "quality-obs1", "speed.example.com", 8443)

	p := newTunnelQualityProber(h)
	// probeAll 的派发循环读 p.ctx（生产由 Start 注入）；直调须自备，
	// 否则 nil ctx 在 select 处 panic。
	p.ctx = context.Background()

	// 全败轮：探针恒失败 → 探了 1 条、成功 0 条。
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 0, 0, errors.New("dial timeout (test fault)")
	}
	if probed, succeeded := p.probeAll(); probed != 1 || succeeded != 0 {
		t.Fatalf("all-fail round: probed=%d succeeded=%d, want 1/0", probed, succeeded)
	}
	// 再跑一轮仍全败——连败在 loop 层累积，此处只验每轮 verdict
	// 如实（旧轮成功快照不得把失败轮认作成功）。
	if probed, succeeded := p.probeAll(); probed != 1 || succeeded != 0 {
		t.Fatalf("second all-fail round: probed=%d succeeded=%d, want 1/0", probed, succeeded)
	}

	// 成功轮：探针通 → 探了 1 条、成功 1 条。
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 10, 0, nil
	}
	if probed, succeeded := p.probeAll(); probed != 1 || succeeded != 1 {
		t.Fatalf("success round: probed=%d succeeded=%d, want 1/1", probed, succeeded)
	}
}
