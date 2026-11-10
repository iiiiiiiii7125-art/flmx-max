package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

const bytesPerGB int64 = 1024 * 1024 * 1024
const bytesPerMiB int64 = 1024 * 1024

// saturatingFlowSum is the F2-04 quota-sum guard for this file:
// a forward/user-tunnel usage total must saturate at MaxInt64
// instead of wrapping negative, because a wrapped sum reads as
// "no usage" and the quota decision below would never fire (the
// restore path can legitimately produce near-MaxInt64 counters).
//
//go:noinline
func saturatingFlowSum(inFlow, outFlow int64) int64 {
	return repo.SaturatingAddInt64(inFlow, outFlow)
}

func flowLimitBytes(flowGB, flowMiB int64) int64 {
	if flowMiB > 0 {
		if flowMiB > math.MaxInt64/bytesPerMiB {
			return math.MaxInt64
		}
		return flowMiB * bytesPerMiB
	}
	if flowGB > math.MaxInt64/bytesPerGB {
		return math.MaxInt64
	}
	return flowGB * bytesPerGB
}

type userTunnelPolicy struct {
	ID       int64
	UserID   int64
	TunnelID int64
	Flow     int64
	FlowMiB  int64
	InFlow   int64
	OutFlow  int64
	ExpTime  int64
	Status   int
	Num      int
}

type gostConfigSnapshot struct {
	Services []namedConfigItem `json:"services"`
	Chains   []namedConfigItem `json:"chains"`
	Limiters []namedConfigItem `json:"limiters"`
}

type namedConfigItem struct {
	Name    string `json:"name"`
	Limiter string `json:"limiter,omitempty"`
	Handler *struct {
		Chain string `json:"chain"`
	} `json:"handler,omitempty"`
}

func parseFlowServiceIDs(serviceName string) (int64, int64, int64, bool) {
	parts := strings.Split(serviceName, "_")
	if len(parts) < 3 {
		return 0, 0, 0, false
	}

	forwardID, err1 := strconv.ParseInt(parts[0], 10, 64)
	userID, err2 := strconv.ParseInt(parts[1], 10, 64)
	userTunnelID, err3 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || forwardID <= 0 || userID <= 0 {
		return 0, 0, 0, false
	}

	return forwardID, userID, userTunnelID, true
}

func normalizeForwardRuntimeServiceName(serviceName string) string {
	name := strings.TrimSpace(serviceName)
	if strings.HasSuffix(name, "_tcp") {
		return strings.TrimSuffix(name, "_tcp")
	}
	if strings.HasSuffix(name, "_udp") {
		return strings.TrimSuffix(name, "_udp")
	}
	return name
}

// O2-002：已删除 scaleFlowByTunnel 死代码（原含 M-001 同类 bug：把 tunnel.Flow 当乘数用，
// Flow=2 时会双倍计费）。全仓确认无调用方；实际计费路径已在 1.0.7 由 M-001 修复
// （nftables_traffic.go / flow_upload_batch.go：Flow=1 仅计上传，Flow=2 计双向）。

func (h *Handler) enforceFlowPolicies(userID int64, userTunnelID int64) {
	now := time.Now().UnixMilli()

	if h.shouldPauseUser(userID, now) {
		h.pauseUserForwards(userID, now)
	}

	policy, err := h.getUserTunnelPolicy(userTunnelID)
	if err != nil || policy == nil {
		return
	}

	if shouldPauseUserTunnel(policy, now) {
		h.pauseUserTunnelForwards(policy.UserID, policy.TunnelID, now)
	}
}

func (h *Handler) ensureUserTunnelForwardAllowed(userID int64, tunnelID int64, now int64) error {
	return h.ensureUserTunnelForwardAllowedWithReserved(userID, tunnelID, now, 0, 0)
}

