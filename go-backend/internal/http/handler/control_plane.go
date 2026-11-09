package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/ws"
)

var errForwardNotFound = errors.New("forward not found")

// S-04: 节点离线错误，携带离线节点ID列表
// 用于删除转发时明确告知调用方哪些节点未清理，避免静默成功
type nodeOfflineError struct {
	nodeIDs []int64
}

func (e *nodeOfflineError) Error() string {
	return fmt.Sprintf("S-04: 节点离线，转发未完全删除，离线节点: %v", e.nodeIDs)
}

// IsNodeOfflineError 判断是否为节点离线错误
func IsNodeOfflineError(err error) bool {
	var target *nodeOfflineError
	return errors.As(err, &target)
}

type forwardRecord = model.ForwardRecord
type tunnelRecord = model.TunnelRecord
type forwardPortRecord = model.ForwardPortRecord
type nodeRecord = model.NodeRecord

type chainNodeRecord = model.ChainNodeRecord

type forwardRuntimeLimiters struct {
	TrafficLimiter string
	ConnLimiter    string
}

type forwardLimiterConfig struct {
	Name   string
	Limits []string
}

type diagnosisTarget struct {
	Address string
	IP      string
	Port    int
}

type diagnosisWorkItem struct {
	fromNodeID   int64
	targetIP     string
	targetPort   int
	description  string
	metadata     map[string]interface{}
	toNode       chainNodeRecord
	hasChainHop  bool
	ipPreference string
	protocol     string
	// decorate（可空）：结果项产出后由 runDiagnosisWorkItems 统一
	// 调用，供构造方并入附加字段/改写文案（RV3-01：nft applied 的
	// 探测项并入规则字段、给 message 加规则状态前缀——非流式与
	// 流式两面经同一执行器产出，装饰必须在此单点生效）。
	decorate func(item map[string]interface{})
}

type diagnosisExecOptions struct {
	commandTimeout time.Duration
	pingTimeoutMS  int
	pingCount      int
	timeoutMessage string
}

type diagnosisProgress struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Success   int `json:"success"`
	Failed    int `json:"failed"`
	// Unverified 是 UDP 未验证项的单列计数（R6-L4-01）：未验证项
	// 既不计成功也不计失败，成败聚合口径在 countDiagnosisResult
	// 单点收口，流式/非流式/汇总三处共用。
	Unverified int `json:"unverified,omitempty"`
}

// countDiagnosisResult 把一个结果项计入进度：Completed 恒 +1；
// 未验证项（unverified=true）只进 Unverified 列、不参与成败；
// 其余按 success 分成功/失败。全部诊断进度计数（runDiagnosis
// WorkItems 的流式进度、summarizeDiagnosisProgress 的 done 汇总、
// 转发流式 statusItems 前缀）必须经此单点，不得再各自按
// success 二分——否则 UDP 项会被误计为失败（R6-L4-01）。
func countDiagnosisResult(progress *diagnosisProgress, item map[string]interface{}) {
	progress.Completed++
	switch {
	case asBool(item["unverified"], false):
		progress.Unverified++
	case asBool(item["success"], false):
		progress.Success++
	default:
		progress.Failed++
	}
}

type diagnosisItemEmitter func(index int, item map[string]interface{}, progress diagnosisProgress)

func (h *Handler) buildDiagnosisStreamStartItems(workItems []diagnosisWorkItem) []map[string]interface{} {
	if len(workItems) == 0 {
		return []map[string]interface{}{}
	}

	nodeCache := map[int64]*nodeRecord{}
	items := make([]map[string]interface{}, 0, len(workItems))
	for _, workItem := range workItems {
		targetIP := strings.TrimSpace(workItem.targetIP)
		targetPort := workItem.targetPort
		if workItem.hasChainHop && workItem.toNode.Port > 0 {
			fromNode, _ := h.cachedNode(nodeCache, workItem.fromNodeID)
			targetNode, err := h.cachedNode(nodeCache, workItem.toNode.NodeID)
			if err == nil {
				resolvedIP, resolvedPort, resolveErr := resolveChainProbeTarget(fromNode, targetNode, workItem.toNode.Port, workItem.ipPreference, workItem.toNode.ConnectIP)
				if resolveErr == nil {
					targetIP = resolvedIP
					targetPort = resolvedPort
				}
			}
		}
		// L4-07：占位项也不编造端口——链行无真实监听口（port≤0）时
		// 不走解析器回退链、保持 0，最终结果项由 appendChainHopDiagnosis
		// 的守卫以「未分配监听端口」失败项替换本占位项（旧实现填 443）。

		nodeName := fmt.Sprintf("node_%d", workItem.fromNodeID)
		if node, err := h.cachedNode(nodeCache, workItem.fromNodeID); err == nil && strings.TrimSpace(node.Name) != "" {
			nodeName = node.Name
		}

		item := map[string]interface{}{
			"success":     false,
			"diagnosing":  true,
			"description": workItem.description,
			"nodeName":    nodeName,
			"nodeId":      strconv.FormatInt(workItem.fromNodeID, 10),
			"targetIp":    targetIP,
			"targetPort":  targetPort,
			"message":     "诊断中...",
		}
		for key, value := range workItem.metadata {
			item[key] = value
		}
		items = append(items, item)
	}

	return items
}

const diagnosisMaxConcurrency = 8

const (
	defaultNodeCommandTimeout  = 6 * time.Second
	diagnosisCommandTimeout    = 30 * time.Second
	diagnosisRequestTimeout    = 2 * time.Minute
	diagnosisCommandTimeoutMsg = "诊断超时（30秒）"
	diagnosisRequestTimeoutMsg = "诊断超时（2分钟）"
)

func (h *Handler) resolveForwardAccess(r *http.Request, forwardID int64) (*forwardRecord, int64, int, error) {
	userID, roleID, err := userRoleFromRequest(r)
	if err != nil {
		return nil, 0, 0, err
	}
	forward, err := h.ensureForwardAccessByActor(userID, roleID, forwardID)
	if err != nil {
		return nil, userID, roleID, err
	}
	return forward, userID, roleID, nil
}

// isForwardPendingDelete 判定转发是否为 pending-delete 状态（R5-B3 D1-001）。
// D15：所有"激活类"路径必须显式调用此守卫，禁止复活 pending 行。
func isForwardPendingDelete(forward *forwardRecord) bool {
	return forward != nil && forward.Status == model.ForwardStatusPendingDelete
}

// ActivateForwardIfNotPending 是 UpdateForwardStatus(id, 1, ...) 的语义化封装
// （R5-B5 D15）。所有把 status 写成 1（active）的路径必须走此函数，
// pending-delete 行会被拒绝并返回哨兵错误，调用方不得忽略。
var errForwardPendingDelete = errors.New("forward is pending delete")

func (h *Handler) ActivateForwardIfNotPending(forwardID int64, now int64) error {
	if h == nil || h.repo == nil {
		return errors.New("handler not initialized")
	}
	// F-02：原子条件更新——检查与写入合一（WHERE status<>2），
	// 消除先读后写的 TOCTOU：并发删除已置 pending 的行不再被复活。
	updated, err := h.repo.UpdateForwardStatusIfNotPending(forwardID, model.ForwardStatusActive, now)
	if err != nil {
		return markRepoError(err)
	}
	if updated {
		return nil
	}
	// 0 行受影响：行不存在或已 pending-delete，读一次区分。
	forward, readErr := h.getForwardRecord(forwardID)
	if readErr != nil {
		return readErr
	}
	if isForwardPendingDelete(forward) {
		return errForwardPendingDelete
	}
	// 行存在且非 pending（已是 active 且 updated_time 恰与 now
	// 相同致驱动报 0 行）：激活后置条件已成立，按成功返回。
	return nil
}

func (h *Handler) ensureForwardAccessByActor(actorUserID int64, actorRole int, forwardID int64) (*forwardRecord, error) {
	forward, err := h.getForwardRecord(forwardID)
	if err != nil {
		return nil, err
	}
	// R5-B3 D1-001 矩阵 #10: pending-delete 行对用户/管理员详情隐藏，视为不存在（编辑/查看/再次删除都不可达）。
	if isForwardPendingDelete(forward) {
		return nil, errForwardNotFound
	}
	if actorRole != 0 && forward.UserID != actorUserID {
		return nil, errForwardNotFound
	}
	return forward, nil
}

func (h *Handler) ensureTunnelPermission(userID int64, roleID int, tunnelID int64) error {
	if roleID == 0 {
		return nil
	}
	ok, err := h.repo.UserTunnelExistsByUserAndTunnel(userID, tunnelID)
	if err != nil {
		return markRepoError(err)
	}
	if !ok {
		return errors.New("你没有该隧道的权限")
	}
	return nil
}

