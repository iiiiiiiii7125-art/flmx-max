package handler

import (
	"context"
	"log"
	"time"

	"go-backend/internal/store/model"
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
	// DB-3：重连重测队列须在 jobsStarted 置位前就绪，onNodeOnline 的
	// scheduleNodeReconnectProbe 在锁内读它。
	h.reconnectProbes = newNodeReconnectProbeQueue()
	// jobsWG 计数采用启动点相邻 Add(1) 形态：下方每个 go h.run*
	// 前紧邻一次 Add(1)，与各循环首行的 defer Done() 一一配对，
	// 计数由结构自维护——增删循环时增删其相邻 Add(1) 即可。此前
	// 的写死总数 Add(16) 与启动全集脱钩、靠人工注释纪律对齐，
	// 2.1.0 删除波次曾两度人工同步（18 → 17 → 16），属 PD-23
	// 硬编码计数漂移原型面；结构哨兵见 jobs_wg_structure_test.go。
	h.jobsMu.Unlock()

	// R4B03-001 启动清扫腿：同步复位上个进程周期残留的僵尸在线
	// 标记（本地非 nftables 节点），赶在任何消费 status 的路径之前。
	h.resetLocalNodeStatusesAtStartup()

	// FIX-D（D6-07）：通知派发 worker 绑定 jobs ctx，随 jobs 一起停。
	h.ensureNotifyDispatcher()

	h.jobsWG.Add(1)
	go h.runHourlyStatsLoop(ctx)
	h.jobsWG.Add(1)
	go h.runResetAndExpiryLoop(ctx)
	h.jobsWG.Add(1)
	go h.runNodeRenewalCycleLoop(ctx)
	h.jobsWG.Add(1)
	go h.runMetricsIngestion(ctx)
	h.jobsWG.Add(1)
	go h.runHealthChecks(ctx)
	h.jobsWG.Add(1)
	go h.runTunnelQualityProber(ctx)
	h.jobsWG.Add(1)
	go h.runNftablesTrafficCollectLoop(ctx)
	h.jobsWG.Add(1)
	go h.runNftPendingDeleteRetryLoop(ctx)
	h.jobsWG.Add(1)
	go h.runNftAutoReconcileWorker(ctx)
	h.jobsWG.Add(1)
	go h.runKernelStatusLoop(ctx)
	h.jobsWG.Add(1)
	go h.runExternalProbeLoop(ctx)
	h.jobsWG.Add(1)
	go h.runNodeReconnectProbes(ctx, h.reconnectProbes, 5*time.Second)
	// R4B03-001 周期对账腿：DB 在线但 WS 无会话且超过重连宽限的
	// 本地非 nftables 节点复位为离线。
	h.jobsWG.Add(1)
	go h.runNodeStatusReconcileLoop(ctx)
	h.jobsWG.Add(1)
	go h.runNotifyLoop(ctx)
	// A4-V-06：升级落地核验的截止扫描（启动首扫接管面板重启前
	// 的待核验记录）。
	h.jobsWG.Add(1)
	go h.runNodeUpgradeVerifyLoop(ctx)
	h.jobsWG.Add(1)
	go h.runSubscriptionLoop(ctx)
	h.jobsWG.Add(1)
	go h.runRemoteSourceLoop(ctx)
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
	h.stopNotifyDispatcher()
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
		// R3X-018（E3-04）：pending 终删此前从不刷新白名单——走共用
		// 漏斗：终删前按该节点本批转发集收集受影响入站，本批终删完成
		// 后按节点聚合刷新一次（与本 job 既有按节点聚合节奏同构）。
		// pending 窗口内（reconcile 失败、未终删）不刷新（N-04）。
		affectedInboundIDs := h.inboundIDsForForwards(forwardIDs)
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
					// R2-E1-13 终局语义点名（低/观察，现状登记不改行为）：
					// 耗尽后该转发 status=2 行与 pending-delete 记账、
					// 端口台账无限期保留（列表隐藏、端口仍占用），自动
					// 路径不再重试；终局只有人工 force-delete 一条。
					// 保留集可经管理员端点 GET
					// /api/v1/node/nftables/pending-deletes 枚举点名
					// （nftPendingDeleteList），与本日志的
					// action=manual_cleanup_required 构成可观测闭环。
					row, _ := h.repo.GetNftPendingDelete(fid)
					retryCount := 0
					lastErr := ""
					if row != nil {
						retryCount = row.RetryCount
						lastErr = row.LastError
					}
					log.Printf("nftables pending-delete exhausted node_id=%d forward_id=%d retry_count=%d last_error=%q action=manual_cleanup_required",
						nodeID, fid, retryCount, lastErr)
					// FIX-CASCADE（E1-01）：耗尽是本墓碑的最后调度
					// 点。隧道尚存的维持人工处理口径（D7 的 24h 窗
					// 内先清规则、终局由管理员 force-delete）；隧道
					// 已被删的墓碑没有任何后续收敛路径，在此终删并
					// 点名——否则其行以已删 tunnel_id 无限悬空。终
					// 删走 DeleteForwardCascade 全集（含台账行与配
					// 额暂停清单摘除）。
					if fwd, ferr := h.repo.GetForwardRecord(fid); ferr == nil && fwd != nil {
						if tunnel, terr := h.repo.GetTunnelRecordRaw(fwd.TunnelID); terr == nil && tunnel == nil {
							if derr := h.deleteForwardByID(fid); derr != nil {
								log.Printf("nftables pending-delete orphan finalize failed forward_id=%d err=%v", fid, derr)
							} else {
								log.Printf("nftables pending-delete orphan finalized node_id=%d forward_id=%d tunnel_id=%d", nodeID, fid, fwd.TunnelID)
							}
						}
					}
				}
			}
			continue
		}
		// R7X-002 防御纵深（非关闭条件，主修复在 importForwards 的
		// 侧表收口）：终删前复查行 status——侧表记账与行状态不一致
		// 时（例如该转发已被备份恢复、status 已归一/直通为非 2，
		// 或记账系存量残留），只清记账、不删行；行已不存在时维持
		// 既有级联清理口径。复查失败则本节点本批不终删（记账保留、
		// 下一 tick 重试），不冒删行风险。
		statusMap, statusErr := h.repo.GetForwardStatusMap(forwardIDs)
		if statusErr != nil {
			log.Printf("nftables pending-delete retry failed op=status_recheck node_id=%d err=%v", nodeID, statusErr)
			continue
		}
		finalized := false
		for _, fid := range forwardIDs {
			if st, ok := statusMap[fid]; ok && st != model.ForwardStatusPendingDelete {
				if err := h.repo.DeleteNftPendingDelete(fid); err != nil {
					log.Printf("nftables pending-delete retry failed op=clear_stale_bookkeeping forward_id=%d err=%v", fid, err)
					continue
				}
				log.Printf("nftables pending-delete bookkeeping cleared without deleting forward node_id=%d forward_id=%d status=%d", nodeID, fid, st)
				continue
			}
			if err := h.deleteForwardByID(fid); err != nil {
				log.Printf("nftables pending-delete retry failed op=db_delete forward_id=%d err=%v", fid, err)
				continue
			}
			finalized = true
			log.Printf("nftables pending-delete completed node_id=%d forward_id=%d", nodeID, fid)
		}
		// R3X-018：本批终删完成后统一刷新一次（漏斗收尾）。
		// T-001：后台任务无回执可进，刷新失败记日志留痕（内层
		// refreshInboundWhitelist 已逐条记错，此处只记汇总）。
		if finalized {
			if failed := h.refreshInboundWhitelists(affectedInboundIDs); len(failed) > 0 {
				log.Printf("nftables pending-delete whitelist refresh failed inbound_ids=%v (kernel still enforces pre-change allow list)", failed)
			}
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

// resetAndExpiryInterval is the cadence of the reset/expiry
// maintenance loop. This round used to run once a day (a timer aimed
// at 00:00:05), so an expired user kept running until the next daily
// pass — up to almost 24h past expiry — and a monthly reset missed
// while the panel was down waited just as long to be caught up. The
// loop now runs once at startup and then ticks every minute. Every
// step of the round is safe at this frequency: the monthly flow reset
// is due-based with an exactly-once marker (monthlyFlowDue), the
// quota window roll is day/month-key based, the quota enforcement
// reconcile is incremental, and the expiry lists only return rows
// that are still active.
var resetAndExpiryInterval = time.Minute

func (h *Handler) runResetAndExpiryLoop(ctx context.Context) {
	defer h.jobsWG.Done()

	interval := resetAndExpiryInterval
	if interval <= 0 {
		interval = time.Minute
	}

	// Startup pass: catch up whatever came due while the process was
	// down (a missed monthly reset day, users who expired during the
	// outage) instead of waiting for the first tick.
	select {
	case <-ctx.Done():
		return
	default:
		h.runResetAndExpiryJob(time.Now())
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.runResetAndExpiryJob(time.Now())
		}
	}
}