// ensureUserTunnelForwardAllowedWithReserved 是
// ensureUserTunnelForwardAllowed 的带预留计数变体（R3 B08-002）：
// reservedUser / reservedUserTunnel 表示同一批量操作中已判定通过、
// 但 DB 计数尚未反映的待激活条数（批量恢复的 nft 意图项 status 写入
// 推迟到分组 reconcile 之后），Num 上限判定必须把预留计入，否则同批
// 多条共用批前计数越限激活。即时写库的调用方传 0/0，口径不变。
func (h *Handler) ensureUserTunnelForwardAllowedWithReserved(userID int64, tunnelID int64, now int64, reservedUser int64, reservedUserTunnel int64) error {
	if h == nil || h.repo == nil {
		return errors.New("invalid flow policy context")
	}
	if userID <= 0 || tunnelID <= 0 {
		return nil
	}

	user, err := h.repo.GetUserByID(userID)
	if err != nil {
		return markRepoError(err)
	}
	if user == nil {
		return errors.New("用户不存在")
	}

	if user.Status != 1 {
		return errors.New("账号已禁用")
	}
	if user.ExpTime > 0 && user.ExpTime <= now {
		return errors.New("账号已过期")
	}

	flowLimit := flowLimitBytes(user.Flow, user.FlowMiB)
	current := saturatingFlowSum(user.InFlow, user.OutFlow)
	// O2-006：配额边界统一为 fail-closed：等于上限也拦截，与隧道级 utCurrent >= utFlowLimit 一致
	// 旧逻辑 flowLimit < current 允许"刚好用满"时开启新转发
	// R9X-001：flowLimit == 0 表示不限额（与 Num/ExpTime 的 >0 守卫同形），不得判超额
	if flowLimit > 0 && flowLimit <= current {
		return errors.New("流量已超额，禁止开启转发")
	}
	if err := h.ensureUserForwardAllowedByQuota(userID, now); err != nil {
		return err
	}

	if user.Num > 0 {
		currentForwardCount, err := h.repo.CountActiveForwardsByUser(userID)
		if err != nil {
			return markRepoError(err)
		}
		if currentForwardCount+reservedUser >= int64(user.Num) {
			return errors.New("转发数量已达上限")
		}
	}

	userTunnelID, _, _, err := h.resolveUserTunnelAndLimiter(userID, tunnelID)
	if err != nil {
		return err
	}
	if userTunnelID <= 0 {
		return nil
	}

	policy, err := h.getUserTunnelPolicy(userTunnelID)
	if err != nil {
		return err
	}
	if policy == nil {
		return nil
	}

	if policy.Status != 1 {
		return errors.New("该隧道已禁用")
	}
	if policy.ExpTime > 0 && policy.ExpTime <= now {
		return errors.New("该隧道已过期")
	}

	utFlowLimit := flowLimitBytes(policy.Flow, policy.FlowMiB)
	utCurrent := saturatingFlowSum(policy.InFlow, policy.OutFlow)
	// R9X-001：utFlowLimit == 0 表示不限额，不得判超额
	if utFlowLimit > 0 && utCurrent >= utFlowLimit {
		return errors.New("该隧道流量已超额，禁止开启转发")
	}

	if policy.Num > 0 {
		currentTunnelForwardCount, err := h.repo.CountActiveForwardsByUserTunnel(userID, tunnelID)
		if err != nil {
			return markRepoError(err)
		}
		if currentTunnelForwardCount+reservedUserTunnel >= int64(policy.Num) {
			return errors.New("该隧道转发数量已达上限")
		}
	}

	return nil
}

func (h *Handler) shouldPauseUser(userID int64, now int64) bool {
	user, err := h.repo.GetUserByID(userID)
	if err != nil || user == nil {
		return false
	}

	flowLimit := flowLimitBytes(user.Flow, user.FlowMiB)
	current := saturatingFlowSum(user.InFlow, user.OutFlow)
	// O2-006：与创建检查一致，等于上限也暂停（fail-closed）
	// R9X-001：flowLimit == 0 表示不限额，不得暂停
	if flowLimit > 0 && flowLimit <= current {
		return true
	}
	if user.ExpTime > 0 && user.ExpTime <= now {
		return true
	}
	return user.Status != 1
}

func shouldPauseUserTunnel(policy *userTunnelPolicy, now int64) bool {
	if policy == nil {
		return false
	}

	flowLimit := flowLimitBytes(policy.Flow, policy.FlowMiB)
	current := saturatingFlowSum(policy.InFlow, policy.OutFlow)
	// R9X-001：flowLimit == 0 表示不限额，不得暂停
	if flowLimit > 0 && current >= flowLimit {
		return true
	}
	if policy.ExpTime > 0 && policy.ExpTime <= now {
		return true
	}
	return policy.Status != 1
}