func (h *Handler) getForwardRecord(forwardID int64) (*forwardRecord, error) {
	fr, err := h.repo.GetForwardRecord(forwardID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if fr == nil {
		return nil, errForwardNotFound
	}
	return fr, nil
}

func (h *Handler) getTunnelRecord(tunnelID int64) (*tunnelRecord, error) {
	tr, err := h.repo.GetTunnelRecord(tunnelID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if tr == nil {
		return nil, errors.New("隧道不存在")
	}
	return tr, nil
}

// getTunnelRecordRaw is the unnormalized variant of getTunnelRecord
// (R4X-004): write-back paths that read the old row to backfill absent
// fields must use it, so stored zero values survive the round trip.
func (h *Handler) getTunnelRecordRaw(tunnelID int64) (*tunnelRecord, error) {
	tr, err := h.repo.GetTunnelRecordRaw(tunnelID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if tr == nil {
		return nil, errors.New("隧道不存在")
	}
	return tr, nil
}

func (h *Handler) listForwardsByTunnel(tunnelID int64) ([]forwardRecord, error) {
	return h.repo.ListForwardsByTunnel(tunnelID)
}

func (h *Handler) listForwardPorts(forwardID int64) ([]forwardPortRecord, error) {
	return h.repo.ListForwardPorts(forwardID)
}

func (h *Handler) getNodeRecord(nodeID int64) (*nodeRecord, error) {
	n, err := h.repo.GetNodeRecord(nodeID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if n == nil {
		return nil, errors.New("节点不存在")
	}
	return n, nil
}

func (h *Handler) resolveUserTunnelAndLimiter(userID, tunnelID int64) (int64, *int64, *int, error) {
	info, err := h.repo.ResolveUserTunnelAndLimiter(userID, tunnelID)
	if err != nil {
		return 0, nil, nil, markRepoError(err)
	}
	if info == nil {
		return 0, nil, nil, nil
	}
	return info.UserTunnelID, info.LimiterID, info.Speed, nil
}

func (h *Handler) listUserTunnelIDs(userID, tunnelID int64) ([]int64, error) {
	return h.repo.ListUserTunnelIDs(userID, tunnelID)
}

func (h *Handler) listUserTunnelIDsByUser(userID int64) ([]int64, error) {
	return h.repo.ListUserTunnelIDsByUser(userID)
}

func (h *Handler) syncForwardServices(forward *forwardRecord, method string, allowFallbackAdd bool) error {
	_, err := h.syncForwardServicesWithWarnings(forward, method, allowFallbackAdd)
	return err
}

func (h *Handler) syncForwardServicesWithWarnings(forward *forwardRecord, method string, allowFallbackAdd bool, oldForwards ...*forwardRecord) ([]string, error) {
	if h == nil || forward == nil {
		return nil, errors.New("invalid forward sync context")
	}
	// R20-X-01 ①：可选的更新前快照。仅当调用方持有更新前的转发记录
	// 时传入（forwardUpdate / 批量换隧道 / 隧道删除决议）；无快照的
	// 重推路径（创建、限速重同步、relay 修复）传空，不触发清理。
	var oldForward *forwardRecord
	if len(oldForwards) > 0 {
		oldForward = oldForwards[0]
	}
	// R5-B3 D1-001 补充 D 兜底：pending-delete 行不得被任何同步路径触碰
	//（删除流程由 pending 机制接管）。直接调用 getForwardRecord 的后台路径
	//（如 relay 修复）可能绕过 ensureForwardAccessByActor 的过滤。
	if isForwardPendingDelete(forward) {
		return nil, nil
	}

	tunnel, err := h.getTunnelRecord(forward.TunnelID)
	if err != nil {
		return nil, err
	}
	nftMode, entryNodeIDs, err := h.tunnelUsesNftables(forward.TunnelID)
	if err != nil {
		return nil, err
	}
	if nftMode {
		// R35-FIX-F01：realm 隧道绝不能走 nft 分支——nft 分支优先于
		// realm 分支会使 realm 配置不被触碰。realm 隧道入口含 nft
		// 节点即为混合态（validateRealmTunnelTx 禁止该组合），拒绝
		// 而不是静默按 nft 下发。
		if tunnelIsRealm(tunnel) {
			return nil, fmt.Errorf("隧道 %d 使用 realm 引擎，入口节点不能为 nftables 模式", forward.TunnelID)
		}
		if err := h.validateNftablesForwardRequest(tunnel, forward.RemoteAddr, entryNodeIDs); err != nil {
			return nil, err
		}
		if len(entryNodeIDs) == 0 {
			return nil, errors.New("nftables 转发缺少入口节点")
		}
		// R3X-019：nftables 渲染无 rate/connlimit 表达，限速/连接数
		// 不执法——按 realm 同谓词（forwardHasIneffectiveLimits：
		// status=1 且 speed>0、连接数裸值、含 UT 级）生成警告并入
		// 本函数的 warnings 回执通道，不再静默丢弃；禁用/零速的
		// 引用本就不生效，不误报。
		var warnings []string
		ineffective, warnErr := h.forwardHasIneffectiveLimits(forward)
		if warnErr != nil {
			return nil, warnErr
		}
		if ineffective {
			warnings = append(warnings, "nftables 模式不支持限速和连接数限制，这些设置不生效")
		}
		if err := h.syncNftablesNode(entryNodeIDs[0]); err != nil {
			return warnings, err
		}
		return warnings, nil
	}
	if tunnelIsRealm(tunnel) {
		return h.syncRealmForward(forward, nil)
	}
	ports, err := h.listForwardPorts(forward.ID)
	if err != nil {
		return nil, err
	}
	if len(ports) == 0 {
		return nil, errors.New("转发入口端口不存在")
	}
	warnings := make([]string, 0)

	// Resolve user tunnel first so runtime service name can carry the real user_tunnel id.
	userTunnelID, utLimiterID, utSpeed, err := h.resolveUserTunnelAndLimiter(forward.UserID, forward.TunnelID)
	if err != nil {
		return nil, err
	}

	// Determine limiter from forward's SpeedID first, fallback to UserTunnel's limiter
	var limiterID *int64
	var speed *int

	if forward.SpeedID.Valid && forward.SpeedID.Int64 > 0 {
		// Forward has its own speed limit
		speedVal, err := h.repo.GetSpeedLimitSpeed(forward.SpeedID.Int64)
		if err == nil && speedVal > 0 {
			limiterID = &forward.SpeedID.Int64
			speed = &speedVal
		}
	}

	if limiterID == nil {
		// Fall back to UserTunnel speed limit
		limiterID = utLimiterID
		speed = utSpeed
	}

	var ipSpeed *int
	if forward.IPSpeedID.Valid && forward.IPSpeedID.Int64 > 0 {
		if speedVal, err := h.repo.GetSpeedLimitSpeed(forward.IPSpeedID.Int64); err == nil && speedVal > 0 {
			ipSpeed = &speedVal
		}
	}

	serviceBase := buildForwardServiceBaseWithResolvedUserTunnel(forward.ID, forward.UserID, userTunnelID)

	user, err := h.repo.GetUserByID(forward.UserID)
	if err != nil {
		return nil, err
	}

	userMaxConn := 0
	if user != nil && user.MaxConn > 0 {
		userMaxConn = user.MaxConn
	}
	connLimiterConfigs := buildConnLimiterConfigs(forward, userMaxConn)

	// R20-X-01 ①：本轮服务推送成功的入口节点（去重）。循环内任一节点
	// 非离线失败即整函数返回错误、调用方回滚 DB，故失势 limiter 只能
	// 在循环全部成功后删——循环内删会在后继节点失败回滚后造成“DB 仍
	// 有限流、节点 limiter 已删”的窗口。离线跳过的节点不进本集。
	pushedNodeIDs := make([]int64, 0, len(ports))
	pushedNodeSeen := make(map[int64]struct{}, len(ports))
	for _, fp := range ports {
		runtimeLimiters := forwardRuntimeLimiters{ConnLimiter: joinLimiterNames(connLimiterConfigs)}
		trafficLimiterNames := make([]string, 0, 2)
		if limiterID != nil && speed != nil {
			totalLimiterName := strconv.FormatInt(*limiterID, 10)
			if err := h.ensureLimiterOnNode(fp.NodeID, *limiterID, *speed); err != nil {
				// AUDIT-R7 (R5R7-001): 限速是关键策略，不可静默跳过。
				// 节点离线时直接失败（fail-closed），而非告警后继续——
				// 后者会导致转发永久无速限运行（无补下发机制）。
				if isNodeOfflineOrTimeoutError(err) {
					node, _ := h.getNodeRecord(fp.NodeID)
					nodeName := fmt.Sprintf("%d", fp.NodeID)
					if node != nil && strings.TrimSpace(node.Name) != "" {
						nodeName = strings.TrimSpace(node.Name)
					}
					return nil, fmt.Errorf("节点 %s 不在线，限速无法下发，请稍后重试", nodeName)
				}
				return nil, err
			}
			trafficLimiterNames = append(trafficLimiterNames, totalLimiterName)
		}
		if ipSpeed != nil {
			ruleLimiterName := fmt.Sprintf("rule_traffic_limit_%d", forward.ID)
			if err := h.ensureTrafficLimiterOnNode(fp.NodeID, ruleLimiterName, nil, ipSpeed); err != nil {
				// AUDIT-R7 (R5R7-001): 同上，IP 限速亦为关键策略，离线时 fail-closed。
				if isNodeOfflineOrTimeoutError(err) {
					node, _ := h.getNodeRecord(fp.NodeID)
					nodeName := fmt.Sprintf("%d", fp.NodeID)
					if node != nil && strings.TrimSpace(node.Name) != "" {
						nodeName = strings.TrimSpace(node.Name)
					}
					return nil, fmt.Errorf("节点 %s 不在线，限速无法下发，请稍后重试", nodeName)
				}
				return nil, err
			}
			trafficLimiterNames = append(trafficLimiterNames, ruleLimiterName)
		}
		runtimeLimiters.TrafficLimiter = strings.Join(trafficLimiterNames, ",")

		for _, connLimiterConfig := range connLimiterConfigs {
			if err := h.ensureConnLimiterOnNode(fp.NodeID, connLimiterConfig); err != nil {
				warnings = append(warnings, fmt.Sprintf("节点 %d 连接限制器下发失败: %v", fp.NodeID, err))
			}
		}

		node, err := h.getNodeRecord(fp.NodeID)
		if err != nil {
			return nil, err
		}
		services := buildForwardServiceConfigs(serviceBase, forward, tunnel, node, fp.Port, strings.TrimSpace(fp.InIP), runtimeLimiters)
		_, err = h.sendNodeCommand(node.ID, method, services, true, false)
		if err == nil && (method == "UpdateService" || method == "AddService") {
			// R17-B：UpdateService 成功即 agent 对这些服务名的注册确认
			//（upsert 语义：不存在则创建），记入确认集供补发守卫使用。
			h.noteAddServiceConfirmed(node.ID, extractAddServiceNames(services))
		}
		if err == nil && strings.TrimSpace(fp.InIP) != "" {
			// B08-005：显式监听 IP 已被 agent 接受，清掉该端口历史的
			// 回退告警（best-effort，失败只记日志不挡同步）。
			if cerr := h.repo.ClearForwardPortBindWarning(forward.ID, node.ID, fp.Port); cerr != nil {
				log.Printf("forward bind warning clear failed forward_id=%d node_id=%d port=%d: %v", forward.ID, node.ID, fp.Port, cerr)
			}
		}
		if err != nil && allowFallbackAdd && method == "UpdateService" {
			if isNotFoundError(err) {
				if delErr := h.deleteForwardServicesOnNode(forward, node.ID); delErr != nil && !isNotFoundError(delErr) {
					return warnings, fmt.Errorf("节点 %s 清理旧服务失败: %w", node.Name, delErr)
				}
			}
			// R17-B：回退 AddService 走幂等守卫。freshAbsence 一律为真：
			// 刚失败的 UpdateService 本身就是新鲜否定证据——无论错误
			// 文本是 not-found、超时还是 bind 失败，失败都意味着期望
			// 注册态未达成，旧确认不得复用（R3-GATE-F3 / R4B6 回归：
			// 仅 not-found 才作废时，setup 期成功的确认会让 bind 失败
			// 后的补发被跳过，forwardUpdate 被误判成功）。
			_, err = h.guardedAddService(node.ID, services, "update-fallback", true)
		}
		if err != nil && strings.EqualFold(strings.TrimSpace(method), "UpdateService") && isAddressAlreadyInUseError(err) {
			err = h.rebindForwardServiceOnSelfOccupiedPort(forward, node, fp.Port, services)
		}
		if err != nil && strings.EqualFold(strings.TrimSpace(method), "UpdateService") && isCannotAssignRequestedAddressError(err) {
			var warning string
			warning, err = h.fallbackForwardPortToDefaultBind(forward, tunnel, node, fp, serviceBase, runtimeLimiters)
			if err == nil && warning != "" {
				warnings = append(warnings, warning)
			}
		}
		// When a node is offline, skip it with a warning instead of failing.
		// This lets users modify forward rules even when some entry nodes are down.
		if err != nil && isNodeOfflineOrTimeoutError(err) {
			warnings = append(warnings, fmt.Sprintf("节点 %s 不在线，已跳过下发", node.Name))
			continue
		}
		if err != nil {
			return warnings, fmt.Errorf("节点 %s 下发失败: %w", node.Name, err)
		}
		if _, ok := pushedNodeSeen[node.ID]; !ok {
			pushedNodeSeen[node.ID] = struct{}{}
			pushedNodeIDs = append(pushedNodeIDs, node.ID)
		}
	}

	// R20-X-01 ①：全部入口节点服务推送成功后，删更新失势的 rule climiter
	//（先推服务后删 limiter；离线节点本轮未推，不删，留待下次成功同步
	// ——重连重推 redeployLocalNodeRuntime 在重推成功后按 ID 补删
	// （R21-X-02 deleteRedeployStaleRuleConnLimiter）——或删除兜底 ②）。
	// 失败只记日志、不挡更新回执（helper 内处理）。
	// FIX-RESIDUAL-2（A1-02 同族）：同序补删更新失势的流量 limiter 条目
	//（总速规则条目＋rule_traffic_limit_<id>）；离线节点同上不删，重连后
	// 由配置上报对账 cleanNodeConfigs 按全集差集兜底（与 A1-02 口径一致，
	// 本类不设重推补删）。
	for _, nodeID := range pushedNodeIDs {
		h.deleteStaleForwardConnLimiterOnNode(oldForward, forward, nodeID)
		h.deleteStaleForwardTrafficLimitersOnNode(oldForward, forward, nodeID)
	}

	// Keep paused forwards paused after UpdateService/AddService, since agent-side UpdateService
	// always restarts services.
	if forward.Status != 1 {
		if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

func (h *Handler) fallbackForwardPortToDefaultBind(forward *forwardRecord, tunnel *tunnelRecord, node *nodeRecord, fp forwardPortRecord, serviceBase string, runtimeLimiters forwardRuntimeLimiters) (string, error) {
	if h == nil || forward == nil || tunnel == nil || node == nil {
		return "", errors.New("invalid bind fallback context")
	}
	if fp.Port <= 0 {
		return "", errors.New("invalid forward port")
	}
	explicitBindIP := strings.TrimSpace(fp.InIP)
	if explicitBindIP == "" {
		return "", errors.New("default bind address cannot be assigned")
	}

	if err := h.deleteForwardServicesOnNode(forward, node.ID); err != nil {
		return "", err
	}

	time.Sleep(150 * time.Millisecond)
	defaultServices := buildForwardServiceConfigs(serviceBase, forward, tunnel, node, fp.Port, "", runtimeLimiters)
	// R17-B：默认监听回退的 AddService 走幂等守卫；刚删过旧服务，
	// freshAbsence=true（新鲜缺失证据），不得用旧确认跳过。
	if _, err := h.guardedAddService(node.ID, defaultServices, "default-bind-fallback", true); err != nil {
		return "", err
	}
	if err := h.repo.UpdateForwardPortBindIP(forward.ID, node.ID, fp.Port, ""); err != nil {
		return "", err
	}

	warning := fmt.Sprintf("节点 %s 监听IP %s 不在主机网卡地址中，已自动回退为默认监听IP", strings.TrimSpace(node.Name), explicitBindIP)
	// B08-005：回退事件持久落到转发端口行，经转发列表 bindWarnings
	// 长期可见——本函数的返回值只抵达当次同步的调用方，节点重连后的
	// 后台重推触发回退时没有用户在场接收。落库失败只记日志：回退本身
	// 已成功，不得因告警留痕失败把同步判失败。
	if werr := h.repo.UpdateForwardPortBindWarning(forward.ID, node.ID, fp.Port, warning); werr != nil {
		log.Printf("forward bind fallback warning persist failed forward_id=%d node_id=%d port=%d: %v", forward.ID, node.ID, fp.Port, werr)
	}
	return warning, nil
}

func (h *Handler) rebindForwardServiceOnSelfOccupiedPort(forward *forwardRecord, node *nodeRecord, port int, services []map[string]interface{}) error {
	if h == nil || forward == nil || node == nil {
		return errors.New("invalid self-occupy rebind context")
	}
	if port <= 0 {
		return errors.New("invalid forward port")
	}

	conflict, err := h.repo.FindOtherForwardOnNodePort(node.ID, port, forward.ID)
	if err != nil {
		return err
	}
	if conflict != nil {
		// L9 观察 2：三源判重命中的未必是转发，按来源措辞（助手在
		// mutations.go），不误报「已被其他转发占用」。
		return errors.New(forwardPortConflictMessage(node, port, conflict))
	}

	bases, err := h.forwardServiceBaseCandidates(forward)
	if err != nil {
		return err
	}

	if err := h.deleteForwardServiceBasesOnNode(node.ID, bases); err != nil {
		return err
	}

	time.Sleep(150 * time.Millisecond)

	// R17-B：自占端口重绑的 AddService 走幂等守卫；刚删过旧服务，
	// freshAbsence=true（新鲜缺失证据），不得用旧确认跳过。
	_, err = h.guardedAddService(node.ID, services, "self-occupied-rebind", true)
	if err != nil {
		return err
	}

	return nil
}

func (h *Handler) deleteForwardServicesOnNode(forward *forwardRecord, nodeID int64) error {
	if h == nil || forward == nil {
		return errors.New("invalid forward delete context")
	}
	bases, err := h.forwardServiceBaseCandidates(forward)
	if err != nil {
		return err
	}
	return h.deleteForwardServiceBasesOnNode(nodeID, bases)

}

func (h *Handler) forwardServiceBaseCandidates(forward *forwardRecord) ([]string, error) {
	if h == nil || forward == nil {
		return nil, errors.New("invalid forward service base context")
	}
	userTunnelID, _, _, err := h.resolveUserTunnelAndLimiter(forward.UserID, forward.TunnelID)
	if err != nil {
		return nil, err
	}
	userTunnelIDs, err := h.listUserTunnelIDs(forward.UserID, forward.TunnelID)
	if err != nil {
		return nil, err
	}
	allUserTunnelIDs, err := h.listUserTunnelIDsByUser(forward.UserID)
	if err != nil {
		return nil, err
	}
	candidateTunnelIDs := make([]int64, 0, len(userTunnelIDs)+len(allUserTunnelIDs))
	candidateTunnelIDs = append(candidateTunnelIDs, userTunnelIDs...)
	candidateTunnelIDs = append(candidateTunnelIDs, allUserTunnelIDs...)
	return buildForwardServiceBaseCandidates(forward.ID, forward.UserID, userTunnelID, candidateTunnelIDs), nil

}

func (h *Handler) deleteForwardServiceBasesOnNode(nodeID int64, bases []string) error {
	names := buildForwardServiceDeleteNames(bases)
	if len(names) == 0 {
		return nil
	}
	payload := map[string]interface{}{"services": names}
	_, err := h.sendNodeCommand(nodeID, "DeleteService", payload, false, true)
	if err == nil {
		// R17-B：DeleteService 成功（已不存在也被容忍为成功）即新鲜
		// 否定证据，作废这些服务名的确认条目。
		h.invalidateAddServiceNames(nodeID, names)
	}
	return err
}

func buildForwardServiceDeleteNames(bases []string) []string {
	names := make([]string, 0, len(bases)*3)
	seen := make(map[string]struct{}, len(bases)*3)
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, base := range bases {
		base = strings.TrimSpace(base)
		if base == "" {
			continue
		}
		appendName(base + "_tcp")
		appendName(base + "_udp")
		appendName(base)
	}
	return names
}

func (h *Handler) controlForwardServices(forward *forwardRecord, commandType string, tolerateNotFound bool) error {
	if h == nil || forward == nil {
		return errors.New("invalid forward control context")
	}
	if tunnel, err := h.getTunnelRecord(forward.TunnelID); err == nil && tunnelIsRealm(tunnel) {
		return h.controlRealmForward(forward, commandType)
	}
	ports, err := h.listForwardPorts(forward.ID)
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		return nil
	}
	userTunnelID, _, _, err := h.resolveUserTunnelAndLimiter(forward.UserID, forward.TunnelID)
	if err != nil {
		return err
	}
	userTunnelIDs, err := h.listUserTunnelIDs(forward.UserID, forward.TunnelID)
	if err != nil {
		return err
	}
	allUserTunnelIDs, err := h.listUserTunnelIDsByUser(forward.UserID)
	if err != nil {
		return err
	}
	candidateTunnelIDs := make([]int64, 0, len(userTunnelIDs)+len(allUserTunnelIDs))
	candidateTunnelIDs = append(candidateTunnelIDs, userTunnelIDs...)
	candidateTunnelIDs = append(candidateTunnelIDs, allUserTunnelIDs...)
	bases := buildForwardServiceBaseCandidates(forward.ID, forward.UserID, userTunnelID, candidateTunnelIDs)
	if strings.EqualFold(strings.TrimSpace(commandType), "DeleteService") {
		seen := map[int64]struct{}{}
		var offlineNodes []int64
		for _, fp := range ports {
			if _, ok := seen[fp.NodeID]; ok {
				continue
			}
			seen[fp.NodeID] = struct{}{}
			deleteErr := h.deleteForwardServiceBasesOnNode(fp.NodeID, bases)
			if deleteErr != nil {
				if isNodeOfflineOrTimeoutError(deleteErr) {
					// S-04: 节点离线时不得静默跳过，记录离线节点，调用方决定如何处理
					// （调用方 forwardDelete 会检查此错误并避免删库）
					offlineNodes = append(offlineNodes, fp.NodeID)
					continue
				}
				if !(tolerateNotFound && isNotFoundError(deleteErr)) {
					return deleteErr
				}
				// 落到此处：服务已不在且调用方容忍缺失，仍清 climiter 收敛。
			}
			// R19-X-03：服务已删后清该转发的 rule 级 climiter。离线节点到不了
			// 这里（上支 continue，已记入 offlineNodes、调用方不删库，重连
			// 清理经同一漏斗重试）；其余失败由 helper 内记日志、不阻塞删库。
			h.deleteForwardConnLimiterOnNode(forward, fp.NodeID)
			// A1-02：同漏斗清该转发的流量 limiter（转发专属条目恒删、
			// 共享总速规则条目仅在无其他存续引用时删）。
			h.deleteForwardTrafficLimitersOnNode(forward, fp.NodeID)
		}
		if len(offlineNodes) > 0 {
			// S-04: 返回离线节点信息，调用方不得静默返回成功
			return &nodeOfflineError{nodeIDs: offlineNodes}
		}
		return nil
	}
	seen := map[int64]struct{}{}
	healed := false
	for _, fp := range ports {
		if _, ok := seen[fp.NodeID]; ok {
			continue
		}
		seen[fp.NodeID] = struct{}{}

		nodeHandled, lastNotFoundErr, err := h.controlForwardServicesOnNode(fp.NodeID, bases, commandType)
		if err != nil {
			if isNodeOfflineOrTimeoutError(err) {
				continue
			}
			return err
		}

		if !nodeHandled && lastNotFoundErr != nil && !healed && shouldSelfHealForwardServiceControl(commandType) {
			if healErr := h.syncForwardServices(forward, "UpdateService", true); healErr != nil {
				return healErr
			}
			healed = true
			nodeHandled, lastNotFoundErr, err = h.controlForwardServicesOnNode(fp.NodeID, bases, commandType)
			if err != nil {
				return err
			}
		}

		if nodeHandled {
			continue
		}
		if tolerateNotFound {
			continue
		}
		if lastNotFoundErr != nil {
			return lastNotFoundErr
		}
		return errors.New("service control failed")
	}
	return nil
}

func (h *Handler) controlForwardServicesOnNode(nodeID int64, bases []string, commandType string) (bool, error, error) {
	return controlForwardServiceCommand(bases, commandType, func(name string) error {
		payload := map[string]interface{}{
			"services": []string{name},
		}
		_, err := h.sendNodeCommand(nodeID, commandType, payload, false, false)
		return err
	})
}

func controlForwardServiceCommand(bases []string, commandType string, send func(name string) error) (bool, error, error) {
	var lastNotFoundErr error
	for _, base := range bases {
		variants := []string{base + "_tcp", base + "_udp"}
		if shouldTryLegacySingleService(commandType) || strings.EqualFold(strings.TrimSpace(commandType), "DeleteService") {
			variants = append(variants, base)
		}

		candidateHandled := false
		for _, name := range variants {
			err := send(name)
			if err == nil {
				candidateHandled = true
				continue
			}
			if !isNotFoundError(err) {
				return false, lastNotFoundErr, err
			}
			lastNotFoundErr = err
		}

		if candidateHandled {
			return true, nil, nil
		}
	}
	return false, lastNotFoundErr, nil
}

func deleteForwardServiceCandidates(bases []string, send func(name string) error) error {
	for _, base := range bases {
		for _, name := range append([]string{base + "_tcp", base + "_udp", base}, []string{}...) {
			err := send(name)
			if err == nil {
				continue
			}
			if isNotFoundError(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func shouldSelfHealForwardServiceControl(commandType string) bool {
	cmd := strings.ToLower(strings.TrimSpace(commandType))
	return cmd == "pauseservice" || cmd == "resumeservice"
}

func (h *Handler) applyNodeProtocolChange(nodeID int64, httpVal, tlsVal, socksVal int) error {
	_, err := h.sendNodeCommand(nodeID, "SetProtocol", map[string]interface{}{
		"http":  httpVal,
		"tls":   tlsVal,
		"socks": socksVal,
	}, false, false)
	return err
}

func (h *Handler) sendNodeCommand(nodeID int64, commandType string, data interface{}, tolerateExists bool, tolerateNotFound bool) (ws.CommandResult, error) {
	return h.sendNodeCommandWithTimeout(nodeID, commandType, data, defaultNodeCommandTimeout, tolerateExists, tolerateNotFound)
}

func (h *Handler) sendNodeCommandWithTimeout(nodeID int64, commandType string, data interface{}, timeout time.Duration, tolerateExists bool, tolerateNotFound bool) (ws.CommandResult, error) {
	var (
		result ws.CommandResult
		err    error
	)
	if timeout <= 0 {
		timeout = defaultNodeCommandTimeout
	}

	result, err = h.wsServer.SendCommand(nodeID, commandType, data, timeout)
	if err == nil {
		return result, nil
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if tolerateExists {
		if isAlreadyExistsMessage(msg) {
			return result, nil
		}
	}
	if tolerateNotFound {
		if strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") {
			return result, nil
		}
	}
	return result, err
}

func (h *Handler) diagnoseForwardRuntime(ctx context.Context, forward *forwardRecord) (map[string]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := h.prepareForwardDiagnosisPlan(forward)
	if err != nil {
		return nil, err
	}

	results := make([]map[string]interface{}, 0, len(plan.statusItems)+len(plan.workItems))
	results = append(results, plan.statusItems...)
	results = append(results, h.runDiagnosisWorkItems(ctx, plan.workItems, nil)...)

	payload := map[string]interface{}{
		"forwardName": plan.name,
		"timestamp":   time.Now().UnixMilli(),
		"results":     results,
	}
	return payload, nil
}

// forwardDiagnosisPlan 是非流式（diagnoseForwardRuntime）与流式
// （forwardDiagnoseStream）两个转发诊断端点共用的准备结果
// （RV3-01）。statusItems 是无需探测的既成结果项（nft 规则未
// applied 入口的规则状态失败项），workItems 是真实探测项；两面
// 的结果集恒为 statusItems ++ workItems 的执行结果。
//
// 此前只有非流式面经 diagnoseNftablesForwardRuntime 走 nft 分支，
// 流式面（前端诊断弹窗首选）直走 prepareForwardDiagnosis：nft
// 转发在规则缺失/下发失败时流式面只剩与规则无关的 agent 直拨
// 探测、目标可达即全绿，与非流式面的规则状态结论互相矛盾
// （L4-04 同机制残留面）。nft 判定与结果构造前置到本 prepare
// 层后，两面同口径。
type forwardDiagnosisPlan struct {
	name        string
	statusItems []map[string]interface{}
	workItems   []diagnosisWorkItem
}

func (h *Handler) prepareForwardDiagnosisPlan(forward *forwardRecord) (*forwardDiagnosisPlan, error) {
	if forward == nil {
		return nil, errForwardNotFound
	}
	nftMode, entryNodeIDs, err := h.tunnelUsesNftables(forward.TunnelID)
	if err != nil {
		return nil, err
	}
	if !nftMode {
		name, workItems, err := h.prepareForwardDiagnosis(forward)
		if err != nil {
			return nil, err
		}
		return &forwardDiagnosisPlan{name: name, workItems: workItems}, nil
	}
	if len(entryNodeIDs) == 0 {
		return nil, errors.New("nftables 转发缺少入口节点")
	}
	targets, err := resolveDiagnosisTargets(forward.RemoteAddr)
	if err != nil {
		return nil, err
	}
	// L4-05：逐个入口节点诊断。此前只查 entryNodeIDs[0]，多入口
	// 转发其余入口的规则缺失/失败完全不检，诊断全绿也可能有入口
	// 未下发。
	plan := &forwardDiagnosisPlan{name: forward.Name}
	for _, nodeID := range entryNodeIDs {
		nodePlan, err := h.planNftablesForwardDiagnosis(forward, nodeID, targets)
		if err != nil {
			return nil, err
		}
		if nodePlan.statusItem != nil {
			plan.statusItems = append(plan.statusItems, nodePlan.statusItem)
		}
		plan.workItems = append(plan.workItems, nodePlan.probeItems...)
	}
	return plan, nil
}

// nftForwardDiagnosisPlan 是单个入口节点的 nft 转发诊断计划：
// 规则未 applied 时只有 statusItem（既成失败项，不探测）；
// applied 时只有 probeItems（真实探测项，结果项产出后经其
// decorate 并入规则字段）。
type nftForwardDiagnosisPlan struct {
	statusItem map[string]interface{}
	probeItems []diagnosisWorkItem
}

func (h *Handler) planNftablesForwardDiagnosis(forward *forwardRecord, nodeID int64, targets []diagnosisTarget) (*nftForwardDiagnosisPlan, error) {
	if h == nil || h.repo == nil {
		return nil, errors.New("handler not initialized")
	}
	node, err := h.getNodeRecord(nodeID)
	if err != nil {
		return nil, err
	}
	bindings, err := h.repo.ListNftRuleBindingsByNode(nodeID)
	if err != nil {
		return nil, err
	}
	var binding *model.NftRuleBinding
	for i := range bindings {
		if bindings[i].ForwardID == forward.ID {
			binding = &bindings[i]
			break
		}
	}
	target := diagnosisTarget{}
	if len(targets) > 0 {
		target = targets[0]
	}

	status := "missing"
	message := "nftables 规则未下发"
	inPort := 0
	protocols := ""
	targetAddr := strings.TrimSpace(forward.RemoteAddr)
	ruleHash := ""
	if binding != nil {
		status = strings.ToLower(strings.TrimSpace(binding.Status))
		inPort = binding.InPort
		protocols = strings.TrimSpace(binding.Protocols)
		targetAddr = strings.TrimSpace(binding.TargetAddr)
		ruleHash = strings.TrimSpace(binding.RuleHash)
		if status == "" {
			status = "pending"
		}
		if status != runtimenft.StatusApplied {
			if strings.TrimSpace(binding.LastError) != "" {
				message = binding.LastError
			} else {
				message = "nftables 规则未完成下发"
			}
		}
	}

	nftFields := map[string]interface{}{
		"forwardMode":   "nftables",
		"nftRuleStatus": status,
		"nftRuleHash":   ruleHash,
		"inPort":        inPort,
		"protocols":     protocols,
		"targetAddr":    targetAddr,
	}

	// L4-04：规则未 applied 时该入口的转发路径本身就不成立，如实
	// 报失败＋规则状态原因（averageTime/packetLoss 取失败项的既有
	// 约定值，与探测失败项同形），不做连通探测、更不报成功。
	if status != runtimenft.StatusApplied {
		result := map[string]interface{}{
			"success":       false,
			"nodeName":      node.Name,
			"nodeId":        strconv.FormatInt(nodeID, 10),
			"targetIp":      target.IP,
			"targetPort":    target.Port,
			"description":   fmt.Sprintf("nftables规则(%s)->目标(%s)", node.Name, defaultString(target.Address, targetAddr)),
			"averageTime":   0,
			"packetLoss":    100,
			"message":       message,
			"fromChainType": 1,
		}
		for k, v := range nftFields {
			result[k] = v
		}
		return &nftForwardDiagnosisPlan{statusItem: result}, nil
	}

	// L4-04：规则已下发只说明数据面规则在位，不等于路径可达——旧
	// 实现据此直接报 success 并伪造 packetLoss=0/averageTime=0，
	// 与 gost 真实探测同表呈现，规则在位但目标不通时假阳性误导
	// 排障。现在对每个目标做与 gost 入口→目标腿相同的真实探测，
	// success/averageTime/packetLoss 全部来自探测结果，规则状态经
	// nftRuleStatus 与 message 前缀单独呈现——字段并入与前缀由每个
	// 探测项的 decorate 在结果产出时生效（runDiagnosisWorkItems
	// 统一调用），非流式/流式两面同形。
	// R7-028：目标腿只测 TCP——不再按 binding 协议集逐协议生成探测
	// 项（UDP 项不可实测、无可信信息，见
	// appendTargetDiagnosisWorkItems）；binding 的协议集仍经
	// nftFields 的 protocols 字段如实呈现。
	decorate := func(item map[string]interface{}) {
		for k, v := range nftFields {
			item[k] = v
		}
		if probeMsg := strings.TrimSpace(asString(item["message"])); probeMsg != "" {
			item["message"] = "nftables 规则已下发；" + probeMsg
		} else {
			item["message"] = "nftables 规则已下发"
		}
	}
	workItems := make([]diagnosisWorkItem, 0, len(targets))
	for _, tgt := range targets {
		description := fmt.Sprintf("入口(%s)->目标(%s)", node.Name, defaultString(tgt.Address, targetAddr))
		workItems = append(workItems, diagnosisWorkItem{
			fromNodeID:  nodeID,
			targetIP:    tgt.IP,
			targetPort:  tgt.Port,
			description: fmt.Sprintf("%s · TCP", description),
			protocol:    "tcp",
			metadata: map[string]interface{}{
				"fromChainType": 1,
				"protocol":      "tcp",
			},
			decorate: decorate,
		})
	}
	return &nftForwardDiagnosisPlan{probeItems: workItems}, nil
}

// appendTargetDiagnosisWorkItems 为一条「节点→目标」腿生成单个 TCP
// 探测项（R7-028，用户 2026-11-02 拍板，回退 L4-06 的双协议探测）。
// L4-06 曾按转发恒双协议服务目标生成 TCP+UDP 两项，但 agent 的
// UdpPing 只 Dial 不发包、测不了 UDP 通断，UDP 项无论何种结果都不
// 携带可信信息，且在生产环境恒呈不通/未验证、误导排障——故目标腿
// 只测 TCP。链路段 KCP/UDP 传输协议腿的「未验证」收口
// （appendPathDiagnosis 单点）是隧道自身传输、与目标腿无关，不动。
func appendTargetDiagnosisWorkItems(workItems []diagnosisWorkItem, fromNodeID int64, target diagnosisTarget, description string, fromChainType int) []diagnosisWorkItem {
	return append(workItems, diagnosisWorkItem{
		fromNodeID:  fromNodeID,
		targetIP:    target.IP,
		targetPort:  target.Port,
		description: fmt.Sprintf("%s · TCP", description),
		protocol:    "tcp",
		metadata: map[string]interface{}{
			"fromChainType": fromChainType,
			"protocol":      "tcp",
		},
	})
}

func (h *Handler) prepareForwardDiagnosis(forward *forwardRecord) (string, []diagnosisWorkItem, error) {
	if forward == nil {
		return "", nil, errForwardNotFound
	}
	targets, err := resolveDiagnosisTargets(forward.RemoteAddr)
	if err != nil {
		return "", nil, err
	}

	tunnel, err := h.getTunnelRecord(forward.TunnelID)
	if err != nil {
		return "", nil, err
	}

	chainRows, err := h.listChainNodesForTunnel(forward.TunnelID)
	if err != nil {
		return "", nil, err
	}
	if len(chainRows) == 0 {
		return "", nil, errors.New("隧道配置不完整")
	}

	ipPreference := h.repo.GetTunnelIPPreference(forward.TunnelID)
	protocol := strings.ToLower(strings.TrimSpace(tunnel.Protocol))

	inNodes, chainHops, outNodes := splitChainNodeGroups(chainRows)
	workItems := make([]diagnosisWorkItem, 0, len(chainRows)*2+len(targets))

	switch tunnel.Type {
	case 1:
		for _, inNode := range inNodes {
			for _, target := range targets {
				description := fmt.Sprintf("入口(%s)->目标(%s)", inNode.NodeName, target.Address)
				workItems = appendTargetDiagnosisWorkItems(workItems, inNode.NodeID, target, description, 1)
			}
		}
	case 2:
		for _, inNode := range inNodes {
			if len(chainHops) > 0 {
				for _, firstNode := range chainHops[0] {
					description := fmt.Sprintf("入口(%s)->第1跳(%s)", inNode.NodeName, firstNode.NodeName)
					workItems = append(workItems, diagnosisWorkItem{
						fromNodeID:   inNode.NodeID,
						toNode:       firstNode,
						hasChainHop:  true,
						ipPreference: ipPreference,
						description:  description,
						protocol:     defaultString(strings.ToLower(strings.TrimSpace(firstNode.Protocol)), protocol),
						metadata: map[string]interface{}{
							"fromChainType": 1,
							"toChainType":   2,
							"toInx":         firstNode.Inx,
						},
					})
				}
			} else {
				for _, outNode := range outNodes {
					description := fmt.Sprintf("入口(%s)->出口(%s)", inNode.NodeName, outNode.NodeName)
					workItems = append(workItems, diagnosisWorkItem{
						fromNodeID:   inNode.NodeID,
						toNode:       outNode,
						hasChainHop:  true,
						ipPreference: ipPreference,
						description:  description,
						protocol:     defaultString(strings.ToLower(strings.TrimSpace(outNode.Protocol)), protocol),
						metadata: map[string]interface{}{
							"fromChainType": 1,
							"toChainType":   3,
						},
					})
				}
			}
		}

		for i, hop := range chainHops {
			for _, currentNode := range hop {
				if i+1 < len(chainHops) {
					for _, nextNode := range chainHops[i+1] {
						description := fmt.Sprintf("第%d跳(%s)->第%d跳(%s)", i+1, currentNode.NodeName, i+2, nextNode.NodeName)
						workItems = append(workItems, diagnosisWorkItem{
							fromNodeID:   currentNode.NodeID,
							toNode:       nextNode,
							hasChainHop:  true,
							ipPreference: ipPreference,
							description:  description,
							protocol:     defaultString(strings.ToLower(strings.TrimSpace(nextNode.Protocol)), protocol),
							metadata: map[string]interface{}{
								"fromChainType": 2,
								"fromInx":       currentNode.Inx,
								"toChainType":   2,
								"toInx":         nextNode.Inx,
							},
						})
					}
				} else {
					for _, outNode := range outNodes {
						description := fmt.Sprintf("第%d跳(%s)->出口(%s)", i+1, currentNode.NodeName, outNode.NodeName)
						workItems = append(workItems, diagnosisWorkItem{
							fromNodeID:   currentNode.NodeID,
							toNode:       outNode,
							hasChainHop:  true,
							ipPreference: ipPreference,
							description:  description,
							protocol:     defaultString(strings.ToLower(strings.TrimSpace(outNode.Protocol)), protocol),
							metadata: map[string]interface{}{
								"fromChainType": 2,
								"fromInx":       currentNode.Inx,
								"toChainType":   3,
							},
						})
					}
				}
			}
		}

		for _, outNode := range outNodes {
			for _, target := range targets {
				description := fmt.Sprintf("出口(%s)->目标(%s)", outNode.NodeName, target.Address)
				workItems = appendTargetDiagnosisWorkItems(workItems, outNode.NodeID, target, description, 3)
			}
		}
	default:
		for _, inNode := range inNodes {
			for _, target := range targets {
				description := fmt.Sprintf("入口(%s)->目标(%s)", inNode.NodeName, target.Address)
				workItems = appendTargetDiagnosisWorkItems(workItems, inNode.NodeID, target, description, 1)
			}
		}
	}

	return forward.Name, workItems, nil
}

func (h *Handler) diagnoseTunnelRuntime(ctx context.Context, tunnelID int64) (map[string]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	tunnelName, tunnelType, workItems, err := h.prepareTunnelDiagnosis(tunnelID)
	if err != nil {
		return nil, err
	}

	results := h.runDiagnosisWorkItems(ctx, workItems, nil)

	payload := map[string]interface{}{
		"tunnelName": tunnelName,
		"tunnelType": tunnelType,
		"timestamp":  time.Now().UnixMilli(),
		"results":    results,
	}
	return payload, nil
}

func (h *Handler) prepareTunnelDiagnosis(tunnelID int64) (string, string, []diagnosisWorkItem, error) {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		return "", "", nil, err
	}

	tunnelName, err := h.repo.GetTunnelName(tunnelID)
	if err != nil {
		return "", "", nil, err
	}
	if tunnelName == "" {
		return "", "", nil, errors.New("隧道不存在")
	}

	chainRows, err := h.listChainNodesForTunnel(tunnelID)
	if err != nil {
		return "", "", nil, err
	}
	if len(chainRows) == 0 {
		return "", "", nil, errors.New("隧道配置不完整")
	}

	ipPreference := h.repo.GetTunnelIPPreference(tunnelID)
	protocol := strings.ToLower(strings.TrimSpace(tunnel.Protocol))
	// R3X-011 下发面：存量坏探测目标跳过外网腿（checked 内部已打
	// 告警日志），链路腿（节点间）不受影响、照常诊断。
	probeTarget, probeTargetOK := checkedTunnelProbeTargetValues(tunnel.ProbeTargetHost, tunnel.ProbeTargetPort)
	inNodes, chainHops, outNodes := splitChainNodeGroups(chainRows)
	workItems := make([]diagnosisWorkItem, 0, len(chainRows)*2)

	switch tunnel.Type {
	case 1:
		if probeTargetOK {
			for _, inNode := range inNodes {
				description := fmt.Sprintf("入口(%s)->外网", inNode.NodeName)
				workItems = append(workItems, diagnosisWorkItem{
					fromNodeID:  inNode.NodeID,
					targetIP:    probeTarget.Host,
					targetPort:  probeTarget.Port,
					description: description,
					protocol:    "tcp",
					metadata: map[string]interface{}{
						"fromChainType": 1,
					},
				})
			}
		}
	case 2:
		for _, inNode := range inNodes {
			if len(chainHops) > 0 {
				for _, firstNode := range chainHops[0] {
					description := fmt.Sprintf("入口(%s)->第1跳(%s)", inNode.NodeName, firstNode.NodeName)
					workItems = append(workItems, diagnosisWorkItem{
						fromNodeID:   inNode.NodeID,
						toNode:       firstNode,
						hasChainHop:  true,
						ipPreference: ipPreference,
						description:  description,
						protocol:     defaultString(strings.ToLower(strings.TrimSpace(firstNode.Protocol)), protocol),
						metadata: map[string]interface{}{
							"fromChainType": 1,
							"toChainType":   2,
							"toInx":         firstNode.Inx,
						},
					})
				}
			} else {
				for _, outNode := range outNodes {
					description := fmt.Sprintf("入口(%s)->出口(%s)", inNode.NodeName, outNode.NodeName)
					workItems = append(workItems, diagnosisWorkItem{
						fromNodeID:   inNode.NodeID,
						toNode:       outNode,
						hasChainHop:  true,
						ipPreference: ipPreference,
						description:  description,
						protocol:     defaultString(strings.ToLower(strings.TrimSpace(outNode.Protocol)), protocol),
						metadata: map[string]interface{}{
							"fromChainType": 1,
							"toChainType":   3,
						},
					})
				}
			}
		}

		for i, hop := range chainHops {
			for _, currentNode := range hop {
				if i+1 < len(chainHops) {
					for _, nextNode := range chainHops[i+1] {
						description := fmt.Sprintf("第%d跳(%s)->第%d跳(%s)", i+1, currentNode.NodeName, i+2, nextNode.NodeName)
						workItems = append(workItems, diagnosisWorkItem{
							fromNodeID:   currentNode.NodeID,
							toNode:       nextNode,
							hasChainHop:  true,
							ipPreference: ipPreference,
							description:  description,
							protocol:     defaultString(strings.ToLower(strings.TrimSpace(nextNode.Protocol)), protocol),
							metadata: map[string]interface{}{
								"fromChainType": 2,
								"fromInx":       currentNode.Inx,
								"toChainType":   2,
								"toInx":         nextNode.Inx,
							},
						})
					}
				} else {
					for _, outNode := range outNodes {
						description := fmt.Sprintf("第%d跳(%s)->出口(%s)", i+1, currentNode.NodeName, outNode.NodeName)
						workItems = append(workItems, diagnosisWorkItem{
							fromNodeID:   currentNode.NodeID,
							toNode:       outNode,
							hasChainHop:  true,
							ipPreference: ipPreference,
							description:  description,
							protocol:     defaultString(strings.ToLower(strings.TrimSpace(outNode.Protocol)), protocol),
							metadata: map[string]interface{}{
								"fromChainType": 2,
								"fromInx":       currentNode.Inx,
								"toChainType":   3,
							},
						})
					}
				}
			}
		}

		if probeTargetOK {
			for _, outNode := range outNodes {
				description := fmt.Sprintf("出口(%s)->外网", outNode.NodeName)
				workItems = append(workItems, diagnosisWorkItem{
					fromNodeID:  outNode.NodeID,
					targetIP:    probeTarget.Host,
					targetPort:  probeTarget.Port,
					description: description,
					protocol:    "tcp",
					metadata: map[string]interface{}{
						"fromChainType": 3,
					},
				})
			}
		}
	default:
		if probeTargetOK {
			for _, inNode := range inNodes {
				description := fmt.Sprintf("入口(%s)->外网", inNode.NodeName)
				workItems = append(workItems, diagnosisWorkItem{
					fromNodeID:  inNode.NodeID,
					targetIP:    probeTarget.Host,
					targetPort:  probeTarget.Port,
					description: description,
					protocol:    "tcp",
					metadata: map[string]interface{}{
						"fromChainType": 1,
					},
				})
			}
		}
	}

	tunnelType := map[bool]string{true: "端口转发", false: "隧道转发"}[tunnel.Type == 1]
	return tunnelName, tunnelType, workItems, nil
}

func splitChainNodeGroups(rows []chainNodeRecord) ([]chainNodeRecord, [][]chainNodeRecord, []chainNodeRecord) {
	inNodes := make([]chainNodeRecord, 0)
	outNodes := make([]chainNodeRecord, 0)
	chainByInx := map[int64][]chainNodeRecord{}
	hopOrder := make([]int64, 0)

	for _, row := range rows {
		switch row.ChainType {
		case 1:
			inNodes = append(inNodes, row)
		case 2:
			if _, ok := chainByInx[row.Inx]; !ok {
				hopOrder = append(hopOrder, row.Inx)
			}
			chainByInx[row.Inx] = append(chainByInx[row.Inx], row)
		case 3:
			outNodes = append(outNodes, row)
		}
	}

	sort.Slice(hopOrder, func(i, j int) bool { return hopOrder[i] < hopOrder[j] })
	chainHops := make([][]chainNodeRecord, 0, len(hopOrder))
	for _, inx := range hopOrder {
		chainHops = append(chainHops, chainByInx[inx])
	}

	return inNodes, chainHops, outNodes
}

func resolveDiagnosisTargets(remoteAddr string) ([]diagnosisTarget, error) {
	rawTargets := splitRemoteTargets(remoteAddr)
	if len(rawTargets) == 0 {
		return nil, errors.New("目标地址不能为空")
	}

	targets := make([]diagnosisTarget, 0, len(rawTargets))
	for _, raw := range rawTargets {
		ip, port, err := parseTargetAddress(raw)
		if err != nil {
			continue
		}
		targets = append(targets, diagnosisTarget{Address: raw, IP: ip, Port: port})
	}
	if len(targets) == 0 {
		return nil, errors.New("目标地址格式错误")
	}
	return targets, nil
}

func diagnosisContextMessage(ctx context.Context) string {
	if ctx == nil {
		return diagnosisRequestTimeoutMsg
	}
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return diagnosisRequestTimeoutMsg
	case context.Canceled:
		return "诊断已取消"
	default:
		return diagnosisRequestTimeoutMsg
	}
}

func diagnosisExecOptionsFromContext(ctx context.Context) diagnosisExecOptions {
	timeout := diagnosisCommandTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				remaining = 100 * time.Millisecond
			}
			if remaining < timeout {
				timeout = remaining
			}
		}
	}
	if timeout <= 0 {
		timeout = 100 * time.Millisecond
	}
	pingTimeoutMS := int(timeout / time.Millisecond)
	if pingTimeoutMS <= 0 {
		pingTimeoutMS = 100
	}
	return diagnosisExecOptions{
		commandTimeout: timeout,
		pingTimeoutMS:  pingTimeoutMS,
		timeoutMessage: diagnosisContextMessage(ctx),
	}
}

func newDiagnosisTimeoutItem(workItem diagnosisWorkItem, message string) map[string]interface{} {
	targetPort := workItem.targetPort
	if targetPort <= 0 {
		targetPort = workItem.toNode.Port
	}
	item := newDiagnosisResultItem(workItem.fromNodeID, workItem.targetIP, targetPort, workItem.description, workItem.metadata)
	item["success"] = false
	if strings.TrimSpace(message) == "" {
		message = diagnosisCommandTimeoutMsg
	}
	item["message"] = message
	return item
}

func (h *Handler) executeDiagnosisWorkItem(workItem diagnosisWorkItem, options diagnosisExecOptions) map[string]interface{} {
	single := make([]map[string]interface{}, 0, 1)
	nodeCache := map[int64]*nodeRecord{}
	if workItem.hasChainHop {
		h.appendChainHopDiagnosis(&single, nodeCache, workItem.fromNodeID, workItem.toNode, workItem.description, workItem.metadata, workItem.ipPreference, workItem.protocol, options)
	} else {
		h.appendPathDiagnosis(&single, nodeCache, workItem.fromNodeID, workItem.targetIP, workItem.targetPort, workItem.description, workItem.metadata, workItem.protocol, options)
	}

	if len(single) == 0 {
		return newDiagnosisTimeoutItem(workItem, "诊断任务未返回结果")
	}
	return single[0]
}

func (h *Handler) runDiagnosisWorkItems(ctx context.Context, workItems []diagnosisWorkItem, emitter diagnosisItemEmitter) []map[string]interface{} {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]map[string]interface{}, len(workItems))
	if len(workItems) == 0 {
		return results
	}

	workerLimit := diagnosisMaxConcurrency
	if workerLimit < 1 {
		workerLimit = 1
	}
	if workerLimit > len(workItems) {
		workerLimit = len(workItems)
	}

	type diagnosisWorkResult struct {
		index int
		item  map[string]interface{}
	}

	jobs := make(chan int)
	resultCh := make(chan diagnosisWorkResult, len(workItems))

	var wg sync.WaitGroup
	for i := 0; i < workerLimit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				select {
				case <-ctx.Done():
					resultCh <- diagnosisWorkResult{index: index, item: newDiagnosisTimeoutItem(workItems[index], diagnosisContextMessage(ctx))}
					continue
				default:
				}
				options := diagnosisExecOptionsFromContext(ctx)
				resultCh <- diagnosisWorkResult{index: index, item: h.executeDiagnosisWorkItem(workItems[index], options)}
			}
		}()
	}

