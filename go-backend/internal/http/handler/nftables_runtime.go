package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"

	"gorm.io/gorm"
)

type nftablesRuntimeManager interface {
	Test(ctx context.Context, cfg runtimenft.SSHConfig) error
	Reconcile(ctx context.Context, cfg runtimenft.SSHConfig, plan runtimenft.NodePlan) (runtimenft.ApplyResult, error)
	Clear(ctx context.Context, cfg runtimenft.SSHConfig) error
	CollectCounters(ctx context.Context, cfg runtimenft.SSHConfig) ([]runtimenft.CounterSample, error)
}

func isNftablesForwardMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), runtimenft.ModeNftables)
}

func (h *Handler) nodeUsesNftables(nodeID int64) (bool, error) {
	return h.nodeUsesNftablesTx(nil, nodeID)
}

func (h *Handler) nodeUsesNftablesTx(tx *gorm.DB, nodeID int64) (bool, error) {
	if h == nil || h.repo == nil {
		return false, errors.New("handler not initialized")
	}
	var (
		mode string
		err  error
	)
	if tx != nil {
		mode, err = h.repo.GetNodeForwardModeTx(tx, nodeID)
	} else {
		mode, err = h.repo.GetNodeForwardMode(nodeID)
	}
	if err != nil {
		return false, err
	}
	return isNftablesForwardMode(mode), nil
}

func (h *Handler) tunnelUsesNftables(tunnelID int64) (bool, []int64, error) {
	entryNodeIDs, err := h.tunnelEntryNodeIDs(tunnelID)
	if err != nil {
		return false, nil, err
	}
	for _, nodeID := range entryNodeIDs {
		ok, modeErr := h.nodeUsesNftables(nodeID)
		if modeErr != nil {
			return false, nil, modeErr
		}
		if ok {
			return true, entryNodeIDs, nil
		}
	}
	return false, entryNodeIDs, nil
}

func (h *Handler) validateNftablesForwardRequest(tunnel *tunnelRecord, remoteAddr string, entryNodeIDs []int64) error {
	if tunnel == nil {
		return errors.New("隧道不存在")
	}
	if tunnel.Type != 1 {
		return errors.New("nftables 节点仅支持直连隧道")
	}
	if len(entryNodeIDs) != 1 {
		return errors.New("nftables 节点仅支持单入口隧道")
	}
	target, err := runtimenft.ParseSingleTarget(remoteAddr)
	if err != nil {
		return err
	}
	if net.ParseIP(strings.Trim(strings.TrimSpace(target.Host), "[]")) == nil {
		return errors.New("nftables 节点仅支持 IP 目标地址")
	}
	return nil
}

// mixedNftForwardStateError 是 [0]-only 的 nft 分发点的 fail-closed
// 守卫（R35-F-01）：nft 分支只对「单入口、非 realm、直连隧道」的转发合
// 法——此时全部入口节点就是 entryNodeIDs[0]，取首元即全量。多入口含
// nft 节点、或 realm 隧道入口为 nft 节点时，[0] 分支会静默漏掉其余引
// 擎的服务（gost/realm 继续监听而 DB 称成功），此时拒绝操作而不是报
// 成功。validateNftablesForwardRequest 只在转发创建/编辑 intake 与
// sync 漏斗设防，本函数是其余分发点的同一口径后防线（单入口门不再是
// 唯一防线）；遗留混合态的修复路径是先编辑隧道去掉 nft/多余入口，
// 而不是在此处静默放行。
func mixedNftForwardStateError(tunnel *tunnelRecord, entryNodeIDs []int64) error {
	if tunnel == nil {
		return errors.New("隧道不存在")
	}
	if len(entryNodeIDs) != 1 {
		return errors.New("nftables 节点仅支持单入口隧道：该转发所在隧道存在多个入口且其中有 nftables 节点，请先编辑隧道去掉多余入口")
	}
	if tunnel.Type != 1 {
		return errors.New("nftables 节点仅支持直连隧道")
	}
	if tunnelIsRealm(tunnel) {
		return fmt.Errorf("隧道 %d 使用 realm 引擎，入口节点不能为 nftables 模式", tunnel.ID)
	}
	return nil
}

