package handler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/store/model"
)

const (
	notifyEventUserQuota   = "user_quota"
	notifyEventDailyReport = "daily_report"

	notifyConfigDailyHour     = "notify_daily_hour"
	notifyConfigDailyLast     = "notify_daily_last"
	notifyConfigDailySnapshot = "notify_daily_inbound_total"

	notifyFlowWarnRatio  = 0.9
	notifyCheckInterval  = 10 * time.Minute
	defaultDailyHour     = 9
	notifyUserExpiryDays = 3
)

func formatBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}

// alertOnce records key and reports whether it is new.
func (n *notifier) alertOnce(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.alerted[key] {
		return false
	}
	n.alerted[key] = true
	return true
}

// unalert 回滚 alertOnce 已占位的键（A7-03）：发送失败时调用，使
// 下一轮检查能重试同一条告警，而不是「状态已标记、消息从未送达」。
func (n *notifier) unalert(keys ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, k := range keys {
		delete(n.alerted, k)
	}
}

// clearAlerts forgets keys with the prefix, so the alert can fire again
// (e.g. after a user's traffic was reset).
func (n *notifier) clearAlerts(prefix string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for k := range n.alerted {
		if strings.HasPrefix(k, prefix) {
			delete(n.alerted, k)
		}
	}
}

func expiryText(expMs int64, now time.Time) (string, bool) {
	exp := time.UnixMilli(expMs)
	left := exp.Sub(now)
	if left <= 0 {
		return exp.Format("2006-01-02 15:04") + "（已到期）", true
	}
	return fmt.Sprintf("%s（剩余约 %d 天）", exp.Format("2006-01-02 15:04"), int(left.Hours()/24)+1), false
}

// checkUserReminders reports forward users near their traffic limit or
// expiry, and shared subscription users near expiry.
func (h *Handler) checkUserReminders(now time.Time) {
	if h.notifier == nil {
		return
	}
	window := time.Duration(notifyUserExpiryDays) * 24 * time.Hour
	if users, err := h.repo.ListUsersForNotify(); err == nil {
		// R35-G-001：一次批量取回全部用户的日/月配额视图，供每用户的
		// 配额档检查复用（避免每用户一次 GetUserQuotaView）；取失败时
		// 视图为空、配额档检查静默跳过（总量/到期档不受影响）。
		ids := make([]int64, 0, len(users))
		for i := range users {
			ids = append(ids, users[i].ID)
		}
		quotaViews, _ := h.repo.ListUserQuotaViewsByUserIDs(ids, now)
		for _, u := range users {
			if u.RoleID == 0 || u.Status != 1 {
				continue
			}
			limit := flowLimitBytes(u.Flow, u.FlowMiB)
			used := u.InFlow + u.OutFlow
			flowKey := fmt.Sprintf("flow:%d:", u.ID)
			switch {
			case limit <= 0:
			case used >= limit:
				if h.notifier.alertOnce(flowKey + "full") {
					h.notifier.alertOnce(flowKey + "warn")
					h.notifyTracked(notifyEventUserQuota, "用户流量已用完",
						fmt.Sprintf("用户：%s\n已用：%s / %s", u.User, formatBytes(used), formatBytes(limit)),
						func(outcome notifyOutcome) {
							if outcome == notifyOutcomeFailed {
								h.notifier.unalert(flowKey+"full", flowKey+"warn")
							}
						})
				}
			case float64(used) >= float64(limit)*notifyFlowWarnRatio:
				if h.notifier.alertOnce(flowKey + "warn") {
					h.notifyTracked(notifyEventUserQuota, "用户流量即将用完",
						fmt.Sprintf("用户：%s\n已用：%s / %s（%.0f%%）", u.User, formatBytes(used), formatBytes(limit),
							float64(used)*100/float64(limit)),
						func(outcome notifyOutcome) {
							if outcome == notifyOutcomeFailed {
								h.notifier.unalert(flowKey + "warn")
							}
						})
				}
			default:
				h.notifier.clearAlerts(flowKey)
				// R3X-030：回落即静默恢复——连带清该用户流量子型的
				// 指纹（forget 连带清通道台账），否则同文案再越界被
				// 30 分钟指纹窗抑制。到期类子型不在此列。
				h.notifier.forgetUserQuotaFlow(u.User)
			}
			// R35-G-001：日/月配额档与总量档同轮检查（同为「用户流量
			// 配额」下的另一子系统，此前全程静默）。档位键独立，日档
			// 回落不触月档的 alertOnce/指纹。
			h.checkUserQuotaReminders(&u, quotaViews[u.ID])
			if u.ExpTime > 0 {
				// O8-004：已过期很久的用户跳过，避免面板重启后重报历史过期记录。
				left := time.UnixMilli(u.ExpTime).Sub(now)
				expKey := fmt.Sprintf("exp:%d:%d", u.ID, u.ExpTime)
				if left <= window && left >= -notifyLongExpired && h.notifier.alertOnce(expKey) {
					text, expired := expiryText(u.ExpTime, now)
					title := "用户即将到期"
					if expired {
						title = "用户已到期"
					}
					h.notifyTracked(notifyEventUserQuota, title, fmt.Sprintf("用户：%s\n到期：%s", u.User, text),
						func(outcome notifyOutcome) {
							if outcome == notifyOutcomeFailed {
								h.notifier.unalert(expKey)
							}
						})
				}
			}
		}
	}
	if subs, err := h.repo.ListSubscriptions(); err == nil {
		for _, s := range subs {
			if isShare(&s) && s.Enabled == 1 {
				h.checkSubQuota(&s)
			}
			// O8-004：已过期很久的订阅跳过，避免面板重启后重报历史过期记录。
			subLeft := time.UnixMilli(s.ExpireTime).Sub(now)
			if !isShare(&s) || s.Enabled != 1 || s.ExpireTime == 0 || subLeft > window || subLeft < -notifyLongExpired {
				continue
			}
			subExpKey := fmt.Sprintf("sub:%d:%d", s.ID, s.ExpireTime)
			if !h.notifier.alertOnce(subExpKey) {
				continue
			}
			text, expired := expiryText(s.ExpireTime, now)
			title := "分享订阅即将到期"
			if expired {
				title = "分享订阅已到期，已断开"
			}
			h.notifyTracked(notifyEventUserQuota, title, fmt.Sprintf("订阅：%s\n到期：%s", s.Name, text),
				func(outcome notifyOutcome) {
					if outcome == notifyOutcomeFailed {
						h.notifier.unalert(subExpKey)
					}
				})
		}
	}
}

