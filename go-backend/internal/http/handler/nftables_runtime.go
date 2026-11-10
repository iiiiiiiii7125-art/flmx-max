package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
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
	SetBlocked(ctx context.Context, cfg runtimenft.SSHConfig, port int, blocked bool) error
	ListBlockedPorts(ctx context.Context, cfg runtimenft.SSHConfig) ([]int, error)
}

func isNftablesForwardMode(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), runtimenft.ModeNftables)
}

// errNftablesNodeNotFound 是 nodeUsesNftables(Tx) 对「节点不存在」的
// 转译哨兵（B04-004）：repo 层把未找到归一为 sql.ErrNoRows，旧实现经
// 各 handler 以 Err(-2, err.Error()) 原样回出，泄漏 SQL 原文
// （"sql: no rows in result set"）。前置查询统一在此转译，上层按
// errors.Is 命中后回人话「节点不存在」；其余传播路径 err.Error()
// 亦为该人话文案。
var errNftablesNodeNotFound = errors.New("节点不存在")

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
		if errors.Is(err, sql.ErrNoRows) {
			return false, errNftablesNodeNotFound
		}
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return false, markRepoError(err)
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
// mixedNftForwardStateRejection 是 mixedNftForwardStateError 的
// 定型拒绝（R3-GEN-04）：四支文案均为面向用户的行动指引（错在哪、
// 下一步先编辑隧道），不含仓储原文。定型后删除面披露漏斗
// （respondForwardDeleteDisclosureErrorWithNote）得以 errors.As
// 识别并原样回执，与其余回执面（forward 系守卫点原文回）口径
// 一致；此前为无型 plain error、流经漏斗时被全量泛化。
type mixedNftForwardStateRejection struct {
	msg string
}

func (e *mixedNftForwardStateRejection) Error() string { return e.msg }

func mixedNftForwardStateError(tunnel *tunnelRecord, entryNodeIDs []int64) error {
	if tunnel == nil {
		return &mixedNftForwardStateRejection{msg: "隧道不存在"}
	}
	if len(entryNodeIDs) != 1 {
		return &mixedNftForwardStateRejection{msg: "nftables 节点仅支持单入口隧道：该转发所在隧道存在多个入口且其中有 nftables 节点，请先编辑隧道去掉多余入口"}
	}
	if tunnel.Type != 1 {
		return &mixedNftForwardStateRejection{msg: "nftables 节点仅支持直连隧道"}
	}
	if tunnelIsRealm(tunnel) {
		return &mixedNftForwardStateRejection{msg: fmt.Sprintf("隧道 %d 使用 realm 引擎，入口节点不能为 nftables 模式", tunnel.ID)}
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
// entryTunnelRef 是 entryNodeTunnelWithForwards 的返回值：入口隧道的 ID
// 与名称。字段是纯数据（不含错误文本），调用方可以将其拼进回执文案。
type entryTunnelRef struct {
	ID   int64
	Name string
}

func (h *Handler) entryNodeTunnelWithForwards(nodeID int64) (entryTunnelRef, error) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return entryTunnelRef{}, errors.New("handler not initialized")
	}
	tunnelIDs, err := h.repo.TunnelIDsByEntryNode(nodeID)
	if err != nil {
		return entryTunnelRef{}, err
	}
	for _, tunnelID := range tunnelIDs {
		forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
		if err != nil {
			return entryTunnelRef{}, err
		}
		if len(forwards) == 0 {
			continue
		}
		name, _ := h.repo.GetTunnelName(tunnelID)
		return entryTunnelRef{ID: tunnelID, Name: name}, nil
	}
	return entryTunnelRef{}, nil
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
		// C3-02：已钉入指纹随配置进入 runner，未钉入为空（runner
		// fail-closed），钉入动作只发生在 nodeNftablesTest。
		HostKeyFingerprint: strings.TrimSpace(cfg.HostKeyFingerprint),
	}, nil
}

// nftSSHConfigError 是 collectNftablesSSHConfigsTx 的定型校验失
// 败（R3-GEN-03）：SSH 配置缺失/不完整，文案由配置态唯一确定、
// 不含仓储原文。调用方以 errors.As 分流后以 msg 字段原样回执；
// 仓储错在 helper 内已标记、由调用方守卫泛化。「handler not
// initialized」是内部故障、刻意不定型（回执泛化）。
type nftSSHConfigError struct{ msg string }