// rejectMixedNftForwardState 是 mixedNftForwardStateError 的调用点版：
// 非 nft 隧道直接放行（走 gost/realm 分支）；nft 隧道先取隧道记录再
// 按同一口径校验。调用方在 nftMode 分支内、触碰数据面前调用。
func (h *Handler) rejectMixedNftForwardState(tunnelID int64, nftMode bool, entryNodeIDs []int64) error {
	if !nftMode {
		return nil
	}
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil {
		return err
	}
	return mixedNftForwardStateError(tunnel, entryNodeIDs)
}

// entryNodeTunnelWithForwards 找节点作为入口节点、且名下已有转发的第一
// 条隧道（按隧道 ID 升序），供 nodeUpdate 翻转发模式前置拒绝时点名。
// 无此类隧道时返回 tunnelID 0。
func (h *Handler) entryNodeTunnelWithForwards(nodeID int64) (int64, string, error) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return 0, "", errors.New("handler not initialized")
	}
	tunnelIDs, err := h.repo.TunnelIDsByEntryNode(nodeID)
	if err != nil {
		return 0, "", err
	}
	for _, tunnelID := range tunnelIDs {
		forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
		if err != nil {
			return 0, "", err
		}
		if len(forwards) == 0 {
			continue
		}
		name, _ := h.repo.GetTunnelName(tunnelID)
		return tunnelID, name, nil
	}
	return 0, "", nil
}

func sshConfigFromModel(cfg *model.NodeSSHConfig) (runtimenft.SSHConfig, error) {
	if cfg == nil {
		return runtimenft.SSHConfig{}, errors.New("节点缺少 SSH 配置")
	}
	if strings.TrimSpace(cfg.Host) == "" || strings.TrimSpace(cfg.Username) == "" {
		return runtimenft.SSHConfig{}, errors.New("节点 SSH 配置不完整")
	}
	return runtimenft.SSHConfig{
		Host:       strings.TrimSpace(cfg.Host),
		Port:       cfg.Port,
		Username:   strings.TrimSpace(cfg.Username),
		AuthType:   strings.TrimSpace(cfg.AuthType),
		Password:   cfg.Password.String,
		PrivateKey: cfg.PrivateKey.String,
		Passphrase: cfg.Passphrase.String,
		SudoMode:   strings.TrimSpace(cfg.SudoMode),
	}, nil
}

// nftablesNodeSSHConfig 是入口节点的 nftables 模式判定 + SSH 配置
// 快照（纯 DB 数据，不含任何网络校验结果）。
type nftablesNodeSSHConfig struct {
	nodeID int64
	cfg    runtimenft.SSHConfig
}

// collectNftablesSSHConfigsTx 只做 DB 侧校验：入口节点是否为
// nftables 模式、SSH 配置是否存在且完整。A3-01：SSH 能力校验
// （manager.Test，单节点最长 15s 的网络调用）禁止在此执行——
// 调用方可能正持有 DB 事务与端口分配锁，SQLite 单连接下会连带
// 阻塞全站读写。Test 由 testNftablesNodeCapabilities 在事务/锁外
// 单独执行，口径与 port_alloc_lock「锁内禁止网络调用」一致。
func (h *Handler) collectNftablesSSHConfigsTx(tx *gorm.DB, entryNodeIDs []int64) ([]nftablesNodeSSHConfig, error) {
	if h == nil || h.repo == nil {
		return nil, errors.New("handler not initialized")
	}
	pairs := make([]nftablesNodeSSHConfig, 0, len(entryNodeIDs))
	for _, nodeID := range entryNodeIDs {
		isNft, err := h.nodeUsesNftablesTx(tx, nodeID)
		if err != nil {
			return nil, err
		}
		if !isNft {
			continue
		}
		var cfg *model.NodeSSHConfig
		if tx != nil {
			cfg, err = h.repo.GetNodeSSHConfigTx(tx, nodeID)
		} else {
			cfg, err = h.repo.GetNodeSSHConfig(nodeID)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("nftables 节点缺少 SSH 配置")
			}
			return nil, err
		}
		sshCfg, err := sshConfigFromModel(cfg)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, nftablesNodeSSHConfig{nodeID: nodeID, cfg: sshCfg})
	}
	return pairs, nil
}

