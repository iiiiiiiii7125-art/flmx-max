package handler

import (
	"fmt"
	"log"
	"sync"

	"go-backend/internal/metrics"
)

// --- 宿主机累积面越阈告警（包 K）---
//
// conntrack 表与根盘都是「无界累积→撞墙即死」的面：表满＝内核
// 全包皆丢（机器看似关机）、盘满＝服务写不进 DB/日志崩溃循环
// （ECS1686 一日双案）。agent 侧已有保底收敛（启动自检抬
// conntrack 下限、压超时、封 journald），但保底够不着的情形
// （agent 未升级的存量机、外部灌入速率压过收敛、非产品文件涨
// 盘）必须有撞墙前的预警。本判定挂在指标摄入点（handler 的
// SetNodeMetricHook）：每个样本必过、无旁路，与 kernel_error
// 的单漏斗纪律同型。
//
// 口径（两面一致）：用量 ≥80% 进入告警（每面每节点只发一
// 次）、回落到 <70% 解除并重新武装（迟滞防阈值线抖动连发）；
// 派发失败（notifyOutcomeFailed）回滚标记，下一一样本重试
// （X-P2-8/BC-16 同口径）；解除时清该问题指纹，使 30 分钟去
// 重窗口内再越阈不被吞（A7-02 同口径）。conntrack max<=0＝
// 该面不在场（旧 agent/未加载），不判定不告警；磁盘 0＝采集
// 失败值（agent getDiskInfo 失败返回 0），同样不判定。

const (
	resourceAlertHighPct  = 80.0
	resourceAlertRearmPct = 70.0
)

// resourceAlertTracker 按「节点×面」记已告警状态（内存态，与
// notifier 的状态同层：面板重启后至多重报一次进行中的越阈，
// 不落库——告警状态本身不是数据资产）。
type resourceAlertTracker struct {
	mu      sync.Mutex
	alerted map[string]bool
}

func newResourceAlertTracker() *resourceAlertTracker {
	return &resourceAlertTracker{alerted: make(map[string]bool)}
}

func resourceAlertKey(nodeID int64, surface string) string {
	return fmt.Sprintf("%d\x00%s", nodeID, surface)
}

const (
	resourceActionNone = iota
	// resourceActionEnter：本次样本越阈且此前未告警——应派发，
	// 状态已先行占位；派发失败由 caller 经 rollback 撤销。
	resourceActionEnter
	// resourceActionExit：本次样本回落到重武装线下且此前处于
	// 告警态—— caller 据此清指纹重新武装。
	resourceActionExit
)

// decide 把一个用量样本折进状态机（纯判定＋占位，无副作用于
// 通知面），迟滞带 [Rearm, High) 内保持现状不动作。
func (t *resourceAlertTracker) decide(key string, pct float64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case pct >= resourceAlertHighPct && !t.alerted[key]:
		t.alerted[key] = true
		return resourceActionEnter
	case pct < resourceAlertRearmPct && t.alerted[key]:
		delete(t.alerted, key)
		return resourceActionExit
	}
	return resourceActionNone
}

func (t *resourceAlertTracker) rollback(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.alerted, key)
}

func (t *resourceAlertTracker) isAlerted(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alerted[key]
}

