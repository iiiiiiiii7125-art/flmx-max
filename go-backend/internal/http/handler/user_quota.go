package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

func isUserQuotaExceeded(view *model.UserQuotaView) bool {
	if view == nil {
		return false
	}
	if view.DailyLimitGB > 0 && view.DailyUsedBytes >= view.DailyLimitGB*bytesPerGB {
		return true
	}
	if view.MonthlyLimitGB > 0 && view.MonthlyUsedBytes >= view.MonthlyLimitGB*bytesPerGB {
		return true
	}
	return false
}

func (h *Handler) userQuotaBlockReason(userID int64, now int64) (string, error) {
	if h == nil || h.repo == nil || userID <= 0 {
		return "", nil
	}
	quota, err := h.repo.GetUserQuotaView(userID, time.UnixMilli(now))
	if err != nil || quota == nil {
		return "", err
	}
	if quota.DisabledByQuota == 1 || isUserQuotaExceeded(quota) {
		return "该用户流量配额已超额，禁止开启转发", nil
	}
	return "", nil
}

// lockUserQuotaEnforcement 取某用户的配额执法串行锁（FIX2-A
// V2-02）。锁表懒初始化，Handler 未经 New 构造时亦可用。
func (h *Handler) lockUserQuotaEnforcement(userID int64) func() {
	h.quotaEnforceMu.Lock()
	if h.quotaEnforceLocks == nil {
		h.quotaEnforceLocks = make(map[int64]*sync.Mutex)
	}
	mu, ok := h.quotaEnforceLocks[userID]
	if !ok {
		mu = &sync.Mutex{}
		h.quotaEnforceLocks[userID] = mu
	}
	h.quotaEnforceMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func parseQuotaPausedForwardIDs(raw string) []int64 {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (h *Handler) enforceUserQuotaIfNeeded(userID int64, quota *model.UserQuotaView) {
	if h == nil || h.repo == nil || userID <= 0 || quota == nil {
		return
	}
	// FIX2-A（V2-02）：per-user 串行化。三路入账（WS 上报 / nft 采集 /
	// realm 采集）可对同一用户并发执法，旧实现无锁：两轮基于各自视
	// 角算出的 paused 集合互相 trim 覆写，已暂停转发被挤出恢复依据
	// （并发探针坐实）。串行区内一律以 DB 最新视图为准——调用方传
	// 入的视图在等待锁期间可能已过期。
	unlock := h.lockUserQuotaEnforcement(userID)
	defer unlock()
	if fresh, err := h.repo.GetUserQuotaView(userID, time.Now()); err == nil && fresh != nil {
		quota = fresh
	}
	if !isUserQuotaExceeded(quota) {
		return
	}

	// FIX2-A（V2-01）：DisabledByQuota==1 不再恒早退。首轮暂停失败
	// （nft reconcile 失败 / 节点离线 / gost 暂停失败）的转发保持
	// active 且不在恢复依据里，旧入口见 disabled=1 即返回，这批转
	// 发永不重试、持续转发持续扣费且无告警。改为增量执法：只处理
	// 「仍 active 且不在 PausedForwardIDs」的转发，已暂停集合原样
	// 保留，最终恢复依据写并集。
	alreadyPausedIDs := parseQuotaPausedForwardIDs(quota.PausedForwardIDs)
	alreadyPaused := make(map[int64]bool, len(alreadyPausedIDs))
	for _, id := range alreadyPausedIDs {
		alreadyPaused[id] = true
	}

	forwards, err := h.listActiveForwardsByUser(userID)
	if err != nil {
		// O6-004：列举活跃转发失败则本次执法无从执行，记录以便排查（旧逻辑静默返回）
		log.Printf("applyUserQuotaBlock: list active forwards failed user_id=%d err=%v", userID, err)
		return
	}
	candidateIDs := make([]int64, 0, len(forwards))
	candidates := make([]*forwardRecord, 0, len(forwards))
	for i := range forwards {
		if forwards[i].Status == 1 && !alreadyPaused[forwards[i].ID] {
			candidateIDs = append(candidateIDs, forwards[i].ID)
			candidates = append(candidates, &forwards[i])
		}
	}
	now := time.Now().UnixMilli()
	if quota.DisabledByQuota == 1 && len(candidateIDs) == 0 {
		return // 已全部暂停且恢复依据完整，无增量可执法。
	}
	// F-018（A4-001，O6-001 变体）：执法顺序先落库再暂停。旧顺序先
	// 逐个暂停、最后以 `_ =` 吞错调用 MarkUserQuotaDisabled：Mark
	// 一旦写失败，转发已停而 disabled/paused 列表未落库；此后第二
	// 次执法时 listActiveForwardsByUser 只查 status=1，已停转发不
	// 在列，pausedIDs=[] 会再次 Mark 成功并把空列表落库，恢复依据
	// 被彻底覆写——这批转发永久无法自动恢复且无任何日志。
	// 现顺序：先写「已暂停 ∪ 候选」超集落库（失败即整轮中止并记
	// ALERT，不动任何转发，下一次流量上报重新触发执法自愈），再逐
	// 个暂停，最后把落库列表回写为实际暂停集合（超集只多不少，
	// 回写失败保持超集仍安全）。首轮（disabled 未置位）用 Mark 同
	// 时置 disabled；增量轮只更新恢复依据列表，不动 disabled_at
	// （保留首次禁用时刻）。
	supersetIDs := append(append([]int64{}, alreadyPausedIDs...), candidateIDs...)
	if quota.DisabledByQuota == 1 {
		if err := h.repo.UpdateUserQuotaPausedForwardIDs(userID, supersetIDs, now); err != nil {
			log.Printf("applyUserQuotaBlock: ALERT extend paused list failed user_id=%d err=%v action=abort_enforce_retry_on_next_report", userID, err)
			return
		}
	} else {
		if err := h.repo.MarkUserQuotaDisabled(userID, supersetIDs, now); err != nil {
			log.Printf("applyUserQuotaBlock: ALERT mark quota disabled failed user_id=%d err=%v action=abort_enforce_retry_on_next_report", userID, err)
			return
		}
	}
	pausedSet := make(map[int64]bool, len(candidates))
	// A4-001：nft 转发没有 gost 服务可 PauseService——旧实现对 nft 转发
	// 必然 not-found 后仅记日志 continue，规则不撤、流量与扣费继续，
	// 且不进 pausedIDs。按 pauseForwardRecords（flow 策略路径，D1-002）
	// 同款：按入口节点分组 reconcile-exclude 先撤规则，成功后写 paused。
	nftGroups := make(map[int64][]*forwardRecord)
	for _, forward := range candidates {
		if forward.Status != 1 {
			continue
		}
		nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID)
		if modeErr == nil && nftMode && len(entryNodeIDs) > 0 {
			// R35-FIX-F01：遗留/竞态混合态下跳过、不进 pausedSet（gost/
			// realm 服务不会被暂停）；行保持 active、恢复依据回写时
			// 被剔除，下次上报重试，记日志不静默。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				log.Printf("applyUserQuotaBlock: mixed nft state skipped forward_id=%d err=%v", forward.ID, err)
				continue
			}
			nftGroups[entryNodeIDs[0]] = append(nftGroups[entryNodeIDs[0]], forward)
			continue
		}
		if err := h.controlForwardServices(forward, "PauseService", false); err != nil {
			// O6-004：暂停服务失败时记录日志（旧逻辑静默 continue，造成"用户被禁用但转发仍在跑"的不一致且无迹可查）
			log.Printf("applyUserQuotaBlock: pause service failed forward_id=%d err=%v action=skip", forward.ID, err)
			continue
		}
		if err := h.repo.UpdateForwardStatus(forward.ID, 0, now); err != nil {
			// O6-001：DB 更新失败时服务已暂停，必须加入 pausedIDs 以便配额恢复时能恢复
			// 旧逻辑：continue 跳过，服务暂停但 DB 显示活跃且不在 pausedIDs 中，永远无法恢复
			log.Printf("applyUserQuotaBlock: UpdateForwardStatus failed for forward %d (service already paused), adding to pausedIDs for later recovery: %v", forward.ID, err)
		}
		pausedSet[forward.ID] = true
	}
	for nodeID, group := range nftGroups {
		exclude := make(map[int64]bool, len(group))
		ids := make([]int64, 0, len(group))
		for _, forward := range group {
			exclude[forward.ID] = true
			ids = append(ids, forward.ID)
		}
		// D6 数据面优先：先 reconcile 撤规则，成功后再写 DB；失败则
		// DB 保持 active、不进 pausedSet，下次流量上报重新触发执法自愈。
		if !h.reconcileNodeWithTimeout(nodeID, exclude, nil, 30*time.Second) {
			log.Printf("applyUserQuotaBlock: nft pause gap node_id=%d forward_ids=%v reason=reconcile_timeout_or_error ALERT=db_state_may_diverge", nodeID, ids)
			continue
		}
		for _, forward := range group {
			if err := h.repo.UpdateForwardStatus(forward.ID, 0, now); err != nil {
				// 同 O6-001：规则已撤，必须进 pausedSet 以便恢复时加回。
				log.Printf("applyUserQuotaBlock: UpdateForwardStatus failed for nft forward %d (rules already removed), adding to pausedIDs for later recovery: %v", forward.ID, err)
			}
			pausedSet[forward.ID] = true
		}
	}
	// 实际暂停集合 = 既有已暂停 ∪ 本轮暂停成功，按 superset 原序
	// 重建（保持「保序子序列」不变式）。与落库超集不等才回写实际
	// 集合使恢复列表精确；回写失败仅记日志——库中仍是超集，恢复
	// 时对未暂停转发置 status=1 为无害空操作，恢复依据只多不少，
	// 不会重蹈覆写丢失。
	pausedIDs := make([]int64, 0, len(supersetIDs))
	for _, id := range supersetIDs {
		if alreadyPaused[id] || pausedSet[id] {
			pausedIDs = append(pausedIDs, id)
		}
	}
	if !slices.Equal(pausedIDs, supersetIDs) {
		var err error
		if quota.DisabledByQuota == 1 {
			err = h.repo.UpdateUserQuotaPausedForwardIDs(userID, pausedIDs, now)
		} else {
			err = h.repo.MarkUserQuotaDisabled(userID, pausedIDs, now)
		}
		if err != nil {
			log.Printf("applyUserQuotaBlock: trim paused list failed user_id=%d err=%v action=keep_candidate_superset", userID, err)
		}
	}
}