func durationUntilNextHour(now time.Time) time.Duration {
	next := now.Truncate(time.Hour).Add(time.Hour)
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
		log.Printf("statistics flow job failed op=list err=%v", err)
		return
	}

	for _, user := range users {
		currentTotal := user.InFlow + user.OutFlow
		// R31-L3-H01: no baseline (fresh restore — statistics_flow is
		// not in any backup segment — first run, or the baseline aged
		// out of the 48h purge window) means the lifetime total cannot
		// be read as an hourly delta. Record a baseline-only row
		// (increment 0) instead of billing the whole lifetime total as
		// one hour; the next hour then meters the real delta. The
		// negative-delta branch below is unchanged: a counter reset
		// (monthly reset) still bills the post-reset total.
		increment := int64(0)

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
	h.reconcileUserQuotaEnforcement(now)
	h.disableExpiredUsers(now.UnixMilli())
	h.disableExpiredUserTunnels(now.UnixMilli())
	// R2-038：事件型状态对账清扫（内部有小时级门，失败不影响
	// 上面任何一步，见 state_sweep.go）。
	h.sweepEventStates(now)
}

func (h *Handler) resetMonthlyFlow(now time.Time) {
	// R11X-005: due-type catch-up (repo.monthlyFlowDue, aligned with
	// subDueReset). Errors must not be swallowed silently — the old
	// `_ =` here is what made a transient failure on the reset day
	// lose the whole month with no trace.
	if err := h.repo.ResetUserMonthlyFlow(now); err != nil {
		log.Printf("monthly flow reset failed op=ResetUserMonthlyFlow now=%s err=%v", now.Format(time.RFC3339), err)
	}
	if err := h.repo.ResetUserTunnelMonthlyFlow(now); err != nil {
		log.Printf("monthly flow reset failed op=ResetUserTunnelMonthlyFlow now=%s err=%v", now.Format(time.RFC3339), err)
	}
}

