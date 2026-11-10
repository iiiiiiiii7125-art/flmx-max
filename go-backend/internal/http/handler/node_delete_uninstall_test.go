package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
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
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, data map[string]interface{}, timeout time.Duration) ([]string, error) {
		if cmdType != "UninstallAgent" {
			t.Errorf("unexpected command type %q", cmdType)
		}
		if _, ok := data["issuedAt"]; !ok {
			t.Errorf("uninstall command must carry the issuedAt idempotency key, data = %v", data)
		}
		return replyWarnings, sendErr
	}
	t.Cleanup(func() {
		nodeAgentIsOnline, nodeAgentSendCommand = origOnline, origSend
	})
}

// pendingRow 查旧 secret 键待卸载登记（节点记录删除后由已删
// secret 对账的那张表）：C4-G2 发起流程不写此表，断言它为空
// 是「发起不误用旧登记面」的钉。
func pendingRow(t *testing.T, r *repo.Repository, secret string) *model.NodeUninstall {
	t.Helper()
	row, err := r.GetNodeUninstallBySecret(secret)
	if err != nil {
		t.Fatalf("get pending: %v", err)
	}
	return row
}

// pendingUninstallRow 查 C4-G2 的节点键待卸载登记
// （node_pending_uninstall）：发起流程的登记面。
func pendingUninstallRow(t *testing.T, r *repo.Repository, nodeID int64) *model.NodePendingUninstall {
	t.Helper()
	row, err := r.GetNodePendingUninstall(nodeID)
	if err != nil {
		t.Fatalf("get pending uninstall: %v", err)
	}
	return row
}

// C4-G2 发起段：在线且 agent 受理 → acked（已受理、执行中），
// 不是 done——终局未到，节点记录与登记都保留待终局上报；发
// 起不写旧 secret 登记表。
func TestUninstallNodeAgentAcked(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "online-node", "secret-done")
	stubNodeAgentChannel(t, true, nil)

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("state = %q, want acked", state)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State != nodeUninstallAcked {
		t.Fatalf("pending uninstall row = %+v, want acked registration", row)
	}
	if row := pendingRow(t, r, "secret-done"); row != nil {
		t.Fatalf("initiation must not write the legacy secret registry: %+v", row)
	}
}

