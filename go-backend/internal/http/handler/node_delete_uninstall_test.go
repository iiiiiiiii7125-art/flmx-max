package handler

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func openUninstallHandlerRepo(t *testing.T) *repo.Repository {
	t.Helper()
	r, err := repo.Open(filepath.Join(t.TempDir(), "node-delete-uninstall.db"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func seedUninstallNode(t *testing.T, r *repo.Repository, name, secret string) *model.Node {
	t.Helper()
	n := &model.Node{Name: name, Secret: secret, ServerIP: "127.0.0.1", Port: "30000", Status: 1}
	if err := r.DB().Create(n).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return n
}

// 替换在线判定/命令下发接缝；sendErr 为命令下发结果。
func stubNodeAgentChannel(t *testing.T, online bool, sendErr error) {
	t.Helper()
	stubNodeAgentChannelWarnings(t, online, nil, sendErr)
}

// stubNodeAgentChannelWarnings 同 stubNodeAgentChannel，另让命令
// 回执携带 agent 点名的 warnings（R2F2-D01 的回执披露面）。
func stubNodeAgentChannelWarnings(t *testing.T, online bool, replyWarnings []string, sendErr error) {
	t.Helper()
	origOnline, origSend := nodeAgentIsOnline, nodeAgentSendCommand
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool { return online }
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, timeout time.Duration) ([]string, error) {
		if cmdType != "UninstallAgent" {
			t.Errorf("unexpected command type %q", cmdType)
		}
		return replyWarnings, sendErr
	}
	t.Cleanup(func() {
		nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend
	})
}

func pendingRow(t *testing.T, r *repo.Repository, secret string) *model.NodeUninstall {
	t.Helper()
	row, err := r.GetNodeUninstallBySecret(secret)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	return row
}

// 三态回执之一：在线且 agent 确认 → done，且不留待卸载登记。
func TestUninstallNodeAgentDone(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "online-node", "secret-done")
	stubNodeAgentChannel(t, true, nil)

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallDone {
		t.Fatalf("state = %q, want done", state)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if row := pendingRow(t, r, "secret-done"); row != nil {
		t.Fatalf("done must not register a pending row: %+v", row)
	}
}

// R2F2-D01：在线且 agent 确认，但回执 data 点名了调优恢复将失败
// （队列备份丢失＋宿主文件清单）→ 状态仍为 done，warnings 必须带
// 节点名原样透传，回执不得是纯成功；不留待卸载登记。
func TestUninstallNodeAgentDoneWithAgentWarnings(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "tuned-node", "secret-tuned")
	agentWarning := "调优恢复将失败：整形前的队列备份已丢失，队列整形与调优参数无法随卸载恢复（重启后随状态消散）；卸载时会删除宿主调优文件：/etc/sysctl.d/99-flmx-tuning.conf"
	stubNodeAgentChannelWarnings(t, true, []string{agentWarning}, nil)

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallDone {
		t.Fatalf("state = %q, want done", state)
	}
	if len(warnings) != 1 || warnings[0] != "tuned-node："+agentWarning {
		t.Fatalf("warnings = %v, want the agent warning named with the node name", warnings)
	}
	if row := pendingRow(t, r, "secret-tuned"); row != nil {
		t.Fatalf("done with warnings must not register a pending row: %+v", row)
	}
}

// 回执 warnings 提取：只收非空字符串条目，其余形态一律忽略（披
// 露面不能反过来让删除失败）。
func TestAgentReplyWarnings(t *testing.T) {
	if got := agentReplyWarnings(nil); got != nil {
		t.Fatalf("nil data: got %v", got)
	}
	if got := agentReplyWarnings(map[string]interface{}{"warnings": "nope"}); got != nil {
		t.Fatalf("non-list warnings: got %v", got)
	}
	got := agentReplyWarnings(map[string]interface{}{
		"warnings": []interface{}{"甲", "", "  ", 42, "乙"},
	})
	if len(got) != 2 || got[0] != "甲" || got[1] != "乙" {
		t.Fatalf("got %v, want [甲 乙]", got)
	}
}

// 三态之二：在线但 agent 回未知命令（版本过旧）→ unsupported，点名
// 人工卸载，且不登记（登记了也会在重连回执后被清，不留无意义行）。
func TestUninstallNodeAgentUnsupported(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "old-node", "secret-old")
	stubNodeAgentChannel(t, true, errors.New("未知命令类型: UninstallAgent"))

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallUnsupported {
		t.Fatalf("state = %q, want unsupported", state)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "手动卸载") {
		t.Fatalf("warnings = %v, want one manual-uninstall warning", warnings)
	}
	if row := pendingRow(t, r, "secret-old"); row != nil {
		t.Fatalf("unsupported must not register a pending row: %+v", row)
	}
}