func (h *Handler) disableExpiredUsers(nowMs int64) {
	userIDs, err := h.repo.ListExpiredActiveUserIDs(nowMs)
	if err != nil {
		log.Printf("expired user disable failed op=list err=%v", err)
		return
	}

	for _, userID := range userIDs {
		forwards, err := h.listActiveForwardsByUser(userID)
		if err == nil {
			h.pauseForwardRecords(forwards, nowMs)
		}
		if err := h.repo.DisableUser(userID); err != nil {
			log.Printf("expired user disable failed op=disable user_id=%d err=%v", userID, err)
		}
	}
}

func (h *Handler) disableExpiredUserTunnels(nowMs int64) {
	items, err := h.repo.ListExpiredActiveUserTunnels(nowMs)
	if err != nil {
		log.Printf("expired user tunnel disable failed op=list err=%v", err)
		return
	}

	for _, item := range items {
		forwards, err := h.listActiveForwardsByUserTunnel(item.UserID, item.TunnelID)
		if err == nil {
			h.pauseForwardRecords(forwards, nowMs)
		}
		if err := h.repo.DisableUserTunnel(item.ID); err != nil {
			log.Printf("expired user tunnel disable failed op=disable id=%d err=%v", item.ID, err)
		}
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
		log.Printf("node renewal cycle job failed op=advance err=%v", err)
		return
	}

	_ = advanced
}