func (h *Handler) applyUserQuotaRelease(release *repo.UserQuotaRelease, now int64) {
	if h == nil || h.repo == nil || release == nil || release.UserID <= 0 || !release.UnblockUser {
		return
	}
	// R5-B3 D1-002: 配额恢复时 nft 转发也要加回规则（原实现只调了 gost 路径）。
	// 按节点聚合恢复，一节点一次 reconcile。
	nftGroups := make(map[int64][]*forwardRecord)
	for _, forwardID := range release.ForwardIDs {
		forward, err := h.getForwardRecord(forwardID)
		if err != nil || forward == nil {
			continue
		}
		// R5-B5 D15: pending-delete 行不得被配额释放复活（删除流程由 pending
		// 机制接管）。注意：不得在 getForwardRecord 全局过滤（会破坏清理任务）。
		if isForwardPendingDelete(forward) {
			log.Printf("quota release skipped op=pending_delete forward_id=%d", forwardID)
			continue
		}
		if err := h.ensureUserTunnelForwardAllowed(forward.UserID, forward.TunnelID, now); err != nil {
			continue
		}
		nftMode, entryNodeIDs, modeErr := h.tunnelUsesNftables(forward.TunnelID)
		if modeErr == nil && nftMode && len(entryNodeIDs) > 0 {
			// R35-FIX-F01：遗留/竞态混合态下跳过、不恢复（nft 规则与
			// gost/realm 服务会双份下发）；行保持 paused 待管理员先
			// 编辑隧道去掉 nft/多余入口，记日志不静默。
			if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
				log.Printf("quota release skipped op=mixed_nft_state forward_id=%d err=%v", forwardID, err)
				continue
			}
			nftGroups[entryNodeIDs[0]] = append(nftGroups[entryNodeIDs[0]], forward)
			continue
		}
		if err := h.controlForwardServices(forward, "ResumeService", false); err != nil {
			continue
		}
		// R5-B5 D15: 经 ActivateForwardIfNotPending（pending 已在循环顶过滤，
		// 此处为纵深防御）。
		if err := h.ActivateForwardIfNotPending(forwardID, now); err != nil {
			log.Printf("quota release failed op=db_write forward_id=%d err=%v", forwardID, err)
		}
	}
	for nodeID, forwards := range nftGroups {
		if !h.reconcileNodeWithTimeout(nodeID, nil, forwards, 30*time.Second) {
			log.Printf("quota release gap node_id=%d reason=reconcile_timeout_or_error", nodeID)
			continue
		}
		for _, forward := range forwards {
			// R5-B5 D15: 经 ActivateForwardIfNotPending（pending 已在循环顶过滤，
			// 此处为纵深防御）。
			if err := h.ActivateForwardIfNotPending(forward.ID, now); err != nil {
				log.Printf("quota release failed op=db_write_after_reconcile forward_id=%d err=%v", forward.ID, err)
			}
		}
	}
}