enqueueLoop:
	for i := 0; i < len(workItems); i++ {
		select {
		case <-ctx.Done():
			message := diagnosisContextMessage(ctx)
			for j := i; j < len(workItems); j++ {
				resultCh <- diagnosisWorkResult{index: j, item: newDiagnosisTimeoutItem(workItems[j], message)}
			}
			break enqueueLoop
		case jobs <- i:
		}
	}
	close(jobs)
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	progress := diagnosisProgress{Total: len(workItems)}
	for result := range resultCh {
		results[result.index] = result.item
		if decorate := workItems[result.index].decorate; decorate != nil {
			decorate(result.item)
		}
		countDiagnosisResult(&progress, result.item)
		if emitter != nil {
			emitter(result.index, result.item, progress)
		}
	}

	for i := range results {
		if results[i] == nil {
			results[i] = newDiagnosisTimeoutItem(workItems[i], diagnosisCommandTimeoutMsg)
			if decorate := workItems[i].decorate; decorate != nil {
				decorate(results[i])
			}
		}
	}
	return results
}

func (h *Handler) cachedNode(nodeCache map[int64]*nodeRecord, nodeID int64) (*nodeRecord, error) {
	if node, ok := nodeCache[nodeID]; ok {
		return node, nil
	}
	node, err := h.getNodeRecord(nodeID)
	if err != nil {
		return nil, err
	}
	nodeCache[nodeID] = node
	return node, nil
}

