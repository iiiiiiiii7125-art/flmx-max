package handler

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go-backend/internal/store/model"
)

// 删服务器连带卸载 agent 的三态回执（回给前端展示，口径与批量聚合
// 计数一一对应）：
const (
	nodeUninstallDone        = "done"        // agent 已确认，正在自卸载
	nodeUninstallPending     = "pending"     // agent 离线/未确认，已登记，重连时自动卸载
	nodeUninstallUnsupported = "unsupported" // agent 版本过旧，不支持自卸载，需人工处理
)

// nodeUninstallCommandTimeout 是在线下发卸载命令的回执等待上限。
const nodeUninstallCommandTimeout = 15 * time.Second

// 在线判定与命令下发的接缝：生产走 Handler 的 WS 服务，测试可替换。
// 命令下发返回 agent 回执 data 里的 warnings 文本集（可为 nil）：
// agent 在回执里点名披露的事项（如调优恢复将失败＋宿主文件清单，
// R2F2-D01）须进删除回执，不得被纯成功吞掉。
var (
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool {
		return h != nil && h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)
	}
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, timeout time.Duration) ([]string, error) {
		if h == nil || h.wsServer == nil {
			return nil, errors.New("节点不在线")
		}
		result, err := h.wsServer.SendCommand(nodeID, cmdType, nil, timeout)
		if err != nil {
			return nil, err
		}
		return agentReplyWarnings(result.Data), nil
	}
)

// agentReplyWarnings 提取 agent 命令回执 data 里的 warnings 文本
// 集。形态不对的条目忽略——回执披露是尽力而为的告知面，不能反
// 过来让删除失败。
func agentReplyWarnings(data map[string]interface{}) []string {
	raw, ok := data["warnings"].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// prepareNodeAgentUninstall 是删节点漏斗的连带卸载步：调用方必须
// 已通过 checkNodeDeleteGuards 守卫校验——守卫失败时整次删除中止、
// 本步不得执行（在线下发触发 agent 自卸载，不可逆）。节点记录还
// 在时取其 secret/名称执行卸载步。节点不存在时静默跳过（删除漏
// 斗自会按原有口径处理），返回空状态。
func (h *Handler) prepareNodeAgentUninstall(id int64) (string, []string) {
	if h == nil || h.repo == nil {
		return "", nil
	}
	node, err := h.repo.GetNodeByID(id)
	if err != nil || node == nil {
		return "", nil
	}
	return h.uninstallNodeAgent(node)
}

// uninstallNodeAgent 让一台即将被删除的节点 agent 自卸载：
//   - 在线：下发 UninstallAgent 命令。agent 先回执再异步卸载自身，
//     回执成功即 done（回执 data 点名的 warnings 随删除回执披露，
//     不算纯成功）；回「未知命令」说明 agent 太旧、无此能力，记
//     unsupported 并点名人工卸载（不登记待卸载——重连拦截问它也只会
//     再回未知命令，但登记表由 WS 侧在回执后清除，不会无限重复）；
//   - 离线或下发未确认：按 secret 登记待卸载，agent 下次连接面板时
//     由 WS 入口拦截下发（见 ws.serveUninstalledNode），记 pending。
//
// 本步只决定 agent 的去向，不影响随后的面板侧删除：任何失败都只进
// warnings，删除照常进行。
func (h *Handler) uninstallNodeAgent(node *model.Node) (string, []string) {
	var warnings []string
	warn := func(format string, args ...interface{}) {
		warnings = append(warnings, node.Name+"："+fmt.Sprintf(format, args...))
	}
	if nodeAgentIsOnline(h, node.ID) {
		replyWarnings, err := nodeAgentSendCommand(h, node.ID, "UninstallAgent", nodeUninstallCommandTimeout)
		if err == nil {
			// agent 回执点名的事项（如调优恢复将失败）随删除回执
			// 披露：状态仍是 done（agent 已确认、正在自卸载），
			// 但回执不是纯成功（R2F2-D01）。
			for _, text := range replyWarnings {
				warn("%s", text)
			}
			return nodeUninstallDone, warnings
		}
		if strings.Contains(err.Error(), "未知命令") {
			warn("agent 版本过旧，不支持自动卸载，请在服务器上手动卸载")
			return nodeUninstallUnsupported, warnings
		}
		warn("卸载命令未确认（%v），将在 agent 重新连接时自动卸载", err)
	}
	if err := h.repo.AddNodeUninstall(node.Secret, node.Name, time.Now()); err != nil {
		warn("记录待卸载失败：%v，请在服务器上手动卸载", err)
		return "", warnings
	}
	return nodeUninstallPending, warnings
}
