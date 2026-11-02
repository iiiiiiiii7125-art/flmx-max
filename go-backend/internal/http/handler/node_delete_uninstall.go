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
var (
	nodeAgentIsOnline = func(h *Handler, nodeID int64) bool {
		return h != nil && h.wsServer != nil && h.wsServer.IsNodeOnline(nodeID)
	}
	nodeAgentSendCommand = func(h *Handler, nodeID int64, cmdType string, timeout time.Duration) error {
		if h == nil || h.wsServer == nil {
			return errors.New("节点不在线")
		}
		_, err := h.wsServer.SendCommand(nodeID, cmdType, nil, timeout)
		return err
	}
)

// prepareNodeAgentUninstall 是删节点漏斗的前置步：节点记录还在时取
// 其 secret/名称执行卸载步。节点不存在时静默跳过（删除漏斗自会按
// 原有口径处理），返回空状态。
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
//     回执成功即 done；回「未知命令」说明 agent 太旧、无此能力，记
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
		err := nodeAgentSendCommand(h, node.ID, "UninstallAgent", nodeUninstallCommandTimeout)
		if err == nil {
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
