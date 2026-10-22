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
			}
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