func (h *Handler) resetUserQuotaWindows(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	releases, err := h.repo.RollUserQuotaWindows(now)
	if err != nil {
		return
	}
	nowMs := now.UnixMilli()
	for i := range releases {
		h.applyUserQuotaRelease(&releases[i], nowMs)
		// R35-G-001 对称面：窗口滚动解禁是「已恢复」事件，与超额通知
		// 成对——否则管理员只收到超额、不知何时恢复。每真实解禁一条，
		// 通知通道的指纹去重吸收 30 分钟内的同文案重复。
		if releases[i].UnblockUser {
			h.notifyUserQuotaRecovered(releases[i].UserID, "配额窗口已滚动")
		}
	}
}

// notifyUserQuotaRecovered reports that a user's daily/monthly quota block
// was lifted (R35-G-001 symmetric face of the exceed notify). Best effort:
// a failed username lookup only skips the message, never the release.
//
// I-01：恢复通知不再走裸 h.notify。发送前先把“待发恢复通知”标记持久
// 化进 vite_config（notify_quota_recovery_pending，按用户去重），再经
// notifyTracked 派发；回调里发送成功（或 30 分钟指纹窗内已送达而被
// 抑制、或通知未启用）才清除标记，发送失败则（重新）记下标记，由
// checkUserReminders 的后续轮次（retryPendingQuotaRecoveryNotices）
// 补发。标记先写后清、失败重记，任一交错下失败的恢复通知都不会丢失。
func (h *Handler) notifyUserQuotaRecovered(userID int64, reason string) {
	if h == nil || h.repo == nil || userID <= 0 {
		return
	}
	u, err := h.repo.GetUserByID(userID)
	if err != nil || u == nil {
		log.Printf("quota recovery notify skipped user_id=%d err=%v", userID, err)
		return
	}
	gen := h.markQuotaRecoveryPending(userID, reason)
	h.notifyTracked(notifyEventUserQuota, "用户配额已恢复",
		fmt.Sprintf("用户：%s\n%s，转发限制已解除", u.User, reason),
		func(outcome notifyOutcome) {
			if outcome == notifyOutcomeFailed {
				// 失败重记：与另一路并发派发的抑制清除交错时，标记
				// 仍能留下来等下一轮补发（见上）。R6-I-01：重记须
				// 经世代守卫，旧回调不得覆写更新的原因。
				h.remarkQuotaRecoveryPendingIfCurrent(userID, gen, reason)
				return
			}
			h.clearQuotaRecoveryPending(userID)
		})
}