func newDiagnosisResultItem(fromNodeID int64, targetIP string, targetPort int, description string, metadata map[string]interface{}) map[string]interface{} {
	item := map[string]interface{}{
		"nodeName":    fmt.Sprintf("node_%d", fromNodeID),
		"nodeId":      strconv.FormatInt(fromNodeID, 10),
		"targetIp":    targetIP,
		"targetPort":  targetPort,
		"description": description,
		"averageTime": 0,
		"packetLoss":  100,
	}
	for k, v := range metadata {
		item[k] = v
	}
	return item
}

func (h *Handler) appendFailedDiagnosis(results *[]map[string]interface{}, nodeCache map[int64]*nodeRecord, fromNodeID int64, targetIP string, targetPort int, description string, metadata map[string]interface{}, message string) {
	item := newDiagnosisResultItem(fromNodeID, targetIP, targetPort, description, metadata)
	if node, err := h.cachedNode(nodeCache, fromNodeID); err == nil {
		item["nodeName"] = node.Name
	}
	if strings.TrimSpace(message) == "" {
		message = "连接失败"
	}
	item["success"] = false
	item["message"] = message
	*results = append(*results, item)
}

func (h *Handler) appendPathDiagnosis(results *[]map[string]interface{}, nodeCache map[int64]*nodeRecord, fromNodeID int64, targetIP string, targetPort int, description string, metadata map[string]interface{}, protocol string, options diagnosisExecOptions) {
	item := newDiagnosisResultItem(fromNodeID, targetIP, targetPort, description, metadata)

	fromNode, err := h.cachedNode(nodeCache, fromNodeID)
	if err != nil {
		item["success"] = false
		item["message"] = err.Error()
		*results = append(*results, item)
		return
	}
	item["nodeName"] = fromNode.Name

	var (
		pingData map[string]interface{}
		pingErr  error
	)
	pingData, pingErr = h.pingViaNode(fromNodeID, targetIP, targetPort, protocol, options)
	if pingErr != nil {
		item["success"] = false
		item["message"] = pingErr.Error()
		*results = append(*results, item)
		return
	}

	// R6-L4-01：UDP 探测不可实测（agent 只 Dial 不发包、无通用回声
	// 协议），其 success/延迟/丢包都不是目标路径的测量值。本组装点
	// 是全部手动诊断呈现面（目标腿/链腿/nft 腿/联邦远端回传）
	// 的唯一汇入口，在此单点把已执行 UDP 探测的结果收口为「未验证」
	// 形态：agent 明确回 unverified 标记（新 agent）或回 success=true
	// （老 agent 的 dial 成功）都收口；agent 回真实失败（DNS 解析
	// 失败等探测前错误、success=false 且无标记）保持失败呈现——
	// 那是与协议无关的目标错误，不是冒充实测。命令级失败
	// （pingErr）同理保持失败（见上方分支）。R7-028 后目标腿与
	// nft 腿只生成 TCP 项，本收口实际服务链路段 UDP 传输腿与
	// 联邦远端回传的 UDP 结果。
	if isUDPBasedProtocol(protocol) && (asBool(pingData["unverified"], false) || asBool(pingData["success"], false)) {
		markDiagnosisItemUnverified(item, protocol)
		*results = append(*results, item)
		return
	}

	success := asBool(pingData["success"], false)
	item["success"] = success
	item["averageTime"] = asFloat(pingData["averageTime"], 0)
	item["packetLoss"] = asFloat(pingData["packetLoss"], 100)

	message := strings.TrimSpace(asString(pingData["message"]))
	if success {
		if message == "" {
			message = "连接成功"
		}
	} else {
		if message == "" {
			message = strings.TrimSpace(asString(pingData["errorMessage"]))
		}
		if message == "" {
			message = "连接失败"
		}
	}
	item["message"] = message
	*results = append(*results, item)
}

