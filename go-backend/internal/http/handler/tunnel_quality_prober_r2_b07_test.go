package handler

import (
	"errors"
	"testing"
)

// Round 2 B07-001/B07-002 回归：快照两段量测值以 -1/100 哨兵起步，
// 未测到的段绝不以 0/0 落库/进缓存。覆盖五条非执行早退路径、
// 类型2 出口离线被门控跳过的 ExitToBing 段，以及类型1 成功时
// 不存在的「入口→出口」段（B07-002 后端数据口径根治）。

func r2B07SingleSnap(t *testing.T, p *tunnelQualityProber) tunnelQualitySnapshot {
	t.Helper()
	snaps := p.GetAll()
	if len(snaps) != 1 {
		t.Fatalf("expected one quality snapshot, got %+v", snaps)
	}
	return snaps[0]
}

func r2B07AssertAllSentinels(t *testing.T, snap tunnelQualitySnapshot) {
	t.Helper()
	if snap.Success {
		t.Fatalf("snapshot must be failed, got %+v", snap)
	}
	if snap.EntryToExitLatency != -1 || snap.EntryToExitLoss != 100 {
		t.Fatalf("entry->exit must be -1/100 sentinel, got lat=%v loss=%v", snap.EntryToExitLatency, snap.EntryToExitLoss)
	}
	if snap.ExitToBingLatency != -1 || snap.ExitToBingLoss != 100 {
		t.Fatalf("exit->target must be -1/100 sentinel, got lat=%v loss=%v", snap.ExitToBingLatency, snap.ExitToBingLoss)
	}
}

// 早退 1：隧道不存在。
func TestR2B07MissingTunnelWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	p := newTunnelQualityProber(h)
	p.probeTunnel(999999)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "隧道不存在" {
		t.Fatalf("expected missing-tunnel error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)

	// 落库行同口径（历史接口原样透传落库值）。
	var row struct {
		EntryToExitLatency float64
		ExitToBingLatency  float64
		EntryToExitLoss    float64
		ExitToBingLoss     float64
		Success            int
	}
	if err := h.repo.DB().Table("tunnel_quality").Where("tunnel_id = ?", 999999).Take(&row).Error; err != nil {
		t.Fatalf("load stored quality row: %v", err)
	}
	if row.Success != 0 || row.EntryToExitLatency != -1 || row.ExitToBingLatency != -1 || row.EntryToExitLoss != 100 || row.ExitToBingLoss != 100 {
		t.Fatalf("stored row must carry -1/100 sentinels, got %+v", row)
	}
}

// 早退 2：探测目标不允许。
func TestR2B07DisallowedTargetWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 301, "quality-r2-bad-target", "localhost", 80)

	p := newTunnelQualityProber(h)
	p.probeTunnel(301)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "探测目标地址不允许，已跳过质量探测" {
		t.Fatalf("expected disallowed-target error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)
}

// 早退 3：隧道配置不完整（无链路行）。
func TestR2B07IncompleteChainWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 302, "quality-r2-incomplete", "speed.example.com", 8443)
	if err := h.repo.DB().Exec(`DELETE FROM chain_tunnel WHERE tunnel_id = ?`, 302).Error; err != nil {
		t.Fatalf("delete chain rows: %v", err)
	}

	p := newTunnelQualityProber(h)
	p.probeTunnel(302)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "隧道配置不完整" {
		t.Fatalf("expected incomplete-chain error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)
}

// 早退 4：类型1 入口离线——唯一量测段 ExitToBing 被门控跳过，不得 0/0。
func TestR2B07Type1EntryOfflineWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 303, "quality-r2-t1-offline", "speed.example.com", 8443)
	if err := h.repo.DB().Exec(`UPDATE node SET status = 0 WHERE id = 10`).Error; err != nil {
		t.Fatalf("mark entry offline: %v", err)
	}

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 0, 100, errors.New("must not probe an offline entry")
	}
	p.probeTunnel(303)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "入口节点均不在线" {
		t.Fatalf("expected entry-offline error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)
}

// 早退 5：类型2 出口离线——EntryToExit 旧口径已哨兵，ExitToBing 此前
// 被 exitOnline 门控跳过留 0/0，本用例锁定其同为哨兵。
func TestR2B07Type2ExitOfflineWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedQualityForwardTunnel(t, h, 304, []int{0})

	p := newTunnelQualityProber(h)
	p.probeTunnel(304)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "出口节点均不在线" {
		t.Fatalf("expected exit-offline error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)
}

// 类型2 入口与出口双离线：两段均哨兵。
func TestR2B07Type2BothOfflineWritesSentinels(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedQualityForwardTunnel(t, h, 305, []int{0})
	if err := h.repo.DB().Exec(`UPDATE node SET status = 0 WHERE id = 10`).Error; err != nil {
		t.Fatalf("mark entry offline: %v", err)
	}

	p := newTunnelQualityProber(h)
	p.probeTunnel(305)

	snap := r2B07SingleSnap(t, p)
	if snap.ErrorMessage != "入口节点均不在线" {
		t.Fatalf("expected entry-offline error, got %+v", snap)
	}
	r2B07AssertAllSentinels(t, snap)
}

// B07-002：类型1 探测成功时 ExitToBing 为实测值，「入口→出口」段
// 不存在，必须保持 -1/100 哨兵（N/A），不得以 0/0 冒充完美段位。
func TestR2B07Type1SuccessHasNoPhantomEntryToExit(t *testing.T) {
	h := setupProbeTargetTunnelHandler(t)
	seedProbeTargetTunnel(t, h, 306, "quality-r2-t1-success", "speed.example.com", 8443)

	p := newTunnelQualityProber(h)
	p.probeNode = func(nodeID int64, ip string, port int, options diagnosisExecOptions) (float64, float64, error) {
		return 12.5, 0, nil
	}
	p.probeTunnel(306)

	snap := r2B07SingleSnap(t, p)
	if !snap.Success {
		t.Fatalf("snapshot must succeed, got %+v", snap)
	}
	if snap.ExitToBingLatency != 12.5 || snap.ExitToBingLoss != 0 {
		t.Fatalf("exit->target must keep measured values, got lat=%v loss=%v", snap.ExitToBingLatency, snap.ExitToBingLoss)
	}
	if snap.EntryToExitLatency != -1 || snap.EntryToExitLoss != 100 {
		t.Fatalf("type1 has no entry->exit segment; must stay -1/100 sentinel, got lat=%v loss=%v", snap.EntryToExitLatency, snap.EntryToExitLoss)
	}
}