// pendingQuotaRecovery is one undelivered quota-recovery notice,
// persisted in vite_config under notifyQuotaRecoveryPendingConfigKey.
//
// Gen is the generation of the mark (R6-I-01), same mechanism as the
// Gist face: every mark advances it monotonically per user, clears
// never move it, and a Failed callback carries the generation its
// dispatch was sent under. Records written before generations existed
// unmarshal with Gen 0 and are advanced past by the next mark.
type pendingQuotaRecovery struct {
	UserID int64  `json:"userId"`
	Reason string `json:"reason"`
	At     int64  `json:"at"`
	Gen    int64  `json:"gen"`
}

// notifyQuotaRecoveryPendingConfigKey holds the pending quota-recovery
// notices (I-01). Runtime ledger, not admin config: excluded from
// backups by backupRuntimeExcludedConfigKeys.
const notifyQuotaRecoveryPendingConfigKey = "notify_quota_recovery_pending"

// notifyQuotaRecoveryPendingGenConfigKey holds the last issued
// generation per user (R6-I-01), as a JSON object mapping decimal user
// ID to generation. Runtime ledger, excluded from backups like the
// pending list itself: it is advanced only by
// markQuotaRecoveryPending, never by clear, so that after a newer
// generation's dispatch succeeds and clears the record, an older
// generation's late failure cannot resurrect the stale reason. A
// re-mark after a retry-loop drop is benign: the retry loop
// re-validates (user gone / blocked again / exceeded again) before
// any resend, so a restored record is dropped again, never sent stale.
const notifyQuotaRecoveryPendingGenConfigKey = "notify_quota_recovery_pending_gen"