func (e *nftSSHConfigError) Error() string { return e.msg }

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
			return nil, markRepoError(err)
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
				return nil, &nftSSHConfigError{msg: "nftables 节点缺少 SSH 配置"}
			}
			return nil, markRepoError(err)
		}
		sshCfg, err := sshConfigFromModel(cfg)
		if err != nil {
			// sshConfigFromModel 的失败为自著配置校验（配置不完整），
			// 在本 helper 边界定型（其余调用面不受影响）。
			return nil, &nftSSHConfigError{msg: err.Error()}
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
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return runtimenft.NodePlan{}, nil, markRepoError(err)
	}
	forwards, err := h.repo.ListActiveForwardsByNode(nodeID)
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return runtimenft.NodePlan{}, nil, markRepoError(err)
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
	// 暂停过滤先于 NAT reconcile 落地：封禁集先行，已建立连接在 DNAT
	// 规则移除/重建的窗口内同样被 drop，不留继续转发的空档。封禁失败
	// 即整次 sync 失败（与 D6 一致：数据面未达成则调用方不前进 DB），
	// 避免出现「DB 显示暂停、流量实际还在跑」的静默缺口。封禁集独立
	// 于主表，主表重建不影响它，故放在 drain 之前执行无碍计费口径。
	blockedPorts, err := h.nftBlockedPorts(nodeID, exclude, include)
	if err != nil {
		return err
	}
	for _, port := range sortedBlockedPorts(blockedPorts) {
		if err := h.nftablesManager.SetBlocked(ctx, nodeID, sshCfg, port, true); err != nil {
			return err
		}
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
		// R3-MR-01：绑定读失败不得装空集静默跳过标错——标错不全会
		// 让失败绑定的待删账被误清；失败点名记日志，下轮 sync 再结清。
		bindings, bindErr := h.repo.ListNftRuleBindingsByNode(nodeID)
		if bindErr != nil {
			log.Printf("nftables sync: list rule bindings for error marking failed node_id=%d err=%v", nodeID, bindErr)
		}
		for _, binding := range bindings {
			_ = h.repo.MarkNftRuleBindingError(binding.ForwardID, nodeID, err.Error(), now)
		}
		return err
	}
	h.markNftApplyFailed(nodeID, false)
	// 恢复收敛：plan 内端口若残留封禁（节点离线期间在面板恢复、封禁
	// 后未及解除、或端口曾被已删转发封禁后复用）一律解除，保证恢复
	// 后规则清零、不残留。应封禁的端口不在 plan 内，不受影响。
	for _, rule := range plan.Rules {
		if blockedPorts[rule.InPort] {
			continue
		}
		if err := h.nftablesManager.SetBlocked(ctx, nodeID, sshCfg, rule.InPort, false); err != nil {
			return err
		}
	}
	activeForwardIDs := make(map[int64]struct{}, len(plan.Rules))
	for _, rule := range plan.Rules {
		activeForwardIDs[rule.ForwardID] = struct{}{}
		hash := result.Hashes[rule.ForwardID]
		_ = h.repo.UpsertNftRuleBinding(modelToRuleBindingInput(nodeID, rule, hash), now)
	}
	// R3-MR-01：陈旧绑定清理的读失败必须显式跳过本轮清理并记
	// 日志——空集绝不参与删除差集判定（旧形态恰为不删、但静默
	// 不可审计）；孤儿绑定由下一轮 sync 再清。
	bindings, bindErr := h.repo.ListNftRuleBindingsByNode(nodeID)
	if bindErr != nil {
		log.Printf("nftables sync: list rule bindings for stale sweep failed, sweep skipped node_id=%d err=%v", nodeID, bindErr)
	}
	for _, binding := range bindings {
		if _, ok := activeForwardIDs[binding.ForwardID]; ok {
			continue
		}
		_ = h.repo.DeleteNftRuleBindingsByForward(binding.ForwardID)
	}
	// R2-E2-02（FIX-BASELINE）：新绑定/新 hash 的计数基线在落成
	// 时刻钉定，不再等首个 30s 周期样本（窗口内流量会被首样本
	// 基线吞掉）。失败不阻塞 sync，见 settleNftCountersAfterSync。
	h.settleNftCountersAfterSync(ctx, nodeID, cfgModel)
	return nil
}

// nftBlockedPorts 计算本次 sync 后该节点应处于封禁状态的入口端口集：
//   - DB 中已暂停且在该节点有端口的转发（稳态来源：节点重启后封禁表
//     丢失、或某次解除未落地，都靠任意一次 sync 重新封禁自愈）；
//   - exclude 意图中的转发（D6 数据面优先：暂停先 reconcile 后写库，
//     此刻 DB 仍为 active，靠意图补入，否则暂停瞬间封不上）。
//
// include 意图中的转发正在恢复，纵然 DB 仍记为 paused 也不封禁。
// 端口按 forward_port 的节点归属过滤，隧道已改配置的转发不会把
// 别的节点的端口封到本节点上。
func (h *Handler) nftBlockedPorts(nodeID int64, exclude map[int64]bool, include []*forwardRecord) (map[int]bool, error) {
	resumed := make(map[int64]bool, len(include))
	for _, f := range include {
		if f != nil {
			resumed[f.ID] = true
		}
	}
	blocked := make(map[int]bool)
	addForwardPorts := func(forwardID int64) error {
		ports, err := h.listForwardPorts(forwardID)
		if err != nil {
			return err
		}
		for _, fp := range ports {
			if fp.NodeID == nodeID && fp.Port >= 1 && fp.Port <= 65535 {
				blocked[fp.Port] = true
			}
		}
		return nil
	}
	paused, err := h.repo.ListPausedForwardsByNode(nodeID)
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return nil, markRepoError(err)
	}
	for i := range paused {
		if resumed[paused[i].ID] {
			continue
		}
		if err := addForwardPorts(paused[i].ID); err != nil {
			return nil, err
		}
	}
	for id := range exclude {
		if resumed[id] {
			continue
		}
		if err := addForwardPorts(id); err != nil {
			return nil, err
		}
	}
	return blocked, nil
}