// diagnosisUDPUnverifiedMessage 是未验证项的规范文案（R6-L4-01）：
// agent 契约、后端组装与前端呈现三层的「未验证」形态以此为准——
// 未验证项 success=false 但不计成败、averageTime/packetLoss 为
// null（未测），前端据 unverified 标记呈中性「未验证」托盘。
const diagnosisUDPUnverifiedMessage = "UDP 无法实测连通性（无通用回声协议），本项未验证"

// markDiagnosisItemUnverified 把一个已执行 UDP 探测的结果项收口
// 为未验证形态（R6-L4-01 单点定义）：不得呈 success、不得携带
// 伪造数值（延迟/丢包置 null）。agent 线上报文的数值字段在此
// 形态下只是零值（agent 侧结构体字段无 omitempty、恒序列化），
// 由本处统一覆写为 null 后才出面板——零值不是测量值，任何直接
// 消费 agent 原始报文的新面都须先判 unverified。链路段项的
// metadata 原本不带 protocol，在此补上本腿协议以便呈现层区分
// （已带 protocol 的腿项不覆盖）。
func markDiagnosisItemUnverified(item map[string]interface{}, protocol string) {
	item["success"] = false
	item["unverified"] = true
	item["averageTime"] = nil
	item["packetLoss"] = nil
	item["message"] = diagnosisUDPUnverifiedMessage
	if _, ok := item["protocol"]; !ok {
		item["protocol"] = strings.ToLower(strings.TrimSpace(protocol))
	}
}