func (h *Handler) loadPendingQuotaRecoveries() []pendingQuotaRecovery {
	if h == nil || h.repo == nil {
		return nil
	}
	h.quotaRecoveryMu.Lock()
	defer h.quotaRecoveryMu.Unlock()
	return h.loadPendingQuotaRecoveriesLocked()
}

func (h *Handler) loadPendingQuotaRecoveriesLocked() []pendingQuotaRecovery {
	cfg, err := h.repo.GetConfigByName(notifyQuotaRecoveryPendingConfigKey)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return nil
	}
	var list []pendingQuotaRecovery
	if err := json.Unmarshal([]byte(cfg.Value), &list); err != nil {
		// 损坏的待发记录无法解析：记日志后丢弃，避免每轮重试都
		// 卡在同一行上。
		log.Printf("quota recovery pending list corrupt, dropping: %v", err)
		_ = h.repo.UpsertConfig(notifyQuotaRecoveryPendingConfigKey, "[]", time.Now().UnixMilli())
		return nil
	}
	return list
}

func (h *Handler) savePendingQuotaRecoveriesLocked(list []pendingQuotaRecovery) {
	if len(list) == 0 {
		if err := h.repo.UpsertConfig(notifyQuotaRecoveryPendingConfigKey, "[]", time.Now().UnixMilli()); err != nil {
			log.Printf("quota recovery pending clear failed: %v", err)
		}
		return
	}
	raw, err := json.Marshal(list)
	if err != nil {
		log.Printf("quota recovery pending marshal failed: %v", err)
		return
	}
	if err := h.repo.UpsertConfig(notifyQuotaRecoveryPendingConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("quota recovery pending save failed: %v", err)
	}
}

func (h *Handler) quotaRecoveryGenLocked() map[string]int64 {
	cfg, err := h.repo.GetConfigByName(notifyQuotaRecoveryPendingGenConfigKey)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return map[string]int64{}
	}
	var gens map[string]int64
	if err := json.Unmarshal([]byte(cfg.Value), &gens); err != nil {
		log.Printf("quota recovery pending generation map corrupt, dropping: %v", err)
		return map[string]int64{}
	}
	if gens == nil {
		return map[string]int64{}
	}
	return gens
}

func (h *Handler) saveQuotaRecoveryGenLocked(gens map[string]int64) {
	raw, err := json.Marshal(gens)
	if err != nil {
		log.Printf("quota recovery pending generation marshal failed: %v", err)
		return
	}
	if err := h.repo.UpsertConfig(notifyQuotaRecoveryPendingGenConfigKey, string(raw), time.Now().UnixMilli()); err != nil {
		log.Printf("quota recovery pending generation save failed: %v", err)
	}
}

// markQuotaRecoveryPending arms (or re-arms) the pending mark for one
// user with a fresh generation and returns it. The watermark is written
// before the list for the same reason as the Gist face: a failed list
// write after a watermark advance is harmless, while a list written
// ahead of the watermark could let a stale callback resurrect after a
// success-clear.
func (h *Handler) markQuotaRecoveryPending(userID int64, reason string) int64 {
	if h == nil || h.repo == nil || userID <= 0 {
		return 0
	}
	h.quotaRecoveryMu.Lock()
	defer h.quotaRecoveryMu.Unlock()
	list := h.loadPendingQuotaRecoveriesLocked()
	gens := h.quotaRecoveryGenLocked()
	key := strconv.FormatInt(userID, 10)
	gen := gens[key] + 1
	for i := range list {
		if list[i].UserID == userID && list[i].Gen >= gen {
			gen = list[i].Gen + 1
		}
	}
	gens[key] = gen
	h.saveQuotaRecoveryGenLocked(gens)
	now := time.Now().UnixMilli()
	for i := range list {
		if list[i].UserID == userID {
			list[i].Reason = reason
			list[i].At = now
			list[i].Gen = gen
			h.savePendingQuotaRecoveriesLocked(list)
			return gen
		}
	}
	list = append(list, pendingQuotaRecovery{UserID: userID, Reason: reason, At: now, Gen: gen})
	h.savePendingQuotaRecoveriesLocked(list)
	return gen
}