func (h *Handler) getUserTunnelPolicy(userTunnelID int64) (*userTunnelPolicy, error) {
	if userTunnelID <= 0 {
		return nil, nil
	}
	ut, err := h.repo.GetUserTunnelByID(userTunnelID)
	if err != nil {
		return nil, markRepoError(err)
	}
	if ut == nil {
		return nil, nil
	}
	return &userTunnelPolicy{
		ID: ut.ID, UserID: ut.UserID, TunnelID: ut.TunnelID,
		Flow: ut.Flow, FlowMiB: ut.FlowMiB, InFlow: ut.InFlow, OutFlow: ut.OutFlow,
		ExpTime: ut.ExpTime, Status: ut.Status, Num: ut.Num,
	}, nil
}

func (h *Handler) pauseUserForwards(userID int64, now int64) {
	forwards, err := h.listActiveForwardsByUser(userID)
	if err != nil {
		return
	}
	h.pauseForwardRecords(forwards, now)
}

func (h *Handler) pauseUserTunnelForwards(userID int64, tunnelID int64, now int64) {
	forwards, err := h.listActiveForwardsByUserTunnel(userID, tunnelID)
	if err != nil {
		return
	}
	h.pauseForwardRecords(forwards, now)
}

func (h *Handler) pauseForwardRecords(forwards []forwardRecord, now int64) {
	// R5-B3 D1-002: 配额超限的 nft 转发必须真正移除规则。原实现只调了 gost 的
	// controlForwardServices，nft 规则残留导致继续产生流量、继续扣配额。
	// R1: 按入口节点聚合，每节点只 reconcile 一次，每节点 30s 总超时，
	// 超时记 gap 并继续下一节点；不无限阻塞采集循环。
	nftGroups := make(map[int64][]int64)
	for i := range forwards {
		forward := &forwards[i]
		nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID)
		if modeErr == nil && nftMode && len(entryNodeIDs) > 0 {
			// R35-FIX-F01：遗留/竞态混合态下跳过、不写 paused（gost/
			// realm 服务不会被暂停）；行保持 active 待管理员先编辑
			// 隧道去掉 nft/多余入口，记日志不静默。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				log.Printf("quota pause skipped op=mixed_nft_state forward_id=%d err=%v", forward.ID, err)
				continue
			}
			nftGroups[entryNodeIDs[0]] = append(nftGroups[entryNodeIDs[0]], forward.ID)
			continue
		}
		if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
			log.Printf("quota pause failed op=dplane forward_id=%d err=%v", forward.ID, err)
			continue
		}
		if err := h.repo.UpdateForwardStatus(forward.ID, model.ForwardStatusPaused, now); err != nil {
			log.Printf("quota pause failed op=db_write forward_id=%d err=%v", forward.ID, err)
		}
	}
	for nodeID, ids := range nftGroups {
		exclude := make(map[int64]bool, len(ids))
		for _, id := range ids {
			exclude[id] = true
		}
		// D6: 先 reconcile（plan 不含这些转发），成功后再写 DB。
		if !h.reconcileNodeWithTimeout(nodeID, exclude, nil, 30*time.Second) {
			// M-005：告警级 gap：reconcile 超时/失败时 DB 仍显示 Active 而实际规则可能已部分移除，
			// DB 与实际状态不一致，下次计费周期会自愈重试，但 gap 期间流量可能漏计
			log.Printf("quota pause gap node_id=%d forward_ids=%v reason=reconcile_timeout_or_error ALERT=db_state_may_diverge", nodeID, ids)
			continue
		}
		for _, id := range ids {
			if err := h.repo.UpdateForwardStatus(id, model.ForwardStatusPaused, now); err != nil {
				log.Printf("quota pause failed op=db_write_after_reconcile forward_id=%d err=%v", id, err)
			}
		}
	}
}

