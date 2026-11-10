package ws

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

// uninstallReplyTimeout 是待卸载 agent 重连后等待其卸载回执的上限。
const uninstallReplyTimeout = 30 * time.Second

// uninstallReplyWarnings 提取 agent 卸载回执 data 里的 warnings
// 文本集；data 缺失或形态不对时返回 nil（披露是尽力而为，不因
// 此影响登记清除等主流程）。
func uninstallReplyWarnings(data json.RawMessage) []string {
	if len(data) == 0 {
		return nil
	}
	var parsed struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	var out []string
	for _, s := range parsed.Warnings {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// uninstallIssuedAt 解终局帧的 issuedAt：正形为数字 UnixMilli
// （面板登记表 IssuedAt int64 同形，统一编码已定）；兼容旧
// agent 发来的数字字符串与 RFC3339 字符串；空串/缺席/null 按
// 0 处理——C2 wirecheck 实证：缺席键可解、显式空串比缺席更
// 糟；存量旧 agent 恒发空串，按 0 接受（跳过新旧比对）严格优
// 于整帧判 malformed 丢弃。无法识别的字符串仍报错，由调用方
// 按 malformed 丢弃。
type uninstallIssuedAt int64

func (v *uninstallIssuedAt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*v = 0
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*v = 0
			return nil
		}
		if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
			*v = uninstallIssuedAt(ms)
			return nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			*v = uninstallIssuedAt(t.UnixMilli())
			return nil
		}
		return fmt.Errorf("issuedAt %q is neither millis nor RFC3339", s)
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*v = uninstallIssuedAt(n)
	return nil
}

// handleUninstallResult 落 agent 的卸载终局上报（C4-G2 面板
// 半）：报文是 agent CommandResponse 正形——{type:
// "UninstallResult", success, message, data:{issuedAt, state,
// failedStep, steps, warnings}}，success 与失败原因在帧顶层
// （agent buildUninstallFinalResponse 构造，字节级 golden 由
// 两侧 uninstall_final_contract_test.go 对钉）。幂等键 = 本连
// 接的 node_id＋登记的 issued_at：issuedAt 与现行登记不符（重
// 试已刷新、旧轮迟到）一律忽略；无登记时同样忽略（重复上报/
// 已处置）。成功 → succeeded（面板清扫随后删记录并清登记）；
// 失败 → failed＋agent 点名原因（顶层 message 逐字透传）。
// warnings 逐条进日志（终局路径没有同步回执可挂，日志是其披
// 露面，与重连拦截路径同口径）。
// R2-ISSUEDAT：issuedAt 接受数字（正形）、数字字符串、RFC3339
// 字符串、空串/缺席（按 0、不参与新旧比对）；补发去重在 agent
// 侧执行，面板侧重连补发以登记原 issued_at 为键、不刷新
// （node_delete_uninstall.go 补发段）。
// 兼容兜底：顶层 success 缺席时才回读 data.success/data.reason
// 的内嵌形态——该形态从未有 agent 版本发出过（构造器自 C4-G2
// 引入即为顶层形态，git 可证），保留它只为面板自身旧测试语料
// 形态的在途帧不断链；顶层在场时内嵌值一律不看。
func (s *Server) handleUninstallResult(nodeID int64, msg string) {
	if s == nil || s.repo == nil {
		return
	}
	var envelope struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Data    struct {
			IssuedAt uninstallIssuedAt `json:"issuedAt"`
			Success  *bool             `json:"success"`
			Reason   string            `json:"reason"`
			Warnings []string          `json:"warnings"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(msg), &envelope); err != nil {
		log.Printf("websocket: malformed UninstallResult from node %d: %v", nodeID, err)
		return
	}
	success := false
	if envelope.Success != nil {
		success = *envelope.Success
	} else if envelope.Data.Success != nil {
		success = *envelope.Data.Success
	}
	rec, err := s.repo.GetNodePendingUninstall(nodeID)
	if err != nil || rec == nil {
		log.Printf("websocket: UninstallResult from node %d without pending registry, ignored", nodeID)
		return
	}
	if envelope.Data.IssuedAt != 0 && int64(envelope.Data.IssuedAt) != rec.IssuedAt {
		log.Printf("websocket: stale UninstallResult from node %d (issuedAt %d, current %d), ignored", nodeID, envelope.Data.IssuedAt, rec.IssuedAt)
		return
	}
	for _, text := range envelope.Data.Warnings {
		if strings.TrimSpace(text) != "" {
			log.Printf("node %d agent uninstall warning: %s", nodeID, text)
		}
	}
	now := time.Now()
	if success {
		won, terr := s.repo.TransitionNodePendingUninstall(nodeID,
			[]string{repo.NodePendingUninstallIssued, repo.NodePendingUninstallAcked, repo.NodePendingUninstallUnconfirmed},
			repo.NodePendingUninstallSucceeded, "", now)
		if terr != nil || !won {
			log.Printf("websocket: UninstallResult success transition failed node_id=%d won=%v err=%v", nodeID, won, terr)
			return
		}
		log.Printf("node %d agent reported uninstall success (issuedAt %d)", nodeID, rec.IssuedAt)
		return
	}
	reason := strings.TrimSpace(envelope.Message)
	if reason == "" {
		reason = strings.TrimSpace(envelope.Data.Reason)
	}
	if reason == "" {
		reason = "agent 上报卸载失败（未附原因）"
	}
	if r := []rune(reason); len(r) > 500 {
		reason = string(r[:500])
	}
	won, terr := s.repo.TransitionNodePendingUninstall(nodeID,
		[]string{repo.NodePendingUninstallIssued, repo.NodePendingUninstallAcked, repo.NodePendingUninstallUnconfirmed},
		repo.NodePendingUninstallFailed, reason, now)
	if terr != nil || !won {
		log.Printf("websocket: UninstallResult failure transition failed node_id=%d won=%v err=%v", nodeID, won, terr)
		return
	}
	log.Printf("node %d agent reported uninstall failure: %s", nodeID, reason)
}

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
			// agent 回执点名的事项（如调优恢复将失败＋宿主文件清
			// 单，R2F2-D01）逐条进日志：重连路径没有用户回执可
			// 挂，日志是这条路径唯一的披露面。
			for _, text := range uninstallReplyWarnings(resp.Data) {
				log.Printf("deleted node %q: agent uninstall warning: %s", pending.NodeName, text)
			}
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
