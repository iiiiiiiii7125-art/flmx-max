package ws

import (
	"fmt"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// issuedAt 生产形状门禁·面板消费半（R2-ISSUEDAT）。
//
// 契约：agent 终局 UninstallResult 帧里的 data.issuedAt 是 JSON 数
// 字（UnixMilli int64，与 node_pending_uninstall 登记表同形）；
// handleUninstallResult 按 int64 对账，数字与登记一致时登记迁移
// acked→succeeded/failed，不一致（旧轮迟到）时忽略。
// 线型 golden 与 agent 半（go-gost/x/socket/
// issuedat_gate_test.go）及 handler 下发半（go-backend/internal/
// http/handler/node_delete_uninstall_issuedat_gate_test.go）同值。
const issuedAtGateValue = int64(1794276644653)

// issuedAtGateFrame 造 golden 终局帧字节：issuedAt 为 JSON 数字。
func issuedAtGateFrame(success bool, reason string, issuedAt int64) string {
	return fmt.Sprintf(`{"type":"UninstallResult","data":{"issuedAt":%d,"success":%v,"reason":%q,"state":%q}}`,
		issuedAt, success, reason, map[bool]string{true: "succeeded", false: "failed"}[success])
}

// issuedAtGateSeed 登记一笔 issued 态待卸载，issued_at 为 golden
// 值，返回节点 id。
func issuedAtGateSeed(t *testing.T, r *repo.Repository, nodeID int64) {
	t.Helper()
	if err := r.UpsertNodePendingUninstall(nodeID, time.UnixMilli(issuedAtGateValue)); err != nil {
		t.Fatalf("seed pending uninstall: %v", err)
	}
}

func issuedAtGateRow(t *testing.T, r *repo.Repository, nodeID int64) string {
	t.Helper()
	row, err := r.GetNodePendingUninstall(nodeID)
	if err != nil {
		t.Fatalf("read pending uninstall: %v", err)
	}
	if row == nil {
		return "<nil>"
	}
	return row.State
}

// TestIssuedAtGateFinalFailureMigratesAckedToFailed：数字 issuedAt
// 的失败终局帧把登记迁为 failed，并带上 agent 点名的原因。
func TestIssuedAtGateFinalFailureMigratesAckedToFailed(t *testing.T) {
	r := uninstallRepo(t)
	s := NewServer(r, "jwt-secret")
	const nodeID = 11
	issuedAtGateSeed(t, r, nodeID)
	s.handleUninstallResult(nodeID, issuedAtGateFrame(false, "step uninstall-kernels failed", issuedAtGateValue))
	if got := issuedAtGateRow(t, r, nodeID); got != repo.NodePendingUninstallFailed {
		t.Fatalf("registry state = %q, want failed", got)
	}
	row, _ := r.GetNodePendingUninstall(nodeID)
	if row.Reason != "step uninstall-kernels failed" {
		t.Fatalf("registry reason = %q, want the agent-named step", row.Reason)
	}
}

// TestIssuedAtGateFinalSuccessMigratesAckedToSucceeded：数字
// issuedAt 的成功终局帧把登记迁为 succeeded。
func TestIssuedAtGateFinalSuccessMigratesAckedToSucceeded(t *testing.T) {
	r := uninstallRepo(t)
	s := NewServer(r, "jwt-secret")
	const nodeID = 12
	issuedAtGateSeed(t, r, nodeID)
	s.handleUninstallResult(nodeID, issuedAtGateFrame(true, "", issuedAtGateValue))
	if got := issuedAtGateRow(t, r, nodeID); got != repo.NodePendingUninstallSucceeded {
		t.Fatalf("registry state = %q, want succeeded", got)
	}
}

// TestIssuedAtGateStaleFinalIgnored：issuedAt 与登记不符的旧轮终
// 局帧一律忽略，登记不动。
func TestIssuedAtGateStaleFinalIgnored(t *testing.T) {
	r := uninstallRepo(t)
	s := NewServer(r, "jwt-secret")
	const nodeID = 13
	issuedAtGateSeed(t, r, nodeID)
	s.handleUninstallResult(nodeID, issuedAtGateFrame(true, "", issuedAtGateValue+1))
	if got := issuedAtGateRow(t, r, nodeID); got != repo.NodePendingUninstallIssued {
		t.Fatalf("stale final must leave registry at issued, got %q", got)
	}
}