// reconcileNodeWithTimeout 按节点执行一次带总超时的 reconcile（R1）。
// 成功返回 true；超时或失败返回 false（调用方记 gap 并继续）。
// F-135（X1-206）：超时必须是真取消。旧实现仅停止等待，底层
// goroutine 拿 context.Background() 继续执行并持有 per-node 锁，
// 同节点后续下发被无声排队（且锁持有者不知情）。现把带超时的
// ctx 透传到 drain 采集与 manager.Reconcile，取消随 SSH 操作
// 退出而生效、锁随之释放；单次 SSH 操作另有 runner 的 15s 超时
// 兜底，取消最迟在一个操作边界生效。
func (h *Handler) reconcileNodeWithTimeout(nodeID int64, exclude map[int64]bool, include []*forwardRecord, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- h.syncNftablesNodeWithOverridesCtx(ctx, nodeID, exclude, include)
	}()
	select {
	case err := <-done:
		return err == nil
	case <-ctx.Done():
		return false
	}
}

func (h *Handler) listActiveForwardsByUser(userID int64) ([]forwardRecord, error) {
	return h.repo.ListActiveForwardsByUser(userID)
}

func (h *Handler) listActiveForwardsByUserTunnel(userID int64, tunnelID int64) ([]forwardRecord, error) {
	return h.repo.ListActiveForwardsByUserTunnel(userID, tunnelID)
}

func (h *Handler) cleanNodeConfigs(nodeID int64, rawConfig string) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	if strings.TrimSpace(rawConfig) == "" {
		return
	}

	var snapshot gostConfigSnapshot
	if err := json.Unmarshal([]byte(rawConfig), &snapshot); err != nil {
		return
	}

	h.cleanOrphanedServices(nodeID, snapshot.Services)
	chainsInUse := make(map[string]struct{})
	limitersInUse := make(map[string]struct{})
	for _, service := range snapshot.Services {
		if service.Handler != nil {
			if chain := strings.TrimSpace(service.Handler.Chain); chain != "" {
				chainsInUse[chain] = struct{}{}
			}
		}
		for _, limiter := range strings.Split(service.Limiter, ",") {
			if limiter = strings.TrimSpace(limiter); limiter != "" {
				limitersInUse[limiter] = struct{}{}
			}
		}
	}
	// Keep dependencies referenced by the reported services, even when their
	// IDs belong to a different panel. Orphan dependencies can be collected on
	// the next report after their services have actually disappeared.
	h.cleanOrphanedChains(nodeID, snapshot.Chains, chainsInUse)
	h.cleanOrphanedLimiters(nodeID, snapshot.Limiters, limitersInUse)
}

