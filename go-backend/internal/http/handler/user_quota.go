package handler

import (
	"errors"
	"log"
	"net/http"
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

func (h *Handler) enforceUserQuotaIfNeeded(userID int64, quota *model.UserQuotaView) {
	if h == nil || h.repo == nil || userID <= 0 || quota == nil {
		return
	}
	if quota.DisabledByQuota == 1 || !isUserQuotaExceeded(quota) {
		return
	}

	forwards, err := h.listActiveForwardsByUser(userID)
	if err != nil {
		// O6-004：列举活跃转发失败则本次执法无从执行，记录以便排查（旧逻辑静默返回）
		log.Printf("applyUserQuotaBlock: list active forwards failed user_id=%d err=%v", userID, err)
		return
	}
	candidateIDs := make([]int64, 0, len(forwards))
	for i := range forwards {
		if forwards[i].Status == 1 {
			candidateIDs = append(candidateIDs, forwards[i].ID)
		}
	}
	now := time.Now().UnixMilli()
	// F-018（A4-001，O6-001 变体）：执法顺序改为先落库再暂停。
	// 旧顺序先逐个暂停、最后以 `_ =` 吞错调用 MarkUserQuotaDisabled：
	// Mark 一旦写失败，转发已停而 disabled/paused 列表未落库；此后
	// 第二次执法时 listActiveForwardsByUser 只查 status=1，已停转发
	// 不在列，pausedIDs=[] 会再次 Mark 成功并把空列表落库，恢复依据
	// 被彻底覆写——这批转发永久无法自动恢复且无任何日志。
	// 现顺序：先以候选全集 Mark 落库（失败即整轮中止并记 ALERT，
	// 不动任何转发；disabled 未置位，下一次流量上报会重新触发执法，
	// 自愈），再逐个暂停，最后把落库列表回写为实际暂停集合。
	if err := h.repo.MarkUserQuotaDisabled(userID, candidateIDs, now); err != nil {
		log.Printf("applyUserQuotaBlock: ALERT mark quota disabled failed user_id=%d err=%v action=abort_enforce_retry_on_next_report", userID, err)
		return
	}
	pausedIDs := make([]int64, 0, len(forwards))
	for i := range forwards {
		forward := &forwards[i]
		if forward.Status != 1 {
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
			pausedIDs = append(pausedIDs, forward.ID)
			continue
		}
		pausedIDs = append(pausedIDs, forward.ID)
	}
	// pausedIDs 是 candidateIDs 的保序子序列（同源同序追加，暂停失败
	// 仅跳过），长度不等即集合不等：回写实际集合使恢复列表精确。
	// 回写失败仅记日志——此时库中仍是候选超集，恢复时对未暂停转发
	// 置 status=1 为无害空操作，恢复依据只多不少，不会重蹈覆写丢失。
	if len(pausedIDs) != len(candidateIDs) {
		if err := h.repo.MarkUserQuotaDisabled(userID, pausedIDs, now); err != nil {
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
	release, err := h.repo.ResetUserQuotaUsage(req.UserID, req.Scope, time.Now())
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	nowMs := time.Now().UnixMilli()
	h.applyUserQuotaRelease(release, nowMs)
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
