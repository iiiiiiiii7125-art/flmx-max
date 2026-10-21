package handler

import (
	"context"
	"log"
	"time"
)

var nftablesTrafficCollectInterval = 30 * time.Second

func (h *Handler) StartBackgroundJobs() {
	if h == nil || h.repo == nil {
		return
	}

	h.jobsMu.Lock()
	if h.jobsStarted {
		h.jobsMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.jobsCtx = ctx
	h.jobsCancel = cancel
	h.jobsStarted = true
	h.jobsWG.Add(15)
	h.jobsMu.Unlock()

	go h.runHourlyStatsLoop(ctx)
	go h.runDailyMaintenanceLoop(ctx)
	go h.runNodeRenewalCycleLoop(ctx)
	go h.runMetricsIngestion(ctx)
	go h.runHealthChecks(ctx)
	go h.runTunnelQualityProber(ctx)
	go h.runNftablesTrafficCollectLoop(ctx)
	go h.runNftPendingDeleteRetryLoop(ctx)
	go h.runNftAutoReconcileWorker(ctx)
	go h.runFederationCleanupRetryLoop(ctx)
	go h.runKernelStatusLoop(ctx)
	go h.runExternalProbeLoop(ctx)
	go h.runNotifyLoop(ctx)
	go h.runSubscriptionLoop(ctx)
	go h.runRemoteSourceLoop(ctx)
}

func (h *Handler) runFederationCleanupRetryLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := h.retryPendingFederationRuntimeCleanup(); err != nil {
			log.Printf("federation cleanup remains pending: %v", err)
		}
		h.retryPendingPeerShareOperations()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) retryPendingPeerShareOperations() {
	nodeIDs, err := h.repo.ListPendingPeerShareNodeIDs()
	if err != nil {
		log.Printf("peer share pending operation lookup failed: %v", err)
		return
	}
	for _, nodeID := range nodeIDs {
		node, err := h.repo.GetNodeByID(nodeID)
		if err != nil || node == nil || node.Status != 1 {
			continue
		}
		if err := h.retryPendingPeerShareResourcesOnNode(nodeID); err != nil {
			log.Printf("peer share resource retry failed node_id=%d err=%v", nodeID, err)
		}
		if err := h.retryPendingPeerShareRoleRuntimesOnNode(nodeID); err != nil {
			log.Printf("peer share role retry failed node_id=%d err=%v", nodeID, err)
		}
	}
}

func (h *Handler) StopBackgroundJobs() {
	if h == nil {
		return
	}

	h.jobsMu.Lock()
	if !h.jobsStarted {
		h.jobsMu.Unlock()
		return
	}
	cancel := h.jobsCancel
	h.jobsCancel = nil
	h.jobsStarted = false
	h.jobsMu.Unlock()

	if cancel != nil {
		cancel()
	}
	h.jobsWG.Wait()
}

func (h *Handler) runMetricsIngestion(ctx context.Context) {
	defer h.jobsWG.Done()
	if h.metrics != nil {
		h.metrics.Start(ctx)
	}
}

func (h *Handler) runHealthChecks(ctx context.Context) {
	defer h.jobsWG.Done()
	if h.healthCheck != nil {
		h.healthCheck.Start(ctx)
	}
}

func (h *Handler) runTunnelQualityProber(ctx context.Context) {
	defer h.jobsWG.Done()
	if h == nil || h.qualityProber == nil {
		return
	}

	h.qualityProber.Start(ctx)
}

func (h *Handler) runNftablesTrafficCollectLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	h.runNftablesStartupReconcile(ctx)
	select {
	case <-ctx.Done():
		return
	default:
		h.runNftablesTrafficCollectJob(time.Now())
	}

	interval := nftablesTrafficCollectInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runNftablesTrafficCollectJob(time.Now())
		}
	}
}

func (h *Handler) runNftablesStartupReconcile(ctx context.Context) {
	if h == nil || h.repo == nil {
		return
	}
	nodes, err := h.repo.ListNftablesNodesForCollection()
	if err != nil {
		log.Printf("nftables startup reconcile failed op=list_nodes err=%v", err)
		return
	}
	for _, node := range nodes {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := h.syncNftablesNode(node.NodeID); err != nil {
			log.Printf("nftables startup reconcile failed node_id=%d err=%v", node.NodeID, err)
		}
	}
}