func (h *Handler) appendChainHopDiagnosis(results *[]map[string]interface{}, nodeCache map[int64]*nodeRecord, fromNodeID int64, toNode chainNodeRecord, description string, metadata map[string]interface{}, ipPreference string, protocol string, options diagnosisExecOptions) {
	fromNode, _ := h.cachedNode(nodeCache, fromNodeID)
	targetNode, err := h.cachedNode(nodeCache, toNode.NodeID)
	if err != nil {
		h.appendFailedDiagnosis(results, nodeCache, fromNodeID, "", 0, description, metadata, err.Error())
		return
	}
	// L4-07：链行未分配监听口（port≤0，仅存量异常行/联邦待预留行）
	// 时不发起探测。resolveChainProbeTarget 的回退链（范围首口→
	// 443）是 R3 收敛的探测面统一代表性口径（质量探测/延迟/best-
	// exit 共用、K7 钉一致性），但诊断呈现面把每条腿当作链路健康
	// 结论：打虚构口得到的结果（443 上无关服务应答/无关失败）不
	// 得冒充该腿结论，故诊断面在此守卫、诚实呈现为未探测失败项。
	if toNode.Port <= 0 {
		h.appendFailedDiagnosis(results, nodeCache, fromNodeID, stripShareHostPort(targetNode.ServerIP), 0, description, metadata, "链路段未分配监听端口，无法探测")
		return
	}
	targetIP, targetPort, err := resolveChainProbeTarget(fromNode, targetNode, toNode.Port, ipPreference, toNode.ConnectIP)
	if err != nil {
		// R28-FIX-F01（sweep）：展示面 targetIp 亦须为裸 host。
		h.appendFailedDiagnosis(results, nodeCache, fromNodeID, stripShareHostPort(targetNode.ServerIP), toNode.Port, description, metadata, err.Error())
		return
	}
	h.appendPathDiagnosis(results, nodeCache, fromNodeID, targetIP, targetPort, description, metadata, protocol, options)
}

func resolveChainProbeTarget(fromNode, targetNode *nodeRecord, preferredPort int, ipPreference string, connectIp string) (string, int, error) {
	if targetNode == nil {
		return "", 0, errors.New("目标节点不存在")
	}
	host, err := selectTunnelDialHost(fromNode, targetNode, ipPreference, connectIp)
	if err != nil {
		// R28-FIX-F01（sweep）：回退拨号 host 可源自含 :port 的
		// ServerIP；剥离后才可作拨号目标，否则 JoinHostPort 式
		// 拼接产出不可拨号地址。
		host = stripShareHostPort(targetNode.ServerIP)
	}
	if host == "" {
		return "", 0, errors.New("目标节点地址为空")
	}
	// 回退链（首选口→范围首口→443）是 R3 收敛的探测面统一代表性
	// 口径：resolveBestExitProbeTarget 已与本函数收敛为同一实现，
	// 质量探测/中转延迟/best-exit 共用，K7 钉两解析器一致——不得
	// 在此单边改动。诊断呈现面对「链行无真实监听口」的诚实性由
	// appendChainHopDiagnosis 的守卫单独保证（L4-07）。
	port := preferredPort
	if port <= 0 {
		port = firstPortFromRange(targetNode.PortRange)
	}
	if port <= 0 {
		port = 443
	}
	return host, port, nil
}

func firstPortFromRange(portRange string) int {
	portRange = strings.TrimSpace(portRange)
	if portRange == "" {
		return 0
	}
	first := strings.Split(portRange, ",")[0]
	first = strings.TrimSpace(first)
	if strings.Contains(first, "-") {
		parts := strings.SplitN(first, "-", 2)
		if len(parts) != 2 {
			return 0
		}
		p, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || p <= 0 || p > 65535 {
			return 0
		}
		return p
	}
	p, err := strconv.Atoi(first)
	if err != nil || p <= 0 || p > 65535 {
		return 0
	}
	return p
}

func (h *Handler) listChainNodesForTunnel(tunnelID int64) ([]chainNodeRecord, error) {
	return h.repo.ListChainNodesForTunnel(tunnelID)
}

func (h *Handler) tcpPingViaNode(nodeID int64, ip string, port int, options diagnosisExecOptions) (map[string]interface{}, error) {
	if options.commandTimeout <= 0 {
		options.commandTimeout = diagnosisCommandTimeout
	}
	if options.pingTimeoutMS <= 0 {
		options.pingTimeoutMS = int(diagnosisCommandTimeout / time.Millisecond)
	}
	pingCount := options.pingCount
	if pingCount <= 0 {
		pingCount = 4
	}
	res, err := h.sendTCPPing(nodeID, map[string]interface{}{
		"ip":      ip,
		"port":    port,
		"count":   pingCount,
		"timeout": options.pingTimeoutMS,
	}, options.commandTimeout)
	if err != nil {
		return nil, err
	}
	if res.Data == nil {
		return nil, errors.New("节点未返回诊断数据")
	}
	return res.Data, nil
}

func isUDPBasedProtocol(protocol string) bool {
	p := strings.ToLower(strings.TrimSpace(protocol))
	return p == "kcp" || p == "udp" || p == "quic"
}

// chainSegmentProtocol 是段协议的统一判定口径：段行自身协议优先、
// 为空回退隧道协议——与诊断 prepare 构造链腿工作项时的
// defaultString(seg.Protocol, tunnel.Protocol) 逐字一致。质量探测
// （tunnel_quality_prober）判定某段是否 UDP 协议段必须经此口径，
// 不得另行只看段行字段（存量段行 protocol 可为空）。
func chainSegmentProtocol(segmentProtocol, tunnelProtocol string) string {
	p := strings.ToLower(strings.TrimSpace(segmentProtocol))
	if p == "" {
		p = strings.ToLower(strings.TrimSpace(tunnelProtocol))
	}
	return p
}

// nodeListenAddrForProtocol 按协议取节点的监听地址：UDP 族
// （udp/kcp/quic）取 UDPListenAddr，其余取 TCPListenAddr。与
// buildForwardServiceConfigs 的分取口径一致（F-UDP-6：链服务与联邦
// 角色服务曾全协议绑 TCPListenAddr，两地址被配成不同值时绑错
// 地址不可达）。
func nodeListenAddrForProtocol(node *nodeRecord, protocol string) string {
	if node == nil {
		return ""
	}
	if isUDPBasedProtocol(protocol) {
		return node.UDPListenAddr
	}
	return node.TCPListenAddr
}

func (h *Handler) pingViaNode(nodeID int64, ip string, port int, protocol string, options diagnosisExecOptions) (map[string]interface{}, error) {
	if isUDPBasedProtocol(protocol) {
		return h.udpPingViaNode(nodeID, ip, port, options)
	}
	return h.tcpPingViaNode(nodeID, ip, port, options)
}

func (h *Handler) udpPingViaNode(nodeID int64, ip string, port int, options diagnosisExecOptions) (map[string]interface{}, error) {
	if options.commandTimeout <= 0 {
		options.commandTimeout = diagnosisCommandTimeout
	}
	if options.pingTimeoutMS <= 0 {
		options.pingTimeoutMS = int(diagnosisCommandTimeout / time.Millisecond)
	}
	res, err := udpPingSend(h, nodeID, map[string]interface{}{
		"ip":      ip,
		"port":    port,
		"count":   4,
		"timeout": options.pingTimeoutMS,
	}, options.commandTimeout)
	if err != nil {
		return nil, err
	}
	if res.Data == nil {
		return nil, errors.New("节点未返回诊断数据")
	}
	return res.Data, nil
}

// splitRemoteTargets 是转发目标的消费侧切分器（gost 渲染、realm
// 端点、转发诊断、中转延迟共用）。R8-06（H-F3-01）：此前只切逗号，
// 与校验侧 splitRemoteParts（逗号/换行/CR，forward.md 明文承诺）口
// 径分歧——换行分隔的多目标校验按 N 段过、渲染成 1 个含 \n 的不可
// 拨号节点静默全灭。现直接复用 splitRemoteParts 保证两侧恒同口径
// （切分、TrimSpace、丢空段全一致），每段再过 processServerAddress
// 做 IPv6 括号化等渲染归一。存量已落库的换行形态行无需迁移：新口
// 径下自动按多目标正确渲染（复活属预期）。
//
// R9-09（I-F3-01）在此之上加有效段过滤：归一后无有效目标的段
// （"#"、纯 scheme、纯格式符段，判据 remoteNormalizedTargetEffective
// 与写面谓词共用）在消费侧丢弃、不渲染空/不可拨节点——R9-09 前
// 经管理员面/开关面/恢复面落库的存量坏行（如 "1.2.3.4:80,#"）自
// 此只渲染其有效目标，全死行渲染零目标、四消费面（gost 零节点、
// realm 的 len(Remotes)==0 显式拒绝、诊断与中转延迟的 len==0 显
// 式收口）均有定义行为，不再静默生成空节点。切分口径本身不变：
// 有效段与 splitRemoteParts 逐段对应、逐段渲染归一同前。
func splitRemoteTargets(remoteAddr string) []string {
	parts := splitRemoteParts(remoteAddr)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		processed := processServerAddress(part)
		if !remoteNormalizedTargetEffective(processed) {
			continue
		}
		out = append(out, processed)
	}
	return out
}

func parseTargetAddress(addr string) (string, int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, errors.New("empty address")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		idx := strings.LastIndex(addr, ":")
		if idx <= 0 || idx >= len(addr)-1 {
			return "", 0, err
		}
		host = strings.TrimSpace(addr[:idx])
		portStr = strings.TrimSpace(addr[idx+1:])
		// PC-06: the fallback must not silently slice a double-port value
		// ("203.0.113.7:8080:9090" -> host "203.0.113.7:8080"). The sliced
		// host must be a bare IP or contain no colon (hostname); otherwise
		// the input is malformed. The bare-IPv6 fallback ("2001:db8::1:443"
		// -> host "2001:db8::1") is preserved because ParseIP succeeds.
		if net.ParseIP(host) == nil && strings.Contains(host, ":") {
			return "", 0, errors.New("invalid host")
		}
	}
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, errors.New("invalid port")
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", 0, errors.New("invalid host")
	}
	return host, port, nil
}

func buildForwardServiceBase(forwardID, userID, userTunnelID int64) string {
	return fmt.Sprintf("%d_%d_%d", forwardID, userID, userTunnelID)
}

func buildForwardServiceBaseWithResolvedUserTunnel(forwardID, userID, resolvedUserTunnelID int64) string {
	if resolvedUserTunnelID <= 0 {
		return buildForwardServiceBase(forwardID, userID, 0)
	}
	return buildForwardServiceBase(forwardID, userID, resolvedUserTunnelID)
}

func buildForwardServiceBaseCandidates(forwardID, userID, preferredUserTunnelID int64, userTunnelIDs []int64) []string {
	orderedIDs := make([]int64, 0, len(userTunnelIDs)+2)
	seen := make(map[int64]struct{}, len(userTunnelIDs)+2)

	appendID := func(id int64) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		orderedIDs = append(orderedIDs, id)
	}

	appendID(preferredUserTunnelID)
	for _, id := range userTunnelIDs {
		appendID(id)
	}
	appendID(0)

	bases := make([]string, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		bases = append(bases, buildForwardServiceBase(forwardID, userID, id))
	}
	return bases
}

func buildForwardControlServiceNames(base, commandType string) []string {
	names := []string{base + "_tcp", base + "_udp"}
	if strings.EqualFold(strings.TrimSpace(commandType), "DeleteService") {
		return append([]string{base}, names...)
	}
	return names
}

func shouldTryLegacySingleService(commandType string) bool {
	cmd := strings.ToLower(strings.TrimSpace(commandType))
	return cmd == "pauseservice" || cmd == "resumeservice"
}

func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "not found") || strings.Contains(msg, "不存在")
}

func isAlreadyExistsMessage(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	if isAddressAlreadyInUseMessage(msg) {
		return false
	}
	compact := compactErrorMessage(msg)
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "已存在") || strings.Contains(compact, "alreadyexists")
}

func isBindAddressInUseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	return isAddressAlreadyInUseMessage(msg) || strings.Contains(msg, "cannot assign requested address")
}

func isAddressAlreadyInUseError(err error) bool {
	if err == nil {
		return false
	}
	return isAddressAlreadyInUseMessage(strings.ToLower(strings.TrimSpace(err.Error())))
}

func isAddressAlreadyInUseMessage(msg string) bool {
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "address already in use") {
		return true
	}
	return strings.Contains(compactErrorMessage(msg), "addressalreadyinuse")
}

func isCannotAssignRequestedAddressError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "cannot assign requested address") {
		return true
	}
	return strings.Contains(compactErrorMessage(msg), "cannotassignrequestedaddress")
}

func compactErrorMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	return strings.Join(strings.Fields(strings.ToLower(msg)), "")
}

func buildForwardServiceConfigs(baseName string, forward *forwardRecord, tunnel *tunnelRecord, node *nodeRecord, port int, bindIP string, runtimeLimiters forwardRuntimeLimiters) []map[string]interface{} {
	protocols := []string{"tcp", "udp"}
	services := make([]map[string]interface{}, 0, 2)
	targets := splitRemoteTargets(forward.RemoteAddr)
	strategy := strings.TrimSpace(forward.Strategy)
	proxyProtocolReceive, proxyProtocolSend := normalizeForwardProxyProtocol(forward.ProxyProtocol, forward.ProxyProtocolReceive, forward.ProxyProtocolSend)
	if strategy == "" {
		strategy = "fifo"
	}

	for _, protocol := range protocols {
		listenerAddr := node.TCPListenAddr
		if protocol == "udp" {
			listenerAddr = node.UDPListenAddr
		}
		var serviceAddr string
		if bindIP != "" {
			trimmedBindIP := strings.TrimSpace(bindIP)
			if _, _, err := net.SplitHostPort(trimmedBindIP); err == nil {
				serviceAddr = processServerAddress(trimmedBindIP)
			} else {
				serviceAddr = processServerAddress(net.JoinHostPort(strings.Trim(trimmedBindIP, "[]"), strconv.Itoa(port)))
			}
		} else {
			// R32-FIX-F01：监听地址先剥离 :port 再与服务端口拼接
			// （bindIP 分支为 D-01「端口胜出」设计，不动）。须用
			// JoinHostPort 而非 "%s:%d" 直拼：裸 IPv6 监听地址拼
			// 端口后会被 processServerAddress 误读成另一个合法
			// IPv6 地址（见 realm.go 同注释）。
			serviceAddr = processServerAddress(net.JoinHostPort(strings.Trim(stripListenAddrPort(listenerAddr), "[]"), strconv.Itoa(port)))
		}
		service := map[string]interface{}{
			"name": fmt.Sprintf("%s_%s", baseName, protocol),
			"addr": serviceAddr,
			"handler": map[string]interface{}{
				"type": protocol,
			},
			"listener": map[string]interface{}{
				"type": protocol,
			},
			"forwarder": map[string]interface{}{
				"nodes": buildForwarderNodes(targets),
				"selector": map[string]interface{}{
					"strategy":    strategy,
					"maxFails":    1,
					"failTimeout": "600s",
				},
			},
		}
		if runtimeLimiters.ConnLimiter != "" {
			service["climiter"] = runtimeLimiters.ConnLimiter
		}
		if runtimeLimiters.TrafficLimiter != "" {
			service["limiter"] = runtimeLimiters.TrafficLimiter
		}
		if proxyProtocolReceive > 0 {
			serviceMetadata := ensureServiceMetadata(service)
			serviceMetadata["proxyProtocol"] = proxyProtocolReceive
		}
		if proxyProtocolSend > 0 {
			handlerConfig := service["handler"].(map[string]interface{})
			if handlerConfig["metadata"] == nil {
				handlerConfig["metadata"] = map[string]interface{}{}
			}
			handlerConfig["metadata"].(map[string]interface{})["proxyProtocol"] = proxyProtocolSend
		}
		if protocol == "udp" {
			listenerMetadata := map[string]interface{}{
				"keepAlive": true,
				"ttl":       "30s",
			}
			service["listener"].(map[string]interface{})["metadata"] = listenerMetadata
		}
		if tunnel != nil && tunnel.Type == 2 {
			service["handler"].(map[string]interface{})["chain"] = fmt.Sprintf("chains_%d", forward.TunnelID)
		}
		if tunnel != nil && tunnel.Type == 1 && strings.TrimSpace(node.InterfaceName) != "" {
			serviceMetadata := ensureServiceMetadata(service)
			serviceMetadata["interface"] = node.InterfaceName
		}
		services = append(services, service)
	}

	return services
}

func buildForwarderNodes(targets []string) []map[string]interface{} {
	nodes := make([]map[string]interface{}, 0, len(targets))
	for i, addr := range targets {
		nodes = append(nodes, map[string]interface{}{
			"name": fmt.Sprintf("node_%d", i+1),
			"addr": addr,
		})
	}
	return nodes
}

func ensureServiceMetadata(service map[string]interface{}) map[string]interface{} {
	if service["metadata"] == nil {
		service["metadata"] = map[string]interface{}{}
	}
	metadata, ok := service["metadata"].(map[string]interface{})
	if !ok {
		metadata = map[string]interface{}{}
		service["metadata"] = metadata
	}
	return metadata
}

func normalizeForwardProxyProtocol(legacy, receive, send int) (int, int) {
	if send == 0 && legacy > 0 {
		send = legacy
	}
	return receive, send
}

func processServerAddress(serverAddr string) string {
	serverAddr = normalizeServerAddressInput(serverAddr)
	if serverAddr == "" {
		return serverAddr
	}
	if strings.HasPrefix(serverAddr, "[") {
		return serverAddr
	}
	// If the input is a bare IPv6 host (no port), bracket it.
	// IPv6-with-port must be provided in bracket form: [::1]:443.
	if looksLikeIPv6(serverAddr) {
		if ip := net.ParseIP(serverAddr); ip != nil && ip.To4() == nil {
			// PC-03: "2001:db8::1:443" parses as a valid IPv6 literal, but
			// it arrives here as a bare-IPv6 host + Sprintf port
			// (mutations.go, control_plane.go,
			// realm.go). If the tail after the last ":" is a decimal
			// port and the prefix is itself a valid IP, treat the input as
			// host:port instead of bracketing the whole string (which would
			// fold the port into the address and drop the port component).
			if idx := strings.LastIndex(serverAddr, ":"); idx > 0 {
				if hostPart, portPart := serverAddr[:idx], serverAddr[idx+1:]; isDecimalPort(portPart) && net.ParseIP(hostPart) != nil {
					return "[" + hostPart + "]:" + portPart
				}
			}
			return "[" + serverAddr + "]"
		}
	}

	idx := strings.LastIndex(serverAddr, ":")
	if idx < 0 {
		if looksLikeIPv6(serverAddr) {
			return "[" + serverAddr + "]"
		}
		return serverAddr
	}
	host := strings.TrimSpace(serverAddr[:idx])
	port := strings.TrimSpace(serverAddr[idx+1:])
	if host == "" || port == "" {
		return serverAddr
	}
	if looksLikeIPv6(host) {
		return "[" + host + "]:" + port
	}
	return serverAddr
}

func normalizeServerAddressInput(serverAddr string) string {
	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		return serverAddr
	}

	if idx := strings.Index(serverAddr, "://"); idx > 0 {
		if parsed, err := url.Parse(serverAddr); err == nil {
			if host := strings.TrimSpace(parsed.Host); host != "" {
				return host
			}
		}
		serverAddr = serverAddr[idx+3:]
	}

	if idx := strings.IndexAny(serverAddr, "/?#"); idx >= 0 {
		serverAddr = serverAddr[:idx]
	}
	return strings.TrimSpace(serverAddr)
}

func looksLikeIPv6(address string) bool {
	return strings.Count(address, ":") >= 2
}

// isDecimalPort reports whether s is a decimal TCP/UDP port number (1-65535).
func isDecimalPort(s string) bool {
	if s == "" || len(s) > 5 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(s)
	return err == nil && port >= 1 && port <= 65535
}

func asBool(v interface{}, def bool) bool {
	s := strings.TrimSpace(strings.ToLower(asString(v)))
	if s == "" {
		return def
	}
	switch s {
	case "1", "t", "true", "yes", "y":
		return true
	case "0", "f", "false", "no", "n":
		return false
	default:
		return def
	}
}

func (h *Handler) ensureLimiterOnNode(nodeID int64, limiterID int64, speed int) error {
	if err := h.upsertLimiterOnNode(nodeID, limiterID, speed); err != nil {
		return fmt.Errorf("限速规则下发失败: %w", err)
	}

	return nil
}

func (h *Handler) ensureConnLimiterOnNode(nodeID int64, cfg forwardLimiterConfig) error {
	if cfg.Name == "" || len(cfg.Limits) == 0 {
		return nil
	}
	payload := map[string]interface{}{"name": cfg.Name, "limits": cfg.Limits}
	if _, err := h.sendNodeCommand(nodeID, "AddCLimiters", payload, false, false); err != nil {
		if !isAlreadyExistsMessage(err.Error()) {
			return fmt.Errorf("连接限制器下发失败: %w", err)
		}
		updatePayload := map[string]interface{}{"limiter": cfg.Name, "data": payload}
		if _, updateErr := h.sendNodeCommand(nodeID, "UpdateCLimiters", updatePayload, false, false); updateErr != nil {
			return fmt.Errorf("连接限制器更新失败: %w", updateErr)
		}
	}
	return nil
}

func buildConnLimiterConfigs(forward *forwardRecord, userMaxConn int) []forwardLimiterConfig {
	if forward == nil {
		return nil
	}
	if forward.MaxConn > 0 {
		limits := []string{fmt.Sprintf("$ %d", forward.MaxConn)}
		if forward.IPMaxConn > 0 {
			limits = append(limits, fmt.Sprintf("$$ %d", forward.IPMaxConn))
		}
		return []forwardLimiterConfig{{Name: forwardRuleConnLimiterName(forward), Limits: limits}}
	}
	configs := make([]forwardLimiterConfig, 0, 2)
	if userMaxConn > 0 {
		configs = append(configs, forwardLimiterConfig{Name: fmt.Sprintf("user_conn_limit_%d", forward.UserID), Limits: []string{fmt.Sprintf("$ %d", userMaxConn)}})
	}
	if forward.IPMaxConn > 0 {
		configs = append(configs, forwardLimiterConfig{Name: forwardRuleConnLimiterName(forward), Limits: []string{fmt.Sprintf("$$ %d", forward.IPMaxConn)}})
	}
	return configs
}

// R19-X-03：转发级连接限流器名。仅当转发带 rule 级连接限制
// （MaxConn>0 或 IPMaxConn>0）时存在，与 buildConnLimiterConfigs 发出
// rule_conn_limit_<id> 的条件一致；无限制时返回 ""，调用方跳过删除。
// user_conn_limit_<uid> 是同用户全部转发共享的，永不在此删除。
func forwardRuleConnLimiterName(forward *forwardRecord) string {
	if forward == nil || (forward.MaxConn <= 0 && forward.IPMaxConn <= 0) {
		return ""
	}
	return forwardRuleConnLimiterNameByID(forward.ID)
}

// forwardRuleConnLimiterNameByID 是 rule 级 climiter 的确定性名：只由
// 转发 ID 决定，与当前记录的限流值无关。R20-X-01 ②：删除兜底不得只按
// 当前记录值取名——更新清零限流后删除再也算不出旧名，孤儿永久残留；
// 按 ID 取名则更新-删除序列仍能命中旧名。agent 侧删不存在者为 noop，
// 从未带过限流的转发多发一次 DeleteCLimiters 亦无害。
func forwardRuleConnLimiterNameByID(forwardID int64) string {
	if forwardID <= 0 {
		return ""
	}
	return fmt.Sprintf("rule_conn_limit_%d", forwardID)
}

// deleteForwardConnLimiterOnNode 在转发服务已删后，尽力删除该转发在节点上
// 的 rule_conn_limit_<id> climiter。R19-X-03 根因：删转发路径只发
// DeleteService，从不发 DeleteCLimiters，climiter 在 gost.json 里永久残留
// （R20-F04 起 agent 启动自愈亦剪无引用 limiter，存量孤儿在 agent 下次
// 启动/重载时自愈；此处仍尽力删，不等重启）。失败只记日志、不向
// 调用方返回错误：服务已删、删库不应被一个 cosmetic 残留挡住；节点离线时
// 调用方本就走 S-04/重连清理分支、会经同一漏斗重试。agent 侧
// deleteConnLimiter 对不存在的名字亦返回成功，故重复发送无害。
// R20-X-01 ②：取名只按转发 ID，不再按当前记录的限流值（无限制返 "" 即
// 跳过的旧口径会在更新清零后漏删孤儿）；user_conn_limit_* 共享，永不删。
func (h *Handler) deleteForwardConnLimiterOnNode(forward *forwardRecord, nodeID int64) {
	if forward == nil {
		return
	}
	name := forwardRuleConnLimiterNameByID(forward.ID)
	if name == "" {
		return
	}
	if _, err := h.sendNodeCommand(nodeID, "DeleteCLimiters", map[string]interface{}{"limiter": name}, false, true); err != nil {
		log.Printf("forward delete: conn limiter cleanup failed node_id=%d forward_id=%d limiter=%s err=%v (continuing)", nodeID, forward.ID, name, err)
	}
}

