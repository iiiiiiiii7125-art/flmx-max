package handler

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// issuedAt 生产形状门禁·面板下发半（R2-ISSUEDAT）。
//
// 契约：面板下发 UninstallAgent 命令时 data.issuedAt 是 JSON 数字
// （UnixMilli int64）：发起段取登记写入时刻的 UnixMilli（与登记表
// issued_at 同值），重连补发段取登记原 issued_at、不刷新。
// 线型 golden 与 agent 半（go-gost/x/socket/
// issuedat_gate_test.go）及 ws 消费半（go-backend/internal/ws/
// uninstall_issuedat_gate_test.go）同值。
const issuedAtGateValue = int64(1794276644653)

var issuedAtGateNumber = regexp.MustCompile(`^\d+$`)

// issuedAtGateCapture 替换命令下发接缝，捕获下发的 cmdType 与 data。
func issuedAtGateCapture(t *testing.T) (captured *[]map[string]interface{}, capturedTypes *[]string) {
	t.Helper()
	var datas []map[string]interface{}
	var types []string
	origOnline, origSend := nodeAgentIsOnline, nodeAgentSendCommand
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool { return true }
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, data map[string]interface{}, timeout time.Duration) ([]string, error) {
		cp := make(map[string]interface{}, len(data))
		for k, v := range data {
			cp[k] = v
		}
		types = append(types, cmdType)
		datas = append(datas, cp)
		return nil, nil
	}
	t.Cleanup(func() {
		nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend
	})
	return &datas, &types
}

// issuedAtGateRawNumber 把捕获的 data 序列化后断言 issuedAt 的原
// 始 JSON 形态是纯数字，并返回其数值。
func issuedAtGateRawNumber(t *testing.T, data map[string]interface{}) int64 {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal captured command data: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal captured command data %s: %v", raw, err)
	}
	rawIssuedAt := string(fields["issuedAt"])
	if !issuedAtGateNumber.MatchString(rawIssuedAt) {
		t.Fatalf("command data.issuedAt must be a JSON number, got %q (full %s)", rawIssuedAt, raw)
	}
	var v int64
	if err := json.Unmarshal(fields["issuedAt"], &v); err != nil {
		t.Fatalf("command data.issuedAt %q is not an int64: %v", rawIssuedAt, err)
	}
	return v
}

// TestIssuedAtGateBeginEmitsNumericIssuedAt：发起段下发的
// issuedAt 是 JSON 数字，且等于登记表刚写入的 issued_at。
func TestIssuedAtGateBeginEmitsNumericIssuedAt(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "gate-node", "secret-gate")
	datas, types := issuedAtGateCapture(t)

	state, _ := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("prepare state = %q, want acked", state)
	}
	if len(*datas) != 1 || len(*types) != 1 || (*types)[0] != "UninstallAgent" {
		t.Fatalf("expected one UninstallAgent command, got types %v datas %v", *types, *datas)
	}
	sent := issuedAtGateRawNumber(t, (*datas)[0])
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil {
		t.Fatal("pending uninstall row missing after prepare")
	}
	if sent != row.IssuedAt {
		t.Fatalf("command issuedAt = %d, want registry issued_at %d", sent, row.IssuedAt)
	}
}

// TestIssuedAtGateReissueReusesRegistryIssuedAt：重连补发段下发的
// issuedAt 是登记原值（JSON 数字）、不刷新。登记以 golden 值种子。
func TestIssuedAtGateReissueReusesRegistryIssuedAt(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "gate-node-reissue", "secret-gate-reissue")
	if err := r.UpsertNodePendingUninstall(node.ID, time.UnixMilli(issuedAtGateValue)); err != nil {
		t.Fatalf("seed pending uninstall: %v", err)
	}
	datas, types := issuedAtGateCapture(t)

	h.reissuePendingNodeUninstall(node.ID)
	if len(*datas) != 1 || len(*types) != 1 || (*types)[0] != "UninstallAgent" {
		t.Fatalf("expected one UninstallAgent reissue, got types %v datas %v", *types, *datas)
	}
	if sent := issuedAtGateRawNumber(t, (*datas)[0]); sent != issuedAtGateValue {
		t.Fatalf("reissue issuedAt = %d, want registry issued_at %d", sent, issuedAtGateValue)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State != repo.NodePendingUninstallAcked {
		t.Fatalf("reissue must move registry to acked, got %+v", row)
	}
}