// remarkQuotaRecoveryPendingIfCurrent is the quota face of the R6-I-01
// generation guard (see remarkGistFailurePendingIfCurrent for the
// rule): re-arm only if the record still carries gen, or the record
// is gone and no newer mark for this user has been issued since gen.
// A newer reason, or a newer generation's success-clear, makes the
// callback stale.
func (h *Handler) remarkQuotaRecoveryPendingIfCurrent(userID, gen int64, reason string) {
	if h == nil || h.repo == nil || userID <= 0 || gen <= 0 {
		return
	}
	h.quotaRecoveryMu.Lock()
	defer h.quotaRecoveryMu.Unlock()
	list := h.loadPendingQuotaRecoveriesLocked()
	now := time.Now().UnixMilli()
	for i := range list {
		if list[i].UserID == userID {
			if list[i].Gen != gen {
				return
			}
			list[i].Reason = reason
			list[i].At = now
			h.savePendingQuotaRecoveriesLocked(list)
			return
		}
	}
	gens := h.quotaRecoveryGenLocked()
	if gens[strconv.FormatInt(userID, 10)] != gen {
		return
	}
	list = append(list, pendingQuotaRecovery{UserID: userID, Reason: reason, At: now, Gen: gen})
	h.savePendingQuotaRecoveriesLocked(list)
}

func (h *Handler) clearQuotaRecoveryPending(userID int64) {
	if h == nil || h.repo == nil || userID <= 0 {
		return
	}
	h.quotaRecoveryMu.Lock()
	defer h.quotaRecoveryMu.Unlock()
	list := h.loadPendingQuotaRecoveriesLocked()
	kept := list[:0]
	for _, p := range list {
		if p.UserID != userID {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(list) {
		return
	}
	h.savePendingQuotaRecoveriesLocked(kept)
}

// retryPendingQuotaRecoveryNotices resends recovery notices whose send
// failed at unblock time (I-01). Called from the 10-minute
// checkUserReminders round, the same loop that retries failed exceed
// notices via alertOnce/unalert. Entries whose user is gone, or who
// has since been blocked again, are dropped: the recovery is no
// longer true, and a later unblock re-creates the mark.
func (h *Handler) retryPendingQuotaRecoveryNotices(now time.Time) {
	if h == nil || h.repo == nil || h.notifier == nil {
		return
	}
	pending := h.loadPendingQuotaRecoveries()
	for _, p := range pending {
		if p.UserID <= 0 {
			h.clearQuotaRecoveryPending(p.UserID)
			continue
		}
		u, err := h.repo.GetUserByID(p.UserID)
		if err != nil || u == nil {
			log.Printf("quota recovery retry dropped user_id=%d: user gone", p.UserID)
			h.clearQuotaRecoveryPending(p.UserID)
			continue
		}
		if view, err := h.repo.GetUserQuotaView(p.UserID, now); err == nil && view != nil &&
			(view.DisabledByQuota == 1 || isUserQuotaExceeded(view)) {
			h.clearQuotaRecoveryPending(p.UserID)
			continue
		}
		h.notifyUserQuotaRecovered(p.UserID, p.Reason)
	}
}

// quotaResetReason renders the manual-reset scope for the recovery notice.
func quotaResetReason(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "daily":
		return "日配额已手动重置"
	case "monthly":
		return "月配额已手动重置"
	default:
		return "日/月配额已手动重置"
	}
}

func (h *Handler) userQuotaReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		UserID int64  `json:"userId"`
		Scope  string `json:"scope"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	if req.UserID <= 0 {
		response.WriteJSON(w, response.ErrDefault("用户ID不能为空"))
		return
	}
	// G-QUOTA-01：重置前校验用户存在（与 userSubscriptionSet 的
	// R2X-012 同口径），不存在即报错，不建孤儿 user_quota 行。
	u, err := h.repo.GetUserByID(req.UserID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if u == nil {
		response.WriteJSON(w, response.ErrDefault("用户不存在"))
		return
	}
	release, err := h.repo.ResetUserQuotaUsage(req.UserID, req.Scope, time.Now())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	nowMs := time.Now().UnixMilli()
	h.applyUserQuotaRelease(release, nowMs)
	// R35-G-001 对称面：手动重置解禁是「已恢复」事件（见 resetUserQuotaWindows）。
	if release != nil && release.UnblockUser {
		h.notifyUserQuotaRecovered(req.UserID, quotaResetReason(req.Scope))
	}
	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) ensureUserForwardAllowedByQuota(userID int64, now int64) error {
	reason, err := h.userQuotaBlockReason(userID, now)
	if err != nil {
		return err
	}
	if reason != "" {
		return errors.New(reason)
	}
	return nil
}
