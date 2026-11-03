package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/repo"
)

// 删节点漏斗三段口径的回归钉：守卫校验失败时整次删除中止，且守卫
// 必须先于连带卸载步——不触发在线下发、不写待卸载登记、批删卸载
// 计数不虚增。旧顺序（先卸载后守卫）下本文件用例全红：在线节点会
// 收到 UninstallAgent、离线节点会留下与删除意图脱钩的待卸载登记。

// stubCountingNodeAgentChannel 同 stubNodeAgentChannel，但记录下发
// 次数，供断言「守卫失败时一次都没下发」。
func stubCountingNodeAgentChannel(t *testing.T, online bool, sends *int) {
	t.Helper()
	origOnline, origSend := nodeAgentIsOnline, nodeAgentSendCommand
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool { return online }
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, timeout time.Duration) ([]string, error) {
		*sends++
		return nil, nil
	}
	t.Cleanup(func() {
		nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend
	})
}

// breakNodeDeleteGuards 让守卫段第一条查询（ListNodeInboundsByNode）
// 失败：删掉 node_inbound 表。节点行本身完好，卸载步若被误执行，
// 在线会下发、离线会登记，都可观测。
func breakNodeDeleteGuards(t *testing.T, r *repo.Repository) {
	t.Helper()
	if err := r.DB().Exec("DROP TABLE node_inbound").Error; err != nil {
		t.Fatalf("drop node_inbound: %v", err)
	}
}

func postNodeDeleteRaw(t *testing.T, h *Handler, body string) nodeDeleteResp {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/node/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeDelete(rec, req)
	var resp nodeDeleteResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp
}

// 单删＋在线 agent：守卫失败 → 删除报错中止、节点保留、卸载命令
// 零下发、不留待卸载登记。
func TestNodeDeleteGuardFailureSkipsUninstallOnline(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "guarded-online", "secret-guarded-online")
	sends := 0
	stubCountingNodeAgentChannel(t, true, &sends)
	breakNodeDeleteGuards(t, r)

	resp := postNodeDeleteRaw(t, h, `{"id":`+nodeIDStr(node.ID)+`,"uninstall":true}`)
	if resp.Code == 0 {
		t.Fatalf("guard failure must abort the delete, got success: %+v", resp.Data)
	}
	if sends != 0 {
		t.Fatalf("uninstall command sent %d times despite guard failure", sends)
	}
	if row := pendingRow(t, r, "secret-guarded-online"); row != nil {
		t.Fatalf("guard failure must not register pending uninstall: %+v", row)
	}
	if got, err := r.GetNodeByID(node.ID); err != nil || got == nil {
		t.Fatalf("node record must survive an aborted delete: node=%v err=%v", got, err)
	}
}

// 单删＋离线 agent：守卫失败 → 不写待卸载登记（旧顺序下登记会先于
// 守卫落盘、与当次删除意图脱钩），节点保留。
func TestNodeDeleteGuardFailureSkipsUninstallOffline(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "guarded-offline", "secret-guarded-offline")
	sends := 0
	stubCountingNodeAgentChannel(t, false, &sends)
	breakNodeDeleteGuards(t, r)

	resp := postNodeDeleteRaw(t, h, `{"id":`+nodeIDStr(node.ID)+`,"uninstall":true}`)
	if resp.Code == 0 {
		t.Fatalf("guard failure must abort the delete, got success: %+v", resp.Data)
	}
	if row := pendingRow(t, r, "secret-guarded-offline"); row != nil {
		t.Fatalf("guard failure must not register pending uninstall: %+v", row)
	}
	if got, err := r.GetNodeByID(node.ID); err != nil || got == nil {
		t.Fatalf("node record must survive an aborted delete: node=%v err=%v", got, err)
	}
}

// 批删：两节点守卫均失败 → 全部记 fail、success=0，卸载三态计数
// 全 0（旧实现删除前累加会把 pending 计成 2，与 fail 自相矛盾），
// 无待卸载登记，节点保留。
func TestNodeBatchDeleteGuardFailureSkipsUninstallAndCounts(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	n1 := seedUninstallNode(t, r, "guarded-batch-1", "secret-guarded-batch-1")
	n2 := seedUninstallNode(t, r, "guarded-batch-2", "secret-guarded-batch-2")
	sends := 0
	stubCountingNodeAgentChannel(t, false, &sends)
	breakNodeDeleteGuards(t, r)

	body := `{"ids":[` + nodeIDStr(n1.ID) + `,` + nodeIDStr(n2.ID) + `],"uninstall":true}`
	req := httptest.NewRequest("POST", "/api/v1/node/batch-delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeBatchDelete(rec, req)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount              int `json:"successCount"`
			FailCount                 int `json:"failCount"`
			UninstalledCount          int `json:"uninstalledCount"`
			UninstallPendingCount     int `json:"uninstallPendingCount"`
			UninstallUnsupportedCount int `json:"uninstallUnsupportedCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Data.SuccessCount != 0 || resp.Data.FailCount != 2 {
		t.Fatalf("success/fail = %d/%d, want 0/2 (%s)", resp.Data.SuccessCount, resp.Data.FailCount, rec.Body.String())
	}
	if resp.Data.UninstalledCount != 0 || resp.Data.UninstallPendingCount != 0 || resp.Data.UninstallUnsupportedCount != 0 {
		t.Fatalf("uninstall counts must stay 0 when every delete failed: %s", rec.Body.String())
	}
	if sends != 0 {
		t.Fatalf("uninstall command sent %d times despite guard failures", sends)
	}
	for _, secret := range []string{"secret-guarded-batch-1", "secret-guarded-batch-2"} {
		if row := pendingRow(t, r, secret); row != nil {
			t.Fatalf("guard failure must not register pending uninstall for %s: %+v", secret, row)
		}
	}
	for _, id := range []int64{n1.ID, n2.ID} {
		if got, err := r.GetNodeByID(id); err != nil || got == nil {
			t.Fatalf("node %d must survive an aborted batch delete: node=%v err=%v", id, got, err)
		}
	}
}