// deleteStaleForwardConnLimiterOnNode 是 R20-X-01 ① 的更新路径清理：
// 旧配置带 rule 级限流、新配置已无时，失势的 rule_conn_limit_<id> 已
// 不再被任何服务引用，须删。调用方保证在服务推送成功后才调本函数
// （先推服务后删 limiter）；新配置仍带限流时不删——新 limiter 由
// ensureConnLimiterOnNode 原地 Add/Update，无需清理。user_conn_limit_*
// 为同用户共享，永不在此删除（新旧名均不含它）。
func (h *Handler) deleteStaleForwardConnLimiterOnNode(oldForward, newForward *forwardRecord, nodeID int64) {
	if oldForward == nil || newForward == nil {
		return
	}
	if forwardRuleConnLimiterName(oldForward) == "" {
		return
	}
	if forwardRuleConnLimiterName(newForward) != "" {
		return
	}
	h.deleteForwardConnLimiterOnNode(newForward, nodeID)
}

// A1-02：流量 limiter（DeleteLimiters）的删除编排。此前删转发只发
// DeleteService＋DeleteCLimiters，限速规则在节点上落的 LimiterConfig
// 条目无人删除，服务清零后仍残留在 agent gost.json（DeleteLimiters
// 全程零下发，真机收据总账实证）。与 deleteForwardConnLimiterOnNode
// 同纪律：服务已删后才调、失败只记日志不阻塞删库、离线节点由调用方
// S-04 分支拦截不到此处（重连后由配置上报对账 cleanNodeConfigs 按
// 全集差集兜底）。
//
// 删除对象两类：
//  1. rule_traffic_limit_<forwardID>：转发专属条目，名只由转发 ID
//     派生（与 forwardRuleConnLimiterNameByID 同口径，不依赖当前
//     记录的限速值——更新清零后删除仍须命中旧名）；agent 删不存
//     在者为 noop，无条件发送安全。
//  2. 总速规则条目（名＝规则 ID 字符串）：可被多条转发共享（直引
//     SpeedID 或经 user_tunnel 回退），仅当引用集正本
//     （SpeedLimitAffectedForwardIDs）内除本转发外再无存续引用时
//     才删；枚举失败按「仍有引用」处理，宁留残、不误删在用条目
//     （残留由删规则路径与配置上报对账兜底清理）。
func (h *Handler) deleteForwardTrafficLimitersOnNode(forward *forwardRecord, nodeID int64) {
	if forward == nil || nodeID <= 0 {
		return
	}
	names := []string{fmt.Sprintf("rule_traffic_limit_%d", forward.ID)}
	for _, ruleID := range h.forwardTotalLimiterRuleIDs(forward) {
		if h.speedLimitHasOtherReferrer(ruleID, forward.ID) {
			continue
		}
		names = append(names, strconv.FormatInt(ruleID, 10))
	}
	for _, name := range names {
		if _, err := h.sendNodeCommand(nodeID, "DeleteLimiters", map[string]interface{}{"limiter": name}, false, true); err != nil {
			log.Printf("forward delete: traffic limiter cleanup failed node_id=%d forward_id=%d limiter=%s err=%v (continuing)", nodeID, forward.ID, name, err)
		}
	}
}

// deleteStaleForwardTrafficLimitersOnNode 是 A1-02 同族的更新路径清理
// （FIX-RESIDUAL-2）：转发更新清零/改绑 SpeedID 时，更新路径此前从不
// 发 DeleteLimiters，节点上失势的流量 limiter 条目永久残留（删除编排
// A1-02 与配置上报对账之外无第三个清理者；CLimiter 有
// deleteStaleForwardConnLimiterOnNode 对位、流量 limiter 此前无）。
// 调用方保证在服务推送成功后才调（先推服务后删 limiter，与 R20-X-01
// ① 同序）；失败只记日志、不挡更新回执。
//
// 两类条目与 A1-02 同口径：
//  1. rule_traffic_limit_<forwardID>（转发专属，IP 限速落点）：旧记录
//     有、新记录已无时恒删——名只属本转发、无共享可能；新记录仍有
//     时同步刚以新值原地 Add/Update，不得删。
//  2. 总速规则条目（名＝规则 ID）：以「新旧生效总速规则不同」为闸
//     （forwardEffectiveTotalLimiterRuleID 折算，清零/改绑/回退变化
//     才触发），扫旧记录候选集（forwardTotalLimiterRuleIDs：直引＋
//     user_tunnel 回退，与 A1-02 枚举同口径）：仍是新记录生效规则的
//     跳过；引用集正本内除本转发外还有存续引用的跳过
//     （speedLimitHasOtherReferrer，枚举失败按仍有引用保留）。
//     任一侧生效规则解析出错则整类跳过——无法判定即不删。
func (h *Handler) deleteStaleForwardTrafficLimitersOnNode(oldForward, newForward *forwardRecord, nodeID int64) {
	if oldForward == nil || newForward == nil || nodeID <= 0 {
		return
	}
	send := func(name string) {
		if _, err := h.sendNodeCommand(nodeID, "DeleteLimiters", map[string]interface{}{"limiter": name}, false, true); err != nil {
			log.Printf("forward update: stale traffic limiter cleanup failed node_id=%d forward_id=%d limiter=%s err=%v (continuing)", nodeID, newForward.ID, name, err)
		}
	}
	if h.forwardHasIPTrafficLimiter(oldForward) && !h.forwardHasIPTrafficLimiter(newForward) {
		send(fmt.Sprintf("rule_traffic_limit_%d", newForward.ID))
	}
	oldEffective, oldErr := h.forwardEffectiveTotalLimiterRuleID(oldForward)
	newEffective, newErr := h.forwardEffectiveTotalLimiterRuleID(newForward)
	if oldErr != nil || newErr != nil {
		log.Printf("forward update: stale traffic limiter sweep skipped node_id=%d forward_id=%d old_err=%v new_err=%v (keeping limiters)", nodeID, newForward.ID, oldErr, newErr)
		return
	}
	if oldEffective == newEffective {
		return
	}
	for _, ruleID := range h.forwardTotalLimiterRuleIDs(oldForward) {
		if ruleID == newEffective {
			continue
		}
		if h.speedLimitHasOtherReferrer(ruleID, newForward.ID) {
			continue
		}
		send(strconv.FormatInt(ruleID, 10))
	}
}

// forwardEffectiveTotalLimiterRuleID 按同步侧口径折算转发实际生效的
// 总速 limiter 规则 ID：直引 SpeedID 且规则速度可读、>0 时优先，否则
// user_tunnel 回退（ResolveUserTunnelAndLimiter 的 JOIN 已保证回退
// 规则启用且速度 >0）；无生效总速返回 0。GetSpeedLimitSpeed 读取失败
// 按同步侧同口径视为无直引限速（非枚举失败）；user_tunnel 解析出错
// 返回 err，调用方须按「无法判定」保留条目（fail-safe 方向为不删）。
func (h *Handler) forwardEffectiveTotalLimiterRuleID(forward *forwardRecord) (int64, error) {
	if forward == nil {
		return 0, nil
	}
	if forward.SpeedID.Valid && forward.SpeedID.Int64 > 0 {
		if speedVal, err := h.repo.GetSpeedLimitSpeed(forward.SpeedID.Int64); err == nil && speedVal > 0 {
			return forward.SpeedID.Int64, nil
		}
	}
	_, utLimiterID, utSpeed, err := h.resolveUserTunnelAndLimiter(forward.UserID, forward.TunnelID)
	if err != nil {
		return 0, err
	}
	if utLimiterID != nil && utSpeed != nil && *utLimiterID > 0 {
		return *utLimiterID, nil
	}
	return 0, nil
}

// forwardHasIPTrafficLimiter 判断转发是否落 rule_traffic_limit_<id>
// 专属条目：与同步侧 ipSpeed 解析同口径（IPSpeedID 指向的规则速度
// 可读、>0 才算落；读取失败按同步侧同口径视为未落）。
func (h *Handler) forwardHasIPTrafficLimiter(forward *forwardRecord) bool {
	if forward == nil || !forward.IPSpeedID.Valid || forward.IPSpeedID.Int64 <= 0 {
		return false
	}
	speedVal, err := h.repo.GetSpeedLimitSpeed(forward.IPSpeedID.Int64)
	return err == nil && speedVal > 0
}

// forwardTotalLimiterRuleIDs 枚举转发总速 limiter 的候选规则 ID：
// 直引 forward.SpeedID 与 user_tunnel 回退规则，与同步侧
// syncForwardServicesWithWarnings 的解析口径一致（直引优先、回退
// 次之）。两候选都返回、由引用集守卫逐个裁决：转发实际未落某候选
// 条目时删除为 noop，无害。
func (h *Handler) forwardTotalLimiterRuleIDs(forward *forwardRecord) []int64 {
	if forward == nil {
		return nil
	}
	var ids []int64
	if forward.SpeedID.Valid && forward.SpeedID.Int64 > 0 {
		ids = append(ids, forward.SpeedID.Int64)
	}
	if _, utLimiterID, _, err := h.resolveUserTunnelAndLimiter(forward.UserID, forward.TunnelID); err == nil && utLimiterID != nil && *utLimiterID > 0 {
		dup := false
		for _, id := range ids {
			if id == *utLimiterID {
				dup = true
				break
			}
		}
		if !dup {
			ids = append(ids, *utLimiterID)
		}
	}
	return ids
}

// speedLimitHasOtherReferrer 判断除 excludeForwardID 外是否再无存续
// 转发引用该限速规则；枚举失败返回 true（保留条目，fail-closed
// 方向为不删）。
func (h *Handler) speedLimitHasOtherReferrer(ruleID, excludeForwardID int64) bool {
	ids, err := h.repo.SpeedLimitAffectedForwardIDs(ruleID)
	if err != nil {
		log.Printf("forward delete: enumerate speed limit referrers failed speed_limit_id=%d err=%v (keeping limiter)", ruleID, err)
		return true
	}
	for _, id := range ids {
		if id != excludeForwardID {
			return true
		}
	}
	return false
}

func joinLimiterNames(configs []forwardLimiterConfig) string {
	names := make([]string, 0, len(configs))
	for _, cfg := range configs {
		if cfg.Name != "" {
			names = append(names, cfg.Name)
		}
	}
	return strings.Join(names, ",")
}

func speedToLimitLine(key string, speed int) string {
	// F-019（A4-002）：换算必须与 agent 侧解析口径一致——agent 用
	// alecthomas/units.ParseBase2Bytes（1MB = 1024*1024 B）还原速率。
	// 旧实现 speed/8 后用 %.1f 量化到 0.1MiB（≈0.84Mbps）步进，
	// 1 Mbps 实际只剩约 0.84 Mbps（偏低约 16%）。现按目标 B/s
	// （Mbps×10^6/8）折算 MiB/s 并保留 3 位小数，还原误差 <0.2%。
	rate := float64(speed) * 1000000 / 8 / (1024 * 1024)
	return fmt.Sprintf("%s %.3fMB %.3fMB", key, rate, rate)
}

func buildTrafficLimiterPayload(name string, totalSpeed *int, ipSpeed *int) map[string]interface{} {
	limits := make([]string, 0, 3)
	if totalSpeed != nil && *totalSpeed > 0 {
		limits = append(limits, speedToLimitLine("$", *totalSpeed))
	}
	if ipSpeed != nil && *ipSpeed > 0 {
		limits = append(limits, speedToLimitLine("0.0.0.0/0", *ipSpeed), speedToLimitLine("::/0", *ipSpeed))
	}
	return map[string]interface{}{"name": name, "limits": limits}
}

func buildLimiterAddPayload(limiterID int64, speed int) (string, map[string]interface{}) {
	name := strconv.FormatInt(limiterID, 10)

	return name, map[string]interface{}{
		"name":   name,
		"limits": []string{speedToLimitLine("$", speed)},
	}
}

func buildLimiterUpdatePayload(name string, data map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"limiter": name,
		"data":    data,
	}
}

func (h *Handler) upsertLimiterOnNode(nodeID int64, limiterID int64, speed int) error {
	name, addPayload := buildLimiterAddPayload(limiterID, speed)
	if _, err := h.sendNodeCommand(nodeID, "AddLimiters", addPayload, false, false); err != nil {
		if !isAlreadyExistsMessage(err.Error()) {
			return err
		}
		payload := map[string]interface{}{
			"name":   name,
			"limits": addPayload["limits"],
		}
		if _, updateErr := h.sendNodeCommand(nodeID, "UpdateLimiters", buildLimiterUpdatePayload(name, payload), false, false); updateErr != nil {
			return updateErr
		}
	}

	return nil
}

func (h *Handler) ensureTrafficLimiterOnNode(nodeID int64, name string, totalSpeed *int, ipSpeed *int) error {
	payload := buildTrafficLimiterPayload(name, totalSpeed, ipSpeed)
	limits, _ := payload["limits"].([]string)
	if name == "" || len(limits) == 0 {
		return nil
	}
	if _, err := h.sendNodeCommand(nodeID, "AddLimiters", payload, false, false); err != nil {
		if !isAlreadyExistsMessage(err.Error()) {
			return fmt.Errorf("限速规则下发失败: %w", err)
		}
		if _, updateErr := h.sendNodeCommand(nodeID, "UpdateLimiters", buildLimiterUpdatePayload(name, payload), false, false); updateErr != nil {
			return fmt.Errorf("限速规则更新失败: %w", updateErr)
		}
	}
	return nil
}
