package ws

import (
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// C4-G2 跨侧契约门禁·面板消费半（终局帧字段对齐）。
//
// 契约：UninstallResult 终局帧是 agent CommandResponse 正形——
// success 与失败原因（message）在帧顶层，data 只载 issuedAt 等
// 幂等键与明细。以下两段 golden 与 agent 生产半逐字一致（源头
// ＝go-gost/x/socket/uninstall_final_contract_test.go，字节由
// buildUninstallFinalResponse 本体产出、与 LIVE 证据
// 28-c4g2-state.json 同源）。本门禁把 agent 真帧喂进面板真解码
// 路径 handleUninstallResult：基线解码只读 data.success/
// data.reason，两格全红（成功被误记 failed、失败原因落「未附
// 原因」）；这正是 LIVE 两轮复现的缺陷本体，不许再用面板自造
// 帧替代真帧作证。
const uninstallFinalContractIssuedAt = int64(1794317291868)

const uninstallFinalSuccessGolden = `{"type":"UninstallResult","success":true,"message":"","data":{"failedStep":"","issuedAt":1794317291868,"requestId":"34_1794317291871470883","state":"succeeded","steps":[{"step":"restore-tuning","ok":true},{"step":"cleanup-host-tuning-files","ok":true},{"step":"uninstall-kernels","ok":true},{"step":"locate-binary","ok":true},{"step":"wipe-data-dir","ok":true}],"uninstallKey":"1794317291868"}}`

const uninstallFinalFailureGolden = `{"type":"UninstallResult","success":false,"message":"step uninstall-kernels failed: kernel uninstall exited: boom","data":{"failedStep":"uninstall-kernels","issuedAt":1794317291868,"requestId":"34_1794317291871470883","state":"failed","steps":[{"step":"restore-tuning","ok":true},{"step":"cleanup-host-tuning-files","ok":true},{"step":"uninstall-kernels","ok":false,"error":"kernel uninstall exited: boom"}],"uninstallKey":"1794317291868"}}`

const uninstallFinalFailureReason = "step uninstall-kernels failed: kernel uninstall exited: boom"

func uninstallFinalSeed(t *testing.T, r *repo.Repository, nodeID int64) {
	t.Helper()
	if err := r.UpsertNodePendingUninstall(nodeID, time.UnixMilli(uninstallFinalContractIssuedAt)); err != nil {
		t.Fatalf("seed pending uninstall: %v", err)
	}
	// 登记推进到 acked（受理回执已回、等终局）的生产形态。
	if won, err := r.TransitionNodePendingUninstall(nodeID,
		[]string{repo.NodePendingUninstallIssued}, repo.NodePendingUninstallAcked, "", time.Now()); err != nil || !won {
		t.Fatalf("seed acked transition: won=%v err=%v", won, err)
	}
}

// TestUninstallResultContractAgentSuccessFrame：agent 真成功帧
// → 登记迁 succeeded（其后删记录支由 handler 清扫承接，见
// handler 包 r9 的 sweep 覆盖：succeeded 登记经
// sweepNodePendingUninstalls 删除节点行）。
func TestUninstallResultContractAgentSuccessFrame(t *testing.T) {
	r := uninstallRepo(t)
	s := NewServer(r, "jwt-secret")
	const nodeID = 21
	uninstallFinalSeed(t, r, nodeID)
	s.handleUninstallResult(nodeID, uninstallFinalSuccessGolden)
	row, err := r.GetNodePendingUninstall(nodeID)
	if err != nil || row == nil {
		t.Fatalf("read pending uninstall: row=%v err=%v", row, err)
	}
	if row.State != repo.NodePendingUninstallSucceeded {
		t.Fatalf("agent success frame must land succeeded, got %q (reason %q)", row.State, row.Reason)
	}
}

// TestUninstallResultContractAgentFailureFrame：agent 真失败帧
// → 登记迁 failed，且 agent 在顶层 message 点名的原因逐字落账
// （不许落「未附原因」泛化文案）。
func TestUninstallResultContractAgentFailureFrame(t *testing.T) {
	r := uninstallRepo(t)
	s := NewServer(r, "jwt-secret")
	const nodeID = 22
	uninstallFinalSeed(t, r, nodeID)
	s.handleUninstallResult(nodeID, uninstallFinalFailureGolden)
	row, err := r.GetNodePendingUninstall(nodeID)
	if err != nil || row == nil {
		t.Fatalf("read pending uninstall: row=%v err=%v", row, err)
	}
	if row.State != repo.NodePendingUninstallFailed {
		t.Fatalf("agent failure frame must land failed, got %q", row.State)
	}
	if row.Reason != uninstallFinalFailureReason {
		t.Fatalf("failure reason must pass through verbatim: got %q, want %q", row.Reason, uninstallFinalFailureReason)
	}
}