// checkUserQuotaReminders reports a forward user's daily/monthly quota
// (UserQuota) near or at its limit (R35-G-001). It mirrors the total-flow
// warn/full discipline of checkUserReminders — same 90% warn ratio, same
// alertOnce/unalert/clearAlerts discipline — with per-period keys so one
// period's recovery never re-arms the other. Enforcement is untouched:
// this only wires the already-computed quota view into the 10-minute
// reminder loop (the view is window-normalized by the repository).
func (h *Handler) checkUserQuotaReminders(u *model.User, view *model.UserQuotaView) {
	if view == nil {
		return
	}
	h.checkUserQuotaLevel(u, "日", "qday", view.DailyUsedBytes, view.DailyLimitGB*bytesPerGB)
	h.checkUserQuotaLevel(u, "月", "qmon", view.MonthlyUsedBytes, view.MonthlyLimitGB*bytesPerGB)
}

// checkUserQuotaLevel applies the warn/full dual档 to one quota period
// ("日"/"月"). limit <= 0 means unlimited for that period.
func (h *Handler) checkUserQuotaLevel(u *model.User, period, keyPrefix string, used, limit int64) {
	key := fmt.Sprintf("%s:%d:", keyPrefix, u.ID)
	title := "用户" + period + "配额"
	switch {
	case limit <= 0:
	case used >= limit:
		if h.notifier.alertOnce(key + "full") {
			h.notifier.alertOnce(key + "warn")
			h.notifyTracked(notifyEventUserQuota, title+"已用完",
				fmt.Sprintf("用户：%s\n%s配额已用：%s / %s", u.User, period, formatBytes(used), formatBytes(limit)),
				func(outcome notifyOutcome) {
					if outcome == notifyOutcomeFailed {
						h.notifier.unalert(key+"full", key+"warn")
					}
				})
		}
	case float64(used) >= float64(limit)*notifyFlowWarnRatio:
		if h.notifier.alertOnce(key + "warn") {
			h.notifyTracked(notifyEventUserQuota, title+"即将用完",
				fmt.Sprintf("用户：%s\n%s配额已用：%s / %s（%.0f%%）", u.User, period, formatBytes(used), formatBytes(limit),
					float64(used)*100/float64(limit)),
				func(outcome notifyOutcome) {
					if outcome == notifyOutcomeFailed {
						h.notifier.unalert(key + "warn")
					}
				})
		}
	default:
		h.notifier.clearAlerts(key)
		// R35-G-001：回落即静默恢复——连带清该档的指纹（forgetUserQuotaFlow
		// 的档位对偶），否则 alertOnce 已再武装、指纹仍在 30 分钟窗内把
		// 同文案再越界成对吞掉（R3X-030 同型）。
		h.notifier.forgetUserQuotaPeriod(u.User, period)
	}
}