// observeNodeResourceUsage 是指标样本的累积面判定漏斗：由
// SetNodeMetricHook 在每个样本落库后调用，两面各走一遍同一状
// 态机（observeResourceSurface）。
func (h *Handler) observeNodeResourceUsage(nodeID int64, info metrics.SystemInfo) {
	if h == nil || h.resourceAlerts == nil {
		return
	}
	if info.ConntrackMax > 0 && info.ConntrackCount >= 0 {
		pct := float64(info.ConntrackCount) * 100 / float64(info.ConntrackMax)
		label := h.nodeLabel(nodeID)
		msg := fmt.Sprintf("节点：%s\n连接跟踪表用量：%d / %d（%d%%）\n表满后内核将丢弃全部新数据包，节点会看似离线。新版 agent 启动时会自动抬高上限并收敛超时；若该节点 agent 尚未升级或用量仍在涨，请人工核查 nf_conntrack_max 与灌入源（自检: sysctl net.netfilter.nf_conntrack_count net.netfilter.nf_conntrack_max）。",
			label, info.ConntrackCount, info.ConntrackMax, int(pct+0.5))
		h.observeResourceSurface(nodeID, "conntrack", notifyEventConntrackHigh, "节点连接跟踪表用量过高", label, pct, msg)
	}
	if info.DiskUsage > 0 {
		pct := info.DiskUsage
		label := h.nodeLabel(nodeID)
		msg := fmt.Sprintf("节点：%s\n根盘用量：%.1f%%\n盘满后服务将写不进数据库与日志。新版 agent 会自动封顶 journald；若仍在涨，请人工核查 journal、备份与升级残留（自检: df -h /; journalctl --disk-usage; du -sh /var/log/*）。",
			label, pct)
		h.observeResourceSurface(nodeID, "disk", notifyEventDiskHigh, "节点磁盘用量过高", label, pct, msg)
	}
	// agent 内存面（包 K 扩围）：会话累积的大头（内核 socket
	// 缓冲/栈/连接结构）在 Go 堆外，2.1.0 堆软帽看不见，进程
	// RSS 才是撞墙面。判据＝RSS/机器总内存，与其余面同阈值；
	// total<=0（旧 agent）判「无此面」。新版 agent 另有会话预
	// 算准入在 75% 线拒新会话保整机，本告警是预算之外（旧
	// agent、外部灌入压过预算口径时）的撞墙前预警。
	if info.MemTotalBytes > 0 && info.AgentRSSBytes > 0 {
		pct := float64(info.AgentRSSBytes) * 100 / float64(info.MemTotalBytes)
		label := h.nodeLabel(nodeID)
		msg := fmt.Sprintf("节点：%s\nagent 进程内存（RSS）：%.1f%%（%d MiB / %d MiB）\n活跃会话：%d，会话预算累计拒绝：%d，压力驱逐累计：%d\n会话内存的大头在 Go 堆外（内核缓冲与栈），堆软帽不会先触发；RSS 逼近整机内存将先 OOM 或拖垮全机。新版 agent 有按内存推导的会话预算准入（75%% 线拒新会话）与压力下驱逐最冷会话；若仍在涨，请人工核查净累积来源（自检: ss -tnp; ps -o rss= -p <agent pid>）。",
			label, pct, info.AgentRSSBytes>>20, info.MemTotalBytes>>20, info.ActiveSessions, info.BudgetRejectedTotal, info.BudgetEvictedTotal)
		h.observeResourceSurface(nodeID, "agent-memory", notifyEventAgentMemHigh, "节点内存用量过高", label, pct, msg)
	}
}

// observeResourceSurface 是单面状态机：越阈进入→派发（失败回
// 滚）、回落解除→清指纹重新武装。event/title 决定指纹口径：
// 指纹＝event\x00title\x00message，message 首行恒为「节点：
// <label>」，解除时按同一前缀清（与 kernel_error 的 forget 口
// 径一致），使同一问题在去重窗口内复发不被吞。
func (h *Handler) observeResourceSurface(nodeID int64, surface, event, title, label string, pct float64, message string) {
	key := resourceAlertKey(nodeID, surface)
	switch h.resourceAlerts.decide(key, pct) {
	case resourceActionEnter:
		h.dispatchResourceAlert(event, title, message, func(outcome notifyOutcome) {
			if outcome == notifyOutcomeFailed {
				h.resourceAlerts.rollback(key)
			}
		})
	case resourceActionExit:
		if h.notifier != nil {
			h.notifier.forgetFingerprintPrefix(event + "\x00" + title + "\x00" +
				fmt.Sprintf("节点：%s\n", label))
		}
	}
}

// dispatchResourceAlert 是资源面告警的派发口（与未落地告警的
// dispatchUpgradeFailedAlert 同形态）：事件在本函数内以
// notifyEvent* 常量直达 notifyTracked，不经 observeResourceSurface
// 的字符串形参原样转交——发射面「事件只来自常量」由
// check-doc-consistency §7 的 doc-extract 机械守卫，形参转交会
// 打断其静态证明。event 的合法取值只有四处调用点直传的三个常
// 量；落到 default 属编程错误，记日志而不派发未知事件（旧形态
// 会把任意字符串当事件发出，正是该门要防的形态）。
func (h *Handler) dispatchResourceAlert(event, title, message string, onDone func(notifyOutcome)) {
	switch event {
	case notifyEventConntrackHigh:
		h.notifyTracked(notifyEventConntrackHigh, title, message, onDone)
	case notifyEventDiskHigh:
		h.notifyTracked(notifyEventDiskHigh, title, message, onDone)
	case notifyEventAgentMemHigh:
		h.notifyTracked(notifyEventAgentMemHigh, title, message, onDone)
	default:
		log.Printf("resource alert dispatch skipped: unknown event %q", event)
	}
}