// R2F2-D01：在线且 agent 受理，但回执 data 点名了调优恢复将失
// 败（队列备份丢失＋宿主文件清单）→ 状态为 acked，warnings 必
// 须带节点名原样透传，回执不得是纯成功。
func TestUninstallNodeAgentAckedWithAgentWarnings(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "tuned-node", "secret-tuned")
	agentWarning := "调优恢复将失败：整形前的队列备份已丢失，队列整形与调优参数无法随卸载恢复（重启后随状态消散）；卸载时会删除宿主调优文件：/etc/sysctl.d/99-flmx-tuning.conf"
	stubNodeAgentChannelWarnings(t, true, []string{agentWarning}, nil)

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallAcked {
		t.Fatalf("state = %q, want acked", state)
	}
	if len(warnings) != 1 || warnings[0] != "tuned-node："+agentWarning {
		t.Fatalf("warnings = %v, want the agent warning named with the node name", warnings)
	}
	if row := pendingUninstallRow(t, r, node.ID); row == nil || row.State != nodeUninstallAcked {
		t.Fatalf("pending uninstall row = %+v, want acked registration", row)
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

// 在线但 agent 回未知命令（版本过旧）→ 明确拒绝即终局：状态
// failed＋点名人工卸载，登记保留 failed 供管理员看见，不许当
// 成功、也不许无登记地放行删记录。
func TestUninstallNodeAgentUnsupported(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	node := seedUninstallNode(t, r, "old-node", "secret-old")
	stubNodeAgentChannel(t, true, errors.New("未知命令类型: UninstallAgent"))

	state, warnings := h.prepareNodeAgentUninstall(node.ID)
	if state != nodeUninstallFailed {
		t.Fatalf("state = %q, want failed", state)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "手动卸载") {
		t.Fatalf("warnings = %v, want one manual-uninstall warning", warnings)
	}
	row := pendingUninstallRow(t, r, node.ID)
	if row == nil || row.State != nodeUninstallFailed || !strings.Contains(row.Reason, "手动卸载") {
		t.Fatalf("pending uninstall row = %+v, want failed registration naming manual uninstall", row)
	}
	if row := pendingRow(t, r, "secret-old"); row != nil {
		t.Fatalf("initiation must not write the legacy secret registry: %+v", row)
	}
}

// 离线 → issued 并按节点登记，回执如实带「已登记待卸载」提示
// （F2-21 同族：不许无提示地装作已受理）；在线但下发未确认同
// 口径（issued + 告警说明将自动补发）。
func TestUninstallNodeAgentIssued(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")

	offline := seedUninstallNode(t, r, "offline-node", "secret-offline")
	stubNodeAgentChannel(t, false, nil)
	state, warnings := h.prepareNodeAgentUninstall(offline.ID)
	if state != nodeUninstallIssued {
		t.Fatalf("offline state = %q, want issued", state)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "已登记待卸载") {
		t.Fatalf("offline warnings = %v, want one registered-pending notice", warnings)
	}
	row := pendingUninstallRow(t, r, offline.ID)
	if row == nil || row.State != nodeUninstallIssued {
		t.Fatalf("pending uninstall row = %+v, want issued registration", row)
	}

	unconfirmed := seedUninstallNode(t, r, "unconfirmed-node", "secret-unconfirmed")
	stubNodeAgentChannel(t, true, errors.New("等待节点响应超时"))
	state, warnings = h.prepareNodeAgentUninstall(unconfirmed.ID)
	if state != nodeUninstallIssued {
		t.Fatalf("unconfirmed state = %q, want issued", state)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "重新连接") {
		t.Fatalf("warnings = %v, want one reconnect warning", warnings)
	}
	if row := pendingUninstallRow(t, r, unconfirmed.ID); row == nil || row.State != nodeUninstallIssued {
		t.Fatalf("pending uninstall row = %+v, want issued registration", row)
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
	if row := pendingUninstallRow(t, r, 99999); row != nil {
		t.Fatalf("missing node must not register: %+v", row)
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

// HTTP 口径：不带 uninstall 时删除照常完成，但回执必须明示
// uninstall=not_requested（F2-21：不许无提示地留下 agent 残
// 留）；带 uninstall:true 且节点离线时，回执 issued +
// nodeDeleted=false、节点记录保留、登记落盘——记录只能在
// agent 终局上报成功后由清扫删除。
func TestNodeDeleteHTTPUninstallFlag(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	stubNodeAgentChannel(t, false, nil)

	plain := seedUninstallNode(t, r, "plain-node", "secret-plain")
	resp := postNodeDelete(t, h, `{"id":`+nodeIDStr(plain.ID)+`}`)
	if resp.Data["uninstall"] != "not_requested" {
		t.Fatalf("no-flag delete must name uninstall=not_requested (F2-21): %v", resp.Data)
	}
	if row := pendingUninstallRow(t, r, plain.ID); row != nil {
		t.Fatalf("no-flag delete must not register pending uninstall: %+v", row)
	}
	if node, err := r.GetNodeByID(plain.ID); err != nil || node != nil {
		t.Fatalf("plain node record must be deleted: node=%v err=%v", node, err)
	}

	flagged := seedUninstallNode(t, r, "flagged-node", "secret-flagged")
	resp = postNodeDelete(t, h, `{"id":`+nodeIDStr(flagged.ID)+`,"uninstall":true}`)
	if resp.Data["uninstall"] != nodeUninstallIssued {
		t.Fatalf("flagged delete uninstall = %v, want issued", resp.Data["uninstall"])
	}
	if resp.Data["nodeDeleted"] != false {
		t.Fatalf("flagged delete must not claim the record deleted: %v", resp.Data)
	}
	if row := pendingUninstallRow(t, r, flagged.ID); row == nil || row.State != nodeUninstallIssued {
		t.Fatalf("pending uninstall row = %+v, want issued registration", row)
	}
	if node, err := r.GetNodeByID(flagged.ID); err != nil || node == nil {
		t.Fatalf("node record must be retained until the uninstall final report: node=%v err=%v", node, err)
	}
}

// C4-G2 协议第 5 条：failed 登记的节点被管理员不带 uninstall 强制删
// 记录时，登记随记录同点清除，且丢弃须留痕（日志点名 state＋reason）。
func TestNodeDeleteForceDiscardsFailedRegistrationWithTrail(t *testing.T) {
	r := openUninstallHandlerRepo(t)
	h := New(r, "test-secret")
	stubNodeAgentChannel(t, false, nil)

	n := seedUninstallNode(t, r, "failed-node", "secret-failed")
	if err := r.UpsertNodePendingUninstall(n.ID, time.Now()); err != nil {
		t.Fatalf("upsert pending uninstall: %v", err)
	}
	if won, err := r.TransitionNodePendingUninstall(n.ID, []string{nodeUninstallIssued}, nodeUninstallFailed, "agent 明确拒绝", time.Now()); err != nil || !won {
		t.Fatalf("transition to failed: won=%v err=%v", won, err)
	}

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOut)

	resp := postNodeDelete(t, h, `{"id":`+nodeIDStr(n.ID)+`}`)
	if resp.Data["uninstall"] != "not_requested" {
		t.Fatalf("force delete receipt must name uninstall=not_requested: %v", resp.Data)
	}
	if node, err := r.GetNodeByID(n.ID); err != nil || node != nil {
		t.Fatalf("node record must be deleted: node=%v err=%v", node, err)
	}
	if row := pendingUninstallRow(t, r, n.ID); row != nil {
		t.Fatalf("failed registration must be cleared with the record: %+v", row)
	}
	if !strings.Contains(buf.String(), "discarding pending uninstall registry") || !strings.Contains(buf.String(), "state=failed") {
		t.Fatalf("force discard must leave a trail naming the state: %q", buf.String())
	}
}

// 批量口径：uninstall:true 且两台均离线 → 两条都只登记
// （successCount=0、pendingCount=2、uninstallIssuedCount=2），
// 节点记录保留、登记落盘；不许把「已登记」计成删除成功。
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
			SuccessCount         int `json:"successCount"`
			PendingCount         int `json:"pendingCount"`
			UninstallIssuedCount int `json:"uninstallIssuedCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if resp.Code != 0 {
		t.Fatalf("batch delete failed: %s", rec.Body.String())
	}
	if resp.Data.SuccessCount != 0 {
		t.Fatalf("successCount = %d, want 0 (registration is not deletion): %s", resp.Data.SuccessCount, rec.Body.String())
	}
	if resp.Data.PendingCount != 2 || resp.Data.UninstallIssuedCount != 2 {
		t.Fatalf("pending/issued = %d/%d, want 2/2 (%s)", resp.Data.PendingCount, resp.Data.UninstallIssuedCount, rec.Body.String())
	}
	for _, n := range []*model.Node{n1, n2} {
		if row := pendingUninstallRow(t, r, n.ID); row == nil || row.State != nodeUninstallIssued {
			t.Fatalf("node %d pending uninstall row = %+v, want issued registration", n.ID, row)
		}
		if node, err := r.GetNodeByID(n.ID); err != nil || node == nil {
			t.Fatalf("node %d record must be retained until the uninstall final report: node=%v err=%v", n.ID, node, err)
		}
	}
}

func nodeIDStr(v int64) string {
	return strconv.FormatInt(v, 10)
}