// runNftPendingDeleteRetryLoop 是 D7 的后台退避重试（R5-B3 D1-001）。
// 每 60s 扫描到期的 pending 删除，按节点聚合后每节点只 reconcile 一次；
// 成功则删库，失败则记录退避；达到 24h 上限转人工并输出可观测信号（补充 E）。
func (h *Handler) runNftPendingDeleteRetryLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		h.retryNftPendingDeletes(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) retryNftPendingDeletes(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}
	due, err := h.repo.ListDueNftPendingDeletes(now, 200)
	if err != nil {
		log.Printf("nftables pending-delete retry failed op=list err=%v", err)
		return
	}
	if len(due) == 0 {
		return
	}
	// 按节点聚合（与 R1 一致）：一节点一次 reconcile。
	byNode := make(map[int64][]int64, len(due))
	for _, row := range due {
		byNode[row.NodeID] = append(byNode[row.NodeID], row.ForwardID)
	}
	for nodeID, forwardIDs := range byNode {
		if err := h.reconcileNftablesNodeByRequest(nodeID); err != nil {
			for _, fid := range forwardIDs {
				exhausted, recErr := h.repo.RecordNftPendingDeleteAttempt(fid, err, now)
				if recErr != nil {
					log.Printf("nftables pending-delete retry failed op=record forward_id=%d err=%v", fid, recErr)
					continue
				}
				if exhausted {
					// 补充 E：退避耗尽的可计数信号（node_id、forward_id、
					// 失败原因、已重试次数），转人工处理。
					row, _ := h.repo.GetNftPendingDelete(fid)
					retryCount := 0
					lastErr := ""
					if row != nil {
						retryCount = row.RetryCount
						lastErr = row.LastError
					}
					log.Printf("nftables pending-delete exhausted node_id=%d forward_id=%d retry_count=%d last_error=%q action=manual_cleanup_required",
						nodeID, fid, retryCount, lastErr)
				}
			}
			continue
		}
		for _, fid := range forwardIDs {
			if err := h.deleteForwardByID(fid); err != nil {
				log.Printf("nftables pending-delete retry failed op=db_delete forward_id=%d err=%v", fid, err)
				continue
			}
			log.Printf("nftables pending-delete completed node_id=%d forward_id=%d", nodeID, fid)
		}
	}
}

func (h *Handler) runHourlyStatsLoop(ctx context.Context) {
	defer h.jobsWG.Done()

	for {
		wait := durationUntilNextHour(time.Now())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			h.runStatisticsFlowJob(time.Now())
		}
	}
}

func (h *Handler) runDailyMaintenanceLoop(ctx context.Context) {
	defer h.jobsWG.Done()

	for {
		wait := durationUntilNextDailyMaintenance(time.Now())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			h.runResetAndExpiryJob(time.Now())
		}
	}
}

func durationUntilNextHour(now time.Time) time.Duration {
	next := now.Truncate(time.Hour).Add(time.Hour)
	return next.Sub(now)
}

func durationUntilNextDailyMaintenance(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 5, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func (h *Handler) runStatisticsFlowJob(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}

	nowMs := now.UnixMilli()
	cutoffMs := nowMs - int64((48*time.Hour)/time.Millisecond)
	_ = h.repo.PurgeOldStatisticsFlows(cutoffMs)
	_ = h.repo.PurgeSubscriptionTraffic(now.Add(-subTrafficRetention).UnixMilli())

	hourMark := now.Truncate(time.Hour)
	hourText := hourMark.Format("15:04")
	createdTime := hourMark.UnixMilli()

	users, err := h.repo.ListAllUserFlowSnapshots()
	if err != nil {
		return
	}

	for _, user := range users {
		currentTotal := user.InFlow + user.OutFlow
		increment := currentTotal

		lastTotal, err := h.repo.GetLastStatisticsFlowTotal(user.UserID)
		if err == nil && lastTotal.Valid {
			increment = currentTotal - lastTotal.Int64
			if increment < 0 {
				increment = currentTotal
			}
		}

		_ = h.repo.CreateStatisticsFlow(user.UserID, increment, currentTotal, hourText, createdTime)
	}
}

func (h *Handler) runResetAndExpiryJob(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}

	h.resetMonthlyFlow(now)
	h.resetUserQuotaWindows(now)
	h.disableExpiredUsers(now.UnixMilli())
	h.disableExpiredUserTunnels(now.UnixMilli())
}

func (h *Handler) resetMonthlyFlow(now time.Time) {
	currentDay := now.Day()
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()

	_ = h.repo.ResetUserMonthlyFlow(currentDay, lastDay)
	_ = h.repo.ResetUserTunnelMonthlyFlow(currentDay, lastDay)
}

func (h *Handler) disableExpiredUsers(nowMs int64) {
	userIDs, err := h.repo.ListExpiredActiveUserIDs(nowMs)
	if err != nil {
		return
	}

	for _, userID := range userIDs {
		forwards, err := h.listActiveForwardsByUser(userID)
		if err == nil {
			h.pauseForwardRecords(forwards, nowMs)
		}
		_ = h.repo.DisableUser(userID)
	}
}

func (h *Handler) disableExpiredUserTunnels(nowMs int64) {
	items, err := h.repo.ListExpiredActiveUserTunnels(nowMs)
	if err != nil {
		return
	}

	for _, item := range items {
		forwards, err := h.listActiveForwardsByUserTunnel(item.UserID, item.TunnelID)
		if err == nil {
			h.pauseForwardRecords(forwards, nowMs)
		}
		_ = h.repo.DisableUserTunnel(item.ID)
	}
}

func (h *Handler) runNodeRenewalCycleLoop(ctx context.Context) {
	defer h.jobsWG.Done()

	for {
		wait := durationUntilNextNodeRenewalCycle(time.Now())
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			h.runNodeRenewalCycleJob(time.Now())
		}
	}
}

func durationUntilNextNodeRenewalCycle(now time.Time) time.Duration {
	next := now.Truncate(6 * time.Hour).Add(6 * time.Hour)
	return next.Sub(now)
}

func (h *Handler) runNodeRenewalCycleJob(now time.Time) {
	if h == nil || h.repo == nil {
		return
	}

	advanced, err := h.repo.AdvanceNodeRenewalCycles(now.UnixMilli())
	if err != nil {
		return
	}

	_ = advanced
}