// 三态之三：离线 → pending 并按 secret 登记；节点名随行供重连日志
// 点名。在线但下发未确认同口径（pending + 告警说明将自动重试）。
func TestUninstallNodeAgentPending(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")

	offline := seedUninstallNode(t, r, "offline-node", "secret-offline")
	stubNodeAgentChannel(t, false, nil)
	state, warnings := h.prepareNodeAgentUninstall(offline.ID)
	if state != nodeUninstallPending {
		t.Fatalf("offline state = %q, want pending", state)
	}
	if len(warnings) != 0 {
		t.Fatalf("offline warnings = %v, want none", warnings)
	}
	row := pendingRow(t, r, "secret-offline")
	if row == nil || row.NodeName != "offline-node" {
		t.Fatalf("pending row = %+v, want registered offline-node", row)
	}

	unconfirmed := seedUninstallNode(t, r, "unconfirmed-node", "secret-unconfirmed")
	stubNodeAgentChannel(t, true, errors.New("等待节点响应超时"))
	state, warnings = h.prepareNodeAgentUninstall(unconfirmed.ID)
	if state != nodeUninstallPending {
		t.Fatalf("unconfirmed state = %q, want pending", state)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "重新连接") {
		t.Fatalf("warnings = %v, want one reconnect warning", warnings)
	}
	if row := pendingRow(t, r, "secret-unconfirmed"); row == nil {
		t.Fatal("unconfirmed must register a pending row")
	}
}

// 节点不存在时静默跳过（删除漏斗自会处理），不产生任何登记。
func TestPrepareNodeAgentUninstallMissingNode(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	stubNodeAgentChannel(t, false, nil)

	state, warnings := h.prepareNodeAgentUninstall(99999)
	if state != "" || warnings != nil {
		t.Fatalf("missing node: state=%q warnings=%v, want empty", state, warnings)
	}
}

type nodeDeleteResp struct {
	Code int                    `json:"code"`
	Data map[string]interface{} `json:"data"`
}

func postNodeDelete(t *testing.T, h *Handler, body string) nodeDeleteResp {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/node/delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeDelete(rec, req)
	var resp nodeDeleteResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 {
		t.Fatalf("delete failed: %s", rec.Body.String())
	}
	return resp
}

// HTTP 口径：不带 uninstall 时行为与此前完全一致（无卸载字段、无
// 登记）；带 uninstall:true 且节点离线时，回执 pending 且登记落盘、
// 节点记录照常删除。
func TestNodeDeleteHTTPUninstallFlag(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	stubNodeAgentChannel(t, false, nil)

	plain := seedUninstallNode(t, r, "plain-node", "secret-plain")
	resp := postNodeDelete(t, h, `{"id":`+nodeIDStr(plain.ID)+`}`)
	if _, ok := resp.Data["uninstall"]; ok {
		t.Fatalf("no-flag delete must not carry uninstall state: %v", resp.Data)
	}
	if row := pendingRow(t, r, "secret-plain"); row != nil {
		t.Fatalf("no-flag delete must not register pending: %+v", row)
	}

	flagged := seedUninstallNode(t, r, "flagged-node", "secret-flagged")
	resp = postNodeDelete(t, h, `{"id":`+nodeIDStr(flagged.ID)+`,"uninstall":true}`)
	if resp.Data["uninstall"] != nodeUninstallPending {
		t.Fatalf("flagged delete uninstall = %v, want pending", resp.Data["uninstall"])
	}
	if row := pendingRow(t, r, "secret-flagged"); row == nil {
		t.Fatal("flagged offline delete must register a pending row")
	}
	if node, err := r.GetNodeByID(flagged.ID); err != nil || node != nil {
		t.Fatalf("node record must be deleted: node=%v err=%v", node, err)
	}
}

// 批量口径：uninstall:true 时三态计数聚合进回执（此处两台均离线
// → uninstallPendingCount=2），节点记录删除。
func TestNodeBatchDeleteHTTPUninstallAggregation(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	stubNodeAgentChannel(t, false, nil)

	n1 := seedUninstallNode(t, r, "batch-1", "secret-batch-1")
	n2 := seedUninstallNode(t, r, "batch-2", "secret-batch-2")
	body := `{"ids":[` + nodeIDStr(n1.ID) + `,` + nodeIDStr(n2.ID) + `],"uninstall":true}`
	req := httptest.NewRequest("POST", "/api/v1/node/batch-delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.nodeBatchDelete(rec, req)

	var resp struct {
		Code int `json:"code"`
		Data struct {
			SuccessCount          int `json:"successCount"`
			UninstallPendingCount int `json:"uninstallPendingCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 || resp.Data.SuccessCount != 2 {
		t.Fatalf("batch delete failed: %s", rec.Body.String())
	}
	if resp.Data.UninstallPendingCount != 2 {
		t.Fatalf("uninstallPendingCount = %d, want 2 (%s)", resp.Data.UninstallPendingCount, rec.Body.String())
	}
	if row := pendingRow(t, r, "secret-batch-1"); row == nil {
		t.Fatal("batch-1 must be registered")
	}
	if row := pendingRow(t, r, "secret-batch-2"); row == nil {
		t.Fatal("batch-2 must be registered")
	}
}

func nodeIDStr(v int64) string {
	return strconv.FormatInt(v, 10)
}