// testNftablesNodeCapabilities 对已收集的 SSH 配置逐个执行节点能力
// 校验（SSH 网络调用）。只能在不持有 DB 事务与端口锁的阶段调用。
func (h *Handler) testNftablesNodeCapabilities(pairs []nftablesNodeSSHConfig) error {
	if len(pairs) == 0 {
		return nil
	}
	if h == nil || h.nftablesManager == nil {
		return errors.New("nftables manager not initialized")
	}
	for _, pair := range pairs {
		if err := h.nftablesManager.Test(context.Background(), pair.nodeID, pair.cfg); err != nil {
			return fmt.Errorf("nftables 节点能力校验失败: %w", err)
		}
	}
	return nil
}

// validateNftablesTunnelState 是完整校验（DB 校验 + SSH 能力校验），
// 只能在事务/锁外调用（测试与无事务入口）。
func (h *Handler) validateNftablesTunnelState(entryNodeIDs []int64) error {
	pairs, err := h.collectNftablesSSHConfigsTx(nil, entryNodeIDs)
	if err != nil {
		return err
	}
	return h.testNftablesNodeCapabilities(pairs)
}

// validateNftablesTunnelStateTx 是事务内可调用的 DB 侧校验（模式 +
// SSH 配置存在性/完整性），不做任何网络调用；SSH 能力校验由调用方
// 在事务外用 collectNftablesSSHConfigsTx 的快照 +
// testNftablesNodeCapabilities 补做（A3-01）。
func (h *Handler) validateNftablesTunnelStateTx(tx *gorm.DB, entryNodeIDs []int64) error {
	_, err := h.collectNftablesSSHConfigsTx(tx, entryNodeIDs)
	return err
}

func (h *Handler) buildNftablesNodePlan(nodeID int64) (runtimenft.NodePlan, *model.NodeSSHConfig, error) {
	return h.buildNftablesNodePlanWithOverrides(nodeID, nil, nil)
}