func (h *Handler) cleanOrphanedServices(nodeID int64, services []namedConfigItem) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	// W8-R-F203：隧道仍在不等于本节点仍应承载它——链行变更后被
	// 移除节点上的同名服务此前永远不会被对账删除（旧谓词只认隧
	// 道整行消失），旧入口继续监听旧端口分流。以隧道当前运行时
	// 集判成员（与 deleteOrphanedForwardService 的 B4-03 同形）：
	// 节点已不在运行时集才删；任一查证失败按存疑保留。运行时集
	// 按隧道缓存（一次上报内同一隧道只查一次）。
	runtimeSets := make(map[int64]map[int64]struct{})
	nodeStillNeedsTunnelService := func(tunnelID int64) bool {
		set, ok := runtimeSets[tunnelID]
		if !ok {
			rows, err := h.listChainNodesForTunnel(tunnelID)
			if err != nil {
				log.Printf("cleanOrphanedServices: list tunnel runtime nodes failed node_id=%d tunnel_id=%d err=%v (keeping service)", nodeID, tunnelID, err)
				return true
			}
			set = make(map[int64]struct{})
			for _, row := range rows {
				if row.NodeID > 0 && tunnelRuntimeNeedsService(row) {
					set[row.NodeID] = struct{}{}
				}
			}
			runtimeSets[tunnelID] = set
		}
		_, needed := set[nodeID]
		return needed
	}
	// W8-R-F203：tunnelExists 吞掉仓储错误（失败返回 false），旧
	// 谓词在读失败时 fail-open 误删在役服务。改直读 TunnelExists：
	// 读失败只记日志、按隧道仍在处理（存疑保留）。
	tunnelGone := func(tunnelID int64) bool {
		exists, err := h.repo.TunnelExists(tunnelID)
		if err != nil {
			log.Printf("cleanOrphanedServices: check tunnel existence failed node_id=%d tunnel_id=%d err=%v (keeping service)", nodeID, tunnelID, err)
			return false
		}
		return !exists
	}
	// W8-R-F203：删除失败不再静默丢弃——点名进日志（节点/隧道/
	// 服务名），否则失势服务残留无从发现、无从重对账。
	deleteService := func(tunnelID int64, name string) {
		if _, err := h.sendNodeCommand(nodeID, "DeleteService", map[string]interface{}{"services": []string{name}}, false, true); err != nil {
			log.Printf("cleanOrphanedServices: delete orphaned service failed node_id=%d tunnel_id=%d service=%s err=%v", nodeID, tunnelID, name, err)
		}
	}
	tunnelServiceOrphaned := func(tunnelID int64) bool {
		if tunnelGone(tunnelID) {
			return true
		}
		return !nodeStillNeedsTunnelService(tunnelID)
	}
	for _, item := range services {
		name := strings.TrimSpace(item.Name)
		if name == "" || name == "web_api" {
			continue
		}
		// Legacy federation service names are not local services; the
		// orphan path never issues deletes for them (they disappear with
		// node config resync after the federation removal).
		if strings.HasPrefix(name, "fed_svc_") || strings.HasPrefix(name, "peer-share-") {
			continue
		}

		parts := strings.Split(name, "_")
		if len(parts) == 2 && parts[0] == "tunnel" {
			tunnelID, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil && tunnelID > 0 && tunnelServiceOrphaned(tunnelID) {
				deleteService(tunnelID, name)
			}
			continue
		}

		if _, _, _, ok := parseFlowServiceIDs(name); ok {
			h.deleteOrphanedForwardService(nodeID, name)
			continue
		}
		suffix := parts[len(parts)-1]

		switch suffix {
		case "tls", "kcp", "wss", "mtls", "mwss", "mtcp":
			tunnelID, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || tunnelID <= 0 || !tunnelServiceOrphaned(tunnelID) {
				continue
			}
			deleteService(tunnelID, name)
		case "tcp":
			if len(parts) < 4 {
				tunnelID, err := strconv.ParseInt(parts[0], 10, 64)
				if err == nil && tunnelID > 0 && tunnelServiceOrphaned(tunnelID) {
					deleteService(tunnelID, name)
				}
				continue
			}
		}
	}
}

func (h *Handler) cleanOrphanedChains(nodeID int64, chains []namedConfigItem, inUse map[string]struct{}) {
	for _, item := range chains {
		name := strings.TrimSpace(item.Name)
		if name == "" || strings.HasPrefix(name, "fed_chain_") || strings.HasPrefix(name, "peer-share-") {
			continue
		}
		if _, ok := inUse[name]; ok {
			continue
		}

		idx := strings.LastIndex(name, "_")
		if idx <= 0 || idx >= len(name)-1 {
			continue
		}
		tunnelID, err := strconv.ParseInt(name[idx+1:], 10, 64)
		if err != nil || tunnelID <= 0 {
			continue
		}
		exists, err := h.repo.TunnelExists(tunnelID)
		if err != nil || exists {
			continue
		}
		// W8-R-F203 同族：删除失败点名进日志，不再静默丢弃。
		if _, err := h.sendNodeCommand(nodeID, "DeleteChains", map[string]interface{}{"chain": name}, false, true); err != nil {
			log.Printf("cleanOrphanedChains: delete orphaned chain failed node_id=%d tunnel_id=%d chain=%s err=%v", nodeID, tunnelID, name, err)
		}
	}
}

func (h *Handler) cleanOrphanedLimiters(nodeID int64, limiters []namedConfigItem, inUse map[string]struct{}) {
	for _, item := range limiters {
		name := strings.TrimSpace(item.Name)
		if name == "" || strings.HasPrefix(name, "peer-share-") {
			continue
		}
		if _, ok := inUse[name]; ok {
			continue
		}
		exists, err := h.lookupSpeedLimiter(name)
		if err != nil || exists {
			continue
		}
		// W8-R-F203 同族：删除失败点名进日志，不再静默丢弃。
		if _, err := h.sendNodeCommand(nodeID, "DeleteLimiters", map[string]interface{}{"limiter": name}, false, true); err != nil {
			log.Printf("cleanOrphanedLimiters: delete orphaned limiter failed node_id=%d limiter=%s err=%v", nodeID, name, err)
		}
	}
}