func sortedBlockedPorts(blocked map[int]bool) []int {
	ports := make([]int, 0, len(blocked))
	for port := range blocked {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
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
	// 本入口除 nodeId 外还读 resetHostKey（C3-02 显式重钉），故自行
	// 一次性解析请求体；错误回执口径与 nftablesNodeIDFromRequest 一致。
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req map[string]interface{}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	nodeID := asInt64(req["nodeId"], 0)
	if nodeID <= 0 {
		response.WriteJSON(w, response.ErrDefault("参数错误"))
		return
	}
	resetHostKey := asBool(req["resetHostKey"], false)
	usesNft, err := h.nodeUsesNftables(nodeID)
	if err != nil {
		if errors.Is(err, errNftablesNodeNotFound) {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		respondInternalError(w, "nodeNftablesTest", err)
		return
	}
	if !usesNft {
		response.WriteJSON(w, response.ErrDefault("节点未启用 nftables 转发模式"))
		return
	}
	cfgModel, err := h.repo.GetNodeSSHConfig(nodeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.WriteJSON(w, response.ErrDefault("nftables 节点缺少 SSH 配置"))
			return
		}
		respondInternalError(w, "nodeNftablesTest", err)
		return
	}
	sshCfg, err := sshConfigFromModel(cfgModel)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if h.nftablesManager == nil {
		response.WriteJSON(w, response.Err(-2, "nftables manager not initialized"))
		return
	}
	ctx := context.Background()
	// C3-02 主机密钥钉入（TOFU）：未钉入、或运维显式 resetHostKey 时，
	// 先探对端呈现的指纹（探针只握手不认证，不发凭据），内存钉入后
	// 跑完整能力校验——runner 按探到的指纹校验，探针与校验之间密钥
	// 被替换必然 fail-closed；全过才落库。已钉入且未要求重置时绝不
	// 重探，杜绝静默重钉（冒名者无法借测连通洗白自己的密钥）。
	pinned := strings.TrimSpace(cfgModel.HostKeyFingerprint)
	pinning := pinned == "" || resetHostKey
	probed := ""
	if pinning {
		probed, err = h.nftablesManager.ProbeHostKey(ctx, nodeID, sshCfg)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		sshCfg.HostKeyFingerprint = probed
	}
	if err := h.nftablesManager.Test(ctx, nodeID, sshCfg); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if pinning {
		if err := h.repo.UpdateNodeSSHHostKeyFingerprint(nodeID, probed, time.Now().UnixMilli()); err != nil {
			respondInternalError(w, "nodeNftablesTest", err)
			return
		}
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"hostKeyFingerprint": probed,
			"hostKeyPinned":      true,
			"hostKeyRepinned":    resetHostKey && pinned != "",
			"warning":            "已钉入该节点当前呈现的 SSH 主机密钥指纹（首次信任）：请与在节点本机执行 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub 的输出核对一致；此后指纹不符的连接将被拒绝，节点重装/换密钥后需勾选重置主机密钥重新测连通",
		}))
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
		// R2-F4-01：reconcile 的仓储错已标记，先守卫泛化；定型
		// 业务错（未启用 nftables 模式）与 SSH/数据面错保留原文案。
		if respondInternalIfRepoError(w, "nodeNftablesReconcile: reconcile failed", err) {
			return
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// nftPendingDeleteList 是补充 E 的管理员可观测入口：列出所有待清理转发
// （含节点、重试次数、下次重试时间、最后失败原因）。
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
		respondInternalError(w, "nftPendingDeleteList", err)
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
		if errors.Is(err, errNftablesNodeNotFound) {
			response.WriteJSON(w, response.ErrDefault("节点不存在"))
			return
		}
		respondInternalError(w, "nodeNftablesClear", err)
		return
	}
	if !usesNft {
		response.WriteJSON(w, response.ErrDefault("节点未启用 nftables 转发模式"))
		return
	}
	if err := h.clearNftablesNode(nodeID); err != nil {
		respondInternalError(w, "nodeNftablesClear", err)
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}