// buildNftablesNodePlanWithOverrides builds the node plan with explicit
// intent overrides for D6 pause/resume ordering (R5-B3 D1-012):
//   - exclude: forward IDs to drop from the plan even if active (pause intent);
//     the data plane is reconciled BEFORE the DB status flips.
//   - include: specific forwards to add even if not active (resume intent).
func (h *Handler) buildNftablesNodePlanWithOverrides(nodeID int64, exclude map[int64]bool, include []*forwardRecord) (runtimenft.NodePlan, *model.NodeSSHConfig, error) {
	cfg, err := h.repo.GetNodeSSHConfig(nodeID)
	if err != nil {
		// R10X-007（F-001）：与同文件 collectNftablesSSHConfigsTx /
		// loadNftablesSSHConfig / clearNftablesNode 的正本措辞对齐——
		// 缺行时点名「nftables 节点缺少 SSH 配置」，禁把仓储层
		// sql.ErrNoRows 字面量透传到同步回执（forwardCreate 的同步
		// 失败回执即经此路径）。
		if errors.Is(err, sql.ErrNoRows) {
			return runtimenft.NodePlan{}, nil, errors.New("nftables 节点缺少 SSH 配置")
		}
		return runtimenft.NodePlan{}, nil, err
	}
	forwards, err := h.repo.ListActiveForwardsByNode(nodeID)
	if err != nil {
		return runtimenft.NodePlan{}, nil, err
	}
	if len(exclude) > 0 {
		kept := forwards[:0]
		for _, f := range forwards {
			if !exclude[f.ID] {
				kept = append(kept, f)
			}
		}
		forwards = kept
	}
	if include != nil {
		seen := make(map[int64]bool, len(forwards))
		for _, f := range forwards {
			seen[f.ID] = true
		}
		for _, inc := range include {
			if inc == nil || seen[inc.ID] {
				continue
			}
			seen[inc.ID] = true
			forwards = append(forwards, *inc)
		}
	}
	plan := runtimenft.NodePlan{NodeID: nodeID, Rules: make([]runtimenft.Rule, 0, len(forwards))}
	for i := range forwards {
		forward := &forwards[i]
		tunnel, err := h.getTunnelRecord(forward.TunnelID)
		if err != nil || tunnel == nil || tunnel.Status != 1 {
			continue
		}
		entryNodeIDs, err := h.tunnelEntryNodeIDs(forward.TunnelID)
		if err != nil {
			return runtimenft.NodePlan{}, nil, err
		}
		if len(entryNodeIDs) != 1 || entryNodeIDs[0] != nodeID {
			continue
		}
		if err := h.validateNftablesForwardRequest(tunnel, forward.RemoteAddr, entryNodeIDs); err != nil {
			return runtimenft.NodePlan{}, nil, err
		}
		ports, err := h.listForwardPorts(forward.ID)
		if err != nil {
			return runtimenft.NodePlan{}, nil, err
		}
		for _, fp := range ports {
			if fp.NodeID != nodeID {
				continue
			}
			target, err := runtimenft.ParseSingleTarget(forward.RemoteAddr)
			if err != nil {
				return runtimenft.NodePlan{}, nil, err
			}
			plan.Rules = append(plan.Rules, runtimenft.Rule{
				ForwardID:  forward.ID,
				InPort:     fp.Port,
				BindIP:     strings.TrimSpace(fp.InIP),
				TargetHost: target.Host,
				TargetPort: target.Port,
				Protocols:  []string{"tcp", "udp"},
			})
		}
	}
	return plan, cfg, nil
}

func (h *Handler) syncNftablesNode(nodeID int64) error {
	return h.syncNftablesNodeWithOverrides(nodeID, nil, nil)
}

// syncNftablesNodeWithOverrides reconciles the node with explicit plan
// overrides (see buildNftablesNodePlanWithOverrides). Used by D6 pause/resume
// to converge the data plane BEFORE the DB status flips.
//
// R5-B3 §3/§4 顺序不变量（所有 reconcile 入口必经此处，新路径必须经此函数）：
//  1. status=1 的转发，其规则应存在，或处于可观测的待 reconcile 状态。
//  2. 不允许“DB 已删、规则仍在”且无 pending 记录（删库前必须数据面成功）。
//  3. 任何触碰节点 nft 状态的操作都必须经 per-node single-flight
//     （R5-B5 D14：下沉到 serializedNftManager 层，Reconcile/Clear/
//     CollectCounters/Test 全覆盖；锁粒度为 node_id，不用全局锁）。
//  4. 数据面失败时 DB 不前进（delete/pause/resume 一致；DB 写失败记日志，
//     靠下一次 reconcile 收敛，方向为“可用”而非“静默计费”）。
//
// R3: 锁内不持有 DB 事务；SSH 超时由 runner 的 15s 单操作超时兜底。
func (h *Handler) syncNftablesNodeWithOverrides(nodeID int64, exclude map[int64]bool, include []*forwardRecord) error {
	return h.syncNftablesNodeWithOverridesCtx(context.Background(), nodeID, exclude, include)
}