func (h *Handler) sendDeleteOrphanedForwardService(nodeID int64, serviceName string) {
	h.sendDeleteOrphanedForwardServices(nodeID, []string{serviceName})
}

func (h *Handler) sendDeleteOrphanedForwardServices(nodeID int64, serviceNames []string) {
	if h == nil || h.repo == nil || nodeID <= 0 || len(serviceNames) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(serviceNames))
	for _, serviceName := range serviceNames {
		serviceName = normalizeForwardRuntimeServiceName(serviceName)
		if _, ok := seen[serviceName]; ok {
			continue
		}
		seen[serviceName] = struct{}{}
		h.deleteOrphanedForwardService(nodeID, serviceName)
	}
}

func (h *Handler) deleteOrphanedForwardService(nodeID int64, serviceName string) {
	forwardID, _, _, ok := parseFlowServiceIDs(serviceName)
	if !ok {
		return
	}
	parts := strings.Split(serviceName, "_")
	base := parts[0] + "_" + parts[1] + "_" + parts[2]
	// Batch metadata can be missing after a read failure, or stale by the time
	// cleanup runs. Confirm absence before issuing a destructive command.
	exists, err := h.repo.ForwardExists(forwardID)
	if err != nil {
		return
	}
	if exists {
		// B4-03 补扫：转发仍存在不等于本节点仍应承载它——隧道入
		// 口集变更后，被移除节点上的同名服务此前永远不会被对账
		// 删除（本函数旧形态只认转发整行消失），旧入口继续监听
		// 旧端口分流。以隧道当前入口集判成员：节点已不在入口集
		// 才删；任一查证失败（转发/入口集读不到）按存疑保留，
		// 不误删在役服务（与本函数 fail-closed 删除口径一致）。
		forward, ferr := h.getForwardRecord(forwardID)
		if ferr != nil || forward == nil {
			return
		}
		entryNodeIDs, eerr := h.tunnelEntryNodeIDs(forward.TunnelID)
		if eerr != nil || len(entryNodeIDs) == 0 {
			// 入口集读不到或为空（存量隧道无链行等）时无法证明
			// 服务过期，存疑保留。
			return
		}
		stillEntry := false
		for _, entryNodeID := range entryNodeIDs {
			if entryNodeID == nodeID {
				stillEntry = true
				break
			}
		}
		if stillEntry {
			return
		}
	}
	// W8-R-F203 同族：删除失败点名进日志，不再静默丢弃。
	if _, err := h.sendNodeCommand(nodeID, "DeleteService", map[string]interface{}{
		"services": buildForwardServiceDeleteNames([]string{base}),
	}, false, true); err != nil {
		log.Printf("deleteOrphanedForwardService: delete orphaned forward service failed node_id=%d forward_id=%d service=%s err=%v", nodeID, forwardID, serviceName, err)
	}
}

func (h *Handler) speedLimiterExists(name string) bool {
	exists, _ := h.lookupSpeedLimiter(name)
	return exists
}

func (h *Handler) lookupSpeedLimiter(name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}

	const forwardRulePrefix = "rule_traffic_limit_"
	if strings.HasPrefix(name, forwardRulePrefix) {
		forwardID, err := strconv.ParseInt(strings.TrimPrefix(name, forwardRulePrefix), 10, 64)
		if err != nil || forwardID <= 0 {
			return false, nil
		}
		forward, err := h.getForwardRecord(forwardID)
		if errors.Is(err, errForwardNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if forward == nil || !forward.IPSpeedID.Valid || forward.IPSpeedID.Int64 <= 0 {
			return false, nil
		}
		// S1-F01：与数字分支谓词同形——条目存续以「所绑限速规则
		// 存在且已启用」为准（SpeedLimitExists 只认 status=1）。
		// 旧实现只看转发行的 IPSpeedID 是否落值，规则禁用后条目
		// 仍被判存续、永不回收（两分支谓词不同形）。
		return h.repo.SpeedLimitExists(forward.IPSpeedID.Int64)
	}

	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil || id <= 0 {
		return false, nil
	}
	return h.repo.SpeedLimitExists(id)
}
