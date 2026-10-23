package handler

import (
	"errors"
	"fmt"
	"testing"
)

// R5X-025：隧道质量快照的四处 err 落串（probeTunnel 的 type-1 主探、
// type-2 主链跳、type-2 出口→公网、default 主探）必须先过
// latencyCommandError 归类，与延迟面（latencyPointFrom）显示逐字一致，
// 不得原样落原始错误串。字面量期望与延迟面归类表同源（见
// TestLatencyPointPreservesFailureReason / R3-B-01 用例）。
func TestR5X025TunnelQualitySnapshotErrorClassification(t *testing.T) {
	assertClassified := func(t *testing.T, p *tunnelQualityProber, raw error, want string) {
		t.Helper()
		snaps := p.GetAll()
		if len(snaps) != 1 {
			t.Fatalf("expected one quality snapshot, got %+v", snaps)
		}
		got := snaps[0].ErrorMessage
		if got != want {
			t.Fatalf("snapshot ErrorMessage = %q, want classified %q (raw %q)", got, want, raw.Error())
		}
		// 与延迟面显示逐字对照：同一 raw error 经 latencyCommandError
		// 与经 latencyPointFrom（延迟点位实际显示路径）必须同串。
		if viaFunc := latencyCommandError(raw); got != viaFunc {
			t.Fatalf("snapshot ErrorMessage = %q, latencyCommandError = %q, must match verbatim", got, viaFunc)
		}
		if viaPoint := latencyPointFrom(latencyTarget{Name: "对照"}, nil, raw).Error; got != viaPoint {
			t.Fatalf("snapshot ErrorMessage = %q, latency point Error = %q, must match verbatim", got, viaPoint)
		}
	}

	t.Run("type1 entry to public target", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedProbeTargetTunnel(t, h, 91, "r5x025-type1", "speed.example.com", 8443)
		raw := errors.New("TCP探测任务过多")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			return 0, 100, raw
		}
		p.probeTunnel(91)
		assertClassified(t, p, raw, "节点繁忙，请稍后重试")
	})

	t.Run("type2 primary hop", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedQualityForwardTunnel(t, h, 92, []int{1})
		raw := errors.New("节点连接已断开")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			if fmt.Sprintf("%d|%s|%d", nodeID, ip, port) == "10|10.0.0.30|30030" {
				return 0, 100, raw
			}
			return 10, 0, nil
		}
		p.probeTunnel(92)
		assertClassified(t, p, raw, "节点离线")
	})

	t.Run("type2 exit to public target", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedQualityForwardTunnel(t, h, 93, []int{1})
		raw := errors.New("等待节点响应超时")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			if fmt.Sprintf("%d|%s|%d", nodeID, ip, port) == "30|www.bing.com|443" {
				return 0, 100, raw
			}
			return 10, 0, nil
		}
		p.probeTunnel(93)
		assertClassified(t, p, raw, "节点响应超时")
	})

	t.Run("default type entry to public target", func(t *testing.T) {
		h := setupProbeTargetTunnelHandler(t)
		seedProbeTargetTunnel(t, h, 94, "r5x025-default", "speed.example.com", 8443)
		if err := h.repo.DB().Exec(`UPDATE tunnel SET type = 3 WHERE id = 94`).Error; err != nil {
			t.Fatalf("set unknown tunnel type: %v", err)
		}
		raw := errors.New("Agent is shutting down")
		p := newTunnelQualityProber(h)
		p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
			return 0, 100, raw
		}
		p.probeTunnel(94)
		assertClassified(t, p, raw, "探测已取消")
	})
}