// syncNftablesNodeWithOverridesCtx 与 syncNftablesNodeWithOverrides
// 相同，但把 ctx 透传给 drain 采集与 manager.Reconcile：带超时的
// 调用方（reconcileNodeWithTimeout，F-135）取消时，底层 SSH 操作
// 随之退出、per-node 锁释放，而不是继续持锁执行到完成。
func (h *Handler) syncNftablesNodeWithOverridesCtx(ctx context.Context, nodeID int64, exclude map[int64]bool, include []*forwardRecord) error {
	if h == nil || h.repo == nil {
		return errors.New("handler not initialized")
	}
	// R5-B5 D14: per-node 串行化已下沉到 serializedNftManager。
	// F-134（X1-205）注释订正：旧注释称「drain 经 CollectCounters
	// 加锁」与实现不符。真实口径——Reconcile/Clear/Test 经 per-node
	// 锁互斥；CollectCounters 按 D18 设计不取锁，采集靠
	// generation/applying 协议丢弃脏读；drain（D1-004）在本函数内、
	// Reconcile 取锁之前执行，其采集同为不取锁路径，靠累计计数器
	// 语义兜底（重建后负 delta 走 reset 分支只推进基线、不重复计费）。

	plan, cfgModel, err := h.buildNftablesNodePlanWithOverrides(nodeID, exclude, include)
	if err != nil {
		return err
	}
	sshCfg, err := sshConfigFromModel(cfgModel)
	if err != nil {
		return err
	}
	// R5-B3 D1-004/D8: reconcile（delete table + nft -f）会清零全部计数器。
	// 先 best-effort drain：CollectCounters → delta 落库，再重建。
	// drain 失败不阻塞 apply（记 gap），重建后重新基线。
	// 取舍（写死）：可用性优先于计费精确性，但每次 gap 必须可观测。
	h.drainNftCountersBeforeReconcileCtx(ctx, nodeID, cfgModel, time.Now())
	result, err := h.nftablesManager.Reconcile(ctx, nodeID, sshCfg, plan)
	now := time.Now().UnixMilli()
	if err != nil {
		// D9(b): 记录 apply 失败，节点恢复后由采集循环触发自动 reconcile。
		h.markNftApplyFailed(nodeID, true)
		bindings, _ := h.repo.ListNftRuleBindingsByNode(nodeID)
		for _, binding := range bindings {
			_ = h.repo.MarkNftRuleBindingError(binding.ForwardID, nodeID, err.Error(), now)
		}
		return err
	}
	h.markNftApplyFailed(nodeID, false)
	activeForwardIDs := make(map[int64]struct{}, len(plan.Rules))
	for _, rule := range plan.Rules {
		activeForwardIDs[rule.ForwardID] = struct{}{}
		hash := result.Hashes[rule.ForwardID]
		_ = h.repo.UpsertNftRuleBinding(modelToRuleBindingInput(nodeID, rule, hash), now)
	}
	bindings, _ := h.repo.ListNftRuleBindingsByNode(nodeID)
	for _, binding := range bindings {
		if _, ok := activeForwardIDs[binding.ForwardID]; ok {
			continue
		}
		_ = h.repo.DeleteNftRuleBindingsByForward(binding.ForwardID)
	}
	return nil
}

func modelToRuleBindingInput(nodeID int64, rule runtimenft.Rule, hash string) repo.NftRuleBindingInput {
	return repo.NftRuleBindingInput{
		ForwardID:  rule.ForwardID,
		NodeID:     nodeID,
		InPort:     rule.InPort,
		Protocols:  strings.Join(rule.Protocols, ","),
		TargetAddr: fmt.Sprintf("%s:%d", rule.TargetHost, rule.TargetPort),
		BindIP:     rule.BindIP,
		RuleHash:   hash,
		Status:     runtimenft.StatusApplied,
	}
}

func (h *Handler) nftablesNodeIDFromRequest(r *http.Request, w http.ResponseWriter) (int64, bool) {
	nodeID := asInt64FromBodyKey(r, w, "nodeId")
	if nodeID <= 0 {
		return 0, false
	}
	return nodeID, true
}

func (h *Handler) loadNftablesSSHConfig(nodeID int64) (runtimenft.SSHConfig, error) {
	cfg, err := h.repo.GetNodeSSHConfig(nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return runtimenft.SSHConfig{}, errors.New("nftables 节点缺少 SSH 配置")
		}
		return runtimenft.SSHConfig{}, err
	}
	return sshConfigFromModel(cfg)
}

