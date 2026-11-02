package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
)

// uninstallReplyTimeout 是待卸载 agent 重连后等待其卸载回执的上限。
const uninstallReplyTimeout = 30 * time.Second

// serveUninstalledNode 处理「服务器已删、agent 当时离线」的重连：
// 该 secret 有待卸载登记时，不把它注册为节点，而是直接下发
// UninstallAgent 命令并等回执。返回 false 表示与本函数无关（无登记
// 或仓储不可用），调用方按原鉴权失败路径处理；返回 true 表示连接
// 已被本函数接管（无论卸载结果如何）。
//
// 回执处置口径：
//   - 成功：删登记，闭环结束；
//   - 回「未知命令」：agent 版本过旧、无自卸载能力，留着登记只会每次
//     重连重复打扰，删登记并记日志提示人工卸载；
//   - 其余失败/超时/断线：保留登记，下次重连再试。
func (s *Server) serveUninstalledNode(w http.ResponseWriter, r *http.Request, secret string) bool {
	pending, err := s.repo.GetNodeUninstallBySecret(secret)
	if err != nil || pending == nil {
		return false
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return true
	}
	defer conn.Close()

	crypto, _ := security.NewAESCrypto(secret)
	requestID := fmt.Sprintf("uninstall_%d", time.Now().UnixNano())
	payload, err := json.Marshal(map[string]interface{}{"type": "UninstallAgent", "data": nil, "requestId": requestID})
	if err != nil {
		return true
	}
	if crypto != nil {
		encrypted, err := crypto.Encrypt(payload)
		if err != nil {
			return true
		}
		if payload, err = json.Marshal(map[string]interface{}{"encrypted": true, "data": encrypted, "timestamp": time.Now().UnixMilli()}); err != nil {
			return true
		}
	}
	if err := writeWSMessage(&connWrap{conn: conn}, websocket.TextMessage, payload); err != nil {
		return true
	}

	_ = conn.SetReadDeadline(time.Now().Add(uninstallReplyTimeout))
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return true // 未确认：保留登记，下次重连重试。
		}
		text := decryptIfNeeded(raw, crypto, secret)
		if !strings.Contains(text, requestID) {
			continue
		}
		var resp commandResponse
		if json.Unmarshal([]byte(text), &resp) != nil || resp.RequestID != requestID {
			continue
		}
		switch {
		case resp.Success:
			log.Printf("deleted node %q: agent uninstalled itself after reconnecting", pending.NodeName)
		case strings.Contains(resp.Message, "未知命令"):
			log.Printf("deleted node %q: agent is too old to uninstall itself; remove it manually on the server", pending.NodeName)
		default:
			log.Printf("deleted node %q: agent uninstall failed: %s", pending.NodeName, resp.Message)
			return true
		}
		_ = s.repo.DeleteNodeUninstall(secret)
		return true
	}
}