// checkSubQuota reports a share subscription near or at its traffic limit.
func (h *Handler) checkSubQuota(s *model.Subscription) {
	key := fmt.Sprintf("subflow:%d:", s.ID)
	used, limit := subUsed(s), s.TrafficLimit
	switch {
	case limit <= 0:
	case used >= limit:
		if h.notifier.alertOnce(key + "full") {
			h.notifier.alertOnce(key + "warn")
			h.notifyTracked(notifyEventUserQuota, "分享订阅流量已用完，已断开",
				fmt.Sprintf("订阅：%s\n已用：%s / %s", s.Name, formatBytes(used), formatBytes(limit)),
				func(outcome notifyOutcome) {
					if outcome == notifyOutcomeFailed {
						h.notifier.unalert(key+"full", key+"warn")
					}
				})
		}
	case float64(used) >= float64(limit)*notifyFlowWarnRatio:
		if h.notifier.alertOnce(key + "warn") {
			h.notifyTracked(notifyEventUserQuota, "分享订阅流量即将用完",
				fmt.Sprintf("订阅：%s\n已用：%s / %s（%.0f%%）", s.Name, formatBytes(used), formatBytes(limit),
					float64(used)*100/float64(limit)),
				func(outcome notifyOutcome) {
					if outcome == notifyOutcomeFailed {
						h.notifier.unalert(key + "warn")
					}
				})
		}
	default:
		h.notifier.clearAlerts(key)
		// R3X-030：同 checkUserReminders 用户面——回落连带清该订阅
		// 流量子型的指纹，到期类子型不在此列。
		h.notifier.forgetSubQuotaFlow(s.Name)
	}
}

// dailyReportHour is the configured hour (0-23) in the panel's time zone.
func (h *Handler) dailyReportHour() int {
	cfg, err := h.repo.GetConfigByName(notifyConfigDailyHour)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.Value) == "" {
		return defaultDailyHour
	}
	v, err := strconv.Atoi(strings.TrimSpace(cfg.Value))
	if err != nil || v < 0 || v > 23 {
		return defaultDailyHour
	}
	return v
}

// dailyReportPending 是日报首次生成后缓存的待发原文（R2X-018）：
// 同日重试逐字复用、不重算；snapshot 是文本所依据的入站累计值，
// 仅整条投递成功时才与 daily_last 一并落盘推进。
type dailyReportPending struct {
	date     string
	text     string
	snapshot int64
	hasSnap  bool
}

// maybeDailyReport sends the summary once a day at the configured hour.
// The last sent date is stored so restarts don't repeat it.
func (h *Handler) maybeDailyReport(now time.Time) {
	if h.notifier == nil || now.Hour() != h.dailyReportHour() {
		return
	}
	if _, _, ok := h.notifyConfig(notifyEventDailyReport); !ok {
		return
	}
	today := now.Format("2006-01-02")
	if cfg, err := h.repo.GetConfigByName(notifyConfigDailyLast); err == nil && cfg != nil && cfg.Value == today {
		return
	}
	// R2X-018：首次生成后把原文与快照缓存进内存 pending，同日重试
	// 逐字复用、不重算（旧实现 dailyReportText 生成即消费快照，补发
	// 的日报流量数值失真为两次生成间的增量）。
	h.dailyMu.Lock()
	pending := h.dailyPending
	if pending == nil || pending.date != today {
		text, snapshot, hasSnap := h.dailyReportText(now)
		pending = &dailyReportPending{date: today, text: text, snapshot: snapshot, hasSnap: hasSnap}
		h.dailyPending = pending
	}
	h.dailyMu.Unlock()
	// O8-002：先同步发送成功、再落盘"今日已发"标记。发送失败时不落盘，
	// 下一个 tick（同小时内）会重试补发。
	if !h.notifySync(notifyEventDailyReport, "每日汇总 "+today, pending.text) {
		return
	}
	// 仅整条投递成功时才依次推进 snapshot 与 daily_last，并清 pending。
	if pending.hasSnap {
		_ = h.repo.UpsertConfig(notifyConfigDailySnapshot, strconv.FormatInt(pending.snapshot, 10), now.UnixMilli())
	}
	_ = h.repo.UpsertConfig(notifyConfigDailyLast, today, now.UnixMilli())
	h.dailyMu.Lock()
	if h.dailyPending == pending {
		h.dailyPending = nil
	}
	h.dailyMu.Unlock()
}