func (h *Handler) clearNftablesNode(nodeID int64) error {
	if h == nil || h.repo == nil {
		return errors.New("handler not initialized")
	}
	cfgModel, err := h.repo.GetNodeSSHConfig(nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("nftables 节点缺少 SSH 配置")
		}
		return err
	}
	sshCfg, err := sshConfigFromModel(cfgModel)
	if err != nil {
		return err
	}
	if h.nftablesManager == nil {
		return errors.New("nftables manager not initialized")
	}
	// F-020：与 sync/reconcile 路径一致，清表前先 best-effort drain
	// 把最近一个采集周期内的增量落库并推进基线；drain 失败已在其
	// 内部记 M-005 gap 告警，不阻塞 Clear（可用性优先口径与 sync
	// 路径相同）。旧实现直接 Clear，最近一周期流量随清表静默丢失。
	h.drainNftCountersBeforeReconcile(nodeID, cfgModel, time.Now())
	if err := h.nftablesManager.Clear(context.Background(), nodeID, sshCfg); err != nil {
		return err
	}
	bindings, listErr := h.repo.ListNftRuleBindingsByNode(nodeID)
	if listErr != nil {
		return listErr
	}
	for _, binding := range bindings {
		if err := h.repo.DeleteNftRuleBindingsByForward(binding.ForwardID); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) reconcileNftablesNodeByRequest(nodeID int64) error {
	usesNft, err := h.nodeUsesNftables(nodeID)
	if err != nil {
		return err
	}
	if !usesNft {
		return errors.New("节点未启用 nftables 转发模式")
	}
	return h.syncNftablesNode(nodeID)
}

func (h *Handler) nodeNftablesTest(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := h.nftablesNodeIDFromRequest(r, w)
	if !ok {
		return
	}
	usesNft, err := h.nodeUsesNftables(nodeID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if !usesNft {
		response.WriteJSON(w, response.ErrDefault("节点未启用 nftables 转发模式"))
		return
	}
	sshCfg, err := h.loadNftablesSSHConfig(nodeID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if h.nftablesManager == nil {
		response.WriteJSON(w, response.Err(-2, "nftables manager not initialized"))
		return
	}
	if err := h.nftablesManager.Test(context.Background(), nodeID, sshCfg); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) nodeNftablesReconcile(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := h.nftablesNodeIDFromRequest(r, w)
	if !ok {
		return
	}
	if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// nftPendingDeleteList 是补充 E 的管理员可观测入口：列出所有待清理转发
//（含节点、重试次数、下次重试时间、最后失败原因）。
func (h *Handler) nftPendingDeleteList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	_, roleID, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	if roleID != 0 {
		response.WriteJSON(w, response.Err(403, "无权限"))
		return
	}
	rows, err := h.repo.ListAllNftPendingDeletes()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	items := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		forwardName := ""
		if fr, frErr := h.repo.GetForwardRecord(row.ForwardID); frErr == nil && fr != nil {
			forwardName = fr.Name
		}
		nodeName := ""
		if n, nErr := h.getNodeRecord(row.NodeID); nErr == nil && n != nil {
			nodeName = n.Name
		}
		items = append(items, map[string]interface{}{
			"forwardId":   row.ForwardID,
			"forwardName": forwardName,
			"nodeId":      row.NodeID,
			"nodeName":    nodeName,
			"retryCount":  row.RetryCount,
			"nextRetryAt": row.NextRetryAt,
			"lastError":   row.LastError,
			"createdAt":   row.CreatedAt,
		})
	}
	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) nodeNftablesClear(w http.ResponseWriter, r *http.Request) {
	nodeID, ok := h.nftablesNodeIDFromRequest(r, w)
	if !ok {
		return
	}
	usesNft, err := h.nodeUsesNftables(nodeID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if !usesNft {
		response.WriteJSON(w, response.ErrDefault("节点未启用 nftables 转发模式"))
		return
	}
	if err := h.clearNftablesNode(nodeID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