// dailyReportText 是纯计算（R2X-018）：返回日报文本与本次依据的入站
// 累计快照，不写任何配置。快照的推进由 maybeDailyReport 在整条投递
// 成功后统一完成。
func (h *Handler) dailyReportText(now time.Time) (string, int64, bool) {
	var lines []string
	if online, total, err := h.repo.CountNodesOnline(); err == nil && total > 0 {
		lines = append(lines, fmt.Sprintf("节点在线：%d / %d", online, total))
	}
	if kernels, err := h.repo.ListNodeKernels(0); err == nil && len(kernels) > 0 {
		bad := 0
		for _, k := range kernels {
			if k.Status == kernelStatusError {
				bad++
			}
		}
		lines = append(lines, fmt.Sprintf("内核出错：%d / %d", bad, len(kernels)))
	}
	if h.externalProbes != nil {
		h.externalProbes.mu.RLock()
		down, checked := 0, 0
		for _, p := range h.externalProbes.results {
			if p.Skipped {
				continue
			}
			checked++
			if !p.OK {
				down++
			}
		}
		h.externalProbes.mu.RUnlock()
		if checked > 0 {
			lines = append(lines, fmt.Sprintf("导入节点连不上：%d / %d", down, checked))
		}
	}
	var snapshot int64
	var hasSnap bool
	if total, err := h.repo.SumInboundTraffic(); err == nil {
		snapshot, hasSnap = total, true
		text := "入站流量（累计）：" + formatBytes(total)
		if cfg, err := h.repo.GetConfigByName(notifyConfigDailySnapshot); err == nil && cfg != nil {
			if prev, err := strconv.ParseInt(cfg.Value, 10, 64); err == nil && total >= prev {
				text = "入站流量（过去一天）：" + formatBytes(total-prev)
			}
		}
		lines = append(lines, text)
	}
	if flow, err := h.repo.SumStatisticsFlowSince(now.Add(-24 * time.Hour).UnixMilli()); err == nil && flow > 0 {
		lines = append(lines, "转发流量（过去 24 小时）："+formatBytes(flow))
	}
	// R35-G-001：日报披露当前日/月配额超额用户数（与 10 分钟轮询的
	// warn/full 通知互补：日报是管理员每日必读的一次性汇总，超额用户
	// 在此露面、无超额时不占行）。只计数，不展开明细。
	if users, err := h.repo.ListUsersForNotify(); err == nil {
		ids := make([]int64, 0, len(users))
		for i := range users {
			if users[i].RoleID == 0 || users[i].Status != 1 {
				continue
			}
			ids = append(ids, users[i].ID)
		}
		if views, err := h.repo.ListUserQuotaViewsByUserIDs(ids, now); err == nil {
			over := 0
			for _, v := range views {
				if v != nil && (v.DisabledByQuota == 1 || isUserQuotaExceeded(v)) {
					over++
				}
			}
			if over > 0 {
				lines = append(lines, fmt.Sprintf("日/月配额超额用户：%d 名", over))
			}
		}
	}
	if subs, err := h.repo.ListSubscriptions(); err == nil {
		active, soon, total := 0, 0, 0
		for i := range subs {
			if !isShare(&subs[i]) {
				continue
			}
			total++
			if subActive(&subs[i], now) {
				active++
				if subs[i].ExpireTime > 0 && time.UnixMilli(subs[i].ExpireTime).Sub(now) <= notifyUserExpiryDays*24*time.Hour {
					soon++
				}
			}
		}
		if total > 0 {
			lines = append(lines, fmt.Sprintf("分享订阅：%d 个有效，%d 个 3 天内到期", active, soon))
		}
	}
	if len(lines) == 0 {
		return "暂无数据", snapshot, hasSnap
	}
	return strings.Join(lines, "\n"), snapshot, hasSnap
}

// runNotifyLoop runs the periodic reminders: node and user expiry,
// traffic limits and the daily summary.
func (h *Handler) runNotifyLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	// O8-001：启动后先等一个离线宽限期让 agent 完成重连，再扫描仍离线的节点
	// 并补发一次通知（重启前已掉线的节点否则永不告警）。
	select {
	case <-ctx.Done():
		return
	case <-time.After(nodeOfflineGrace):
	}
	h.checkStartupOfflineNodes()
	ticker := time.NewTicker(notifyCheckInterval)
	defer ticker.Stop()
	for {
		now := time.Now()
		// O8-003：每轮兜底清理已删除节点的通知状态（删除入口未调用 forgetNode 时）。
		h.pruneStaleNodeAlerts()
		h.checkNodeExpiry(now)
		h.checkUserReminders(now)
		h.maybeDailyReport(now)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
