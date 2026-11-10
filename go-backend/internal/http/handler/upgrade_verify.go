package handler

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// A4-V-06 落地核验：面板下发 UpgradeAgent / RollbackAgent 时同时
// 握有「目标版本」，节点重连时又握有「新自报版本」——旧实现从不
// 比对，ACK 后静默失败的升级（脚本失败、自动回滚到旧版、新版起
// 来连不上）在面板上只剩「离线」或「版本号没变」，要靠人肉盯列
// 表。本文件补上核验环：
//   - 下发成功即落库一条待核验记录（节点、种类、目标版本、期望
//     哈希、下发时自报、下发与截止时刻），面板重启不丢；
//   - onNodeOnline 在自报落库后第一时间比对：命中目标即核销
//     landed 并记日志；未命中只记 observed（脚本可能还在换版途
//     中，不在一次握手上判死刑）；
//   - 后台扫描（runNodeUpgradeVerifyLoop，启动即扫一轮）把过截
//     止仍 pending 的记录置 failed，并经现行通知面发一条节点级
//     告警（事件 node_upgrade_failed，点名期望与实际）；
//   - failed 之后节点又以目标版本回连，翻 landed 并标 late——
//     告警已按时发出不撤回，但状态面不得留下与版本列矛盾的假
//     失败。
//   - 无目标回退（老 agent 无 toVersion）成功下发时，既有未结
//     行置 superseded 结案（LB-03）：回退意图已接管版本走向，
//     原行留到截止只会误发与回退意图相冲的告警；结案记理由、
//     不伪造 landed、不发通知。
// 回执语义（M-0051）不变：回执仍是 dispatched + expectedVersion，
// 本核验环是回执之外的系统侧对账，不回头改回执形状。

const (
	upgradeVerifyKindUpgrade  = "upgrade"
	upgradeVerifyKindRollback = "rollback"
)

// nodeUpgradeVerifyWindow 是下发回执（ACK）之后等待节点以目标版
// 本回连的窗口。取值依据：ACK 的含义是 agent 已完成下载/校验/
// 备份并拉起换版脚本（长耗时段都在 ACK 之前、在面板的
// upgradeTimeout 等待内），ACK 之后只剩节点本地的停服→换版→
// 启服→agent 启动→WS 回连，实测与代码预算都在数十秒量级；
// 5 分钟给出约一个数量级的余量（慢启动、systemd 限速 reset、
// 面板短暂重启导致的重连退避都涵盖），同时足够短、能在同一运
// 维会话内把「没落地」暴露出来。变量形态供测试收紧。
var nodeUpgradeVerifyWindow = 5 * time.Minute

// nodeUpgradeVerifySweepInterval 是后台扫描周期：相对 5 分钟窗
// 口足够细（最坏多等 30s 才发告警），又不给 DB 添高频查询。
var nodeUpgradeVerifySweepInterval = 30 * time.Second

// upgradeSHAAnchor 命名期望哈希的下发形态（F2-12）：已下发
// 返回空串；未下发时区分「架构辨不出」（节点自报版本不带架构
// 后缀的格式异常）与「哈希未就绪」两类降级，供核验记录、回执
// 与告警面点名，不让回退锚静默生效。
func upgradeSHAAnchor(nodeArch, expectedSHA string) string {
	if expectedSHA != "" {
		return ""
	}
	if nodeArch == "" {
		return "arch-unresolvable"
	}
	return "hash-unavailable"
}

// recordNodeUpgradeVerification 在升级类命令下发成功后落待核验
// 记录。目标版本为空（如无代际配对信息的老 agent 回退，面板根
// 本不知道目标）时不落记录：无目标可比对，硬造一条永远命不中
// 的记录只会误报——该情形的回执已按 M-0051 明示「未经校验」。
// 记录失败只记日志：命令已经下发，核验环的缺失不得反噬下发本
// 身的回执。
func (h *Handler) recordNodeUpgradeVerification(nodeID int64, kind, targetVersion, expectedSHA, shaAnchor string) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		return
	}
	now := time.Now()
	fromVersion := ""
	if node, err := h.repo.GetNodeByID(nodeID); err == nil && node != nil && node.Version.Valid {
		fromVersion = strings.TrimSpace(node.Version.String)
	}
	rec := &model.NodeUpgradeVerification{
		NodeID:        nodeID,
		Kind:          kind,
		TargetVersion: targetVersion,
		ExpectedSHA:   strings.TrimSpace(expectedSHA),
		SHAAnchor:     strings.TrimSpace(shaAnchor),
		FromVersion:   fromVersion,
		DispatchedAt:  now.UnixMilli(),
		DeadlineAt:    now.Add(nodeUpgradeVerifyWindow).UnixMilli(),
		CreatedTime:   now.UnixMilli(),
		UpdatedTime:   now.UnixMilli(),
	}
	if err := h.repo.UpsertNodeUpgradeVerification(rec); err != nil {
		log.Printf("node upgrade verification record failed node_id=%d kind=%s target=%s err=%v", nodeID, kind, targetVersion, err)
		return
	}
	if rec.SHAAnchor != "" {
		// F2-12：降级点名进日志，与记录/回执/告警四面一致。
		log.Printf("node upgrade verification anchor degraded node_id=%d kind=%s target=%s anchor=%s (expected sha not dispatched; verification falls back to version anchor)", nodeID, kind, targetVersion, rec.SHAAnchor)
	}
}

// upgradeVerifyTargetMatches 按现行上报型版本比较口径判自报是否
// 命中目标：compareWithReportedVersion 提取自报首段 semver 再严
// 格比（dev 粘连后缀等不可解析自报提取失败 → 不命中，与升级前
// 置比对的 fail-closed 同口径）。
func upgradeVerifyTargetMatches(targetVersion, reported string) bool {
	if strings.TrimSpace(reported) == "" {
		return false
	}
	cmp, err := compareWithReportedVersion(strings.TrimPrefix(strings.TrimSpace(targetVersion), "v"), reported)
	return err == nil && cmp == 0
}

// verifyNodeUpgradeLanding 是 onNodeOnline 的第一环：自报刚落库，
// 若该节点有未结核验记录，当场对账。命中 → 核销 landed；未命
// 中 → 只更新 observed（不在单次握手判失败：换版脚本与回连之间
// 存在旧进程残连等瞬态，判死刑是截止扫描的职责）。
func (h *Handler) verifyNodeUpgradeLanding(nodeID int64) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	rec, err := h.repo.GetNodeUpgradeVerification(nodeID)
	if err != nil || rec == nil {
		return
	}
	if rec.Status != repo.NodeUpgradeVerifyPending && rec.Status != repo.NodeUpgradeVerifyFailed {
		return
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil || node == nil || !node.Version.Valid {
		return
	}
	reported := strings.TrimSpace(node.Version.String)
	if reported == "" {
		return
	}
	now := time.Now().UnixMilli()
	if !upgradeVerifyTargetMatches(rec.TargetVersion, reported) {
		if rec.Status == repo.NodeUpgradeVerifyPending {
			_ = h.repo.ObserveNodeUpgradeVersion(nodeID, reported, now)
		}
		return
	}
	late := rec.Status == repo.NodeUpgradeVerifyFailed
	won, err := h.repo.ResolveNodeUpgradeVerification(nodeID, rec.Status, repo.NodeUpgradeVerifyLanded, reported, late, now)
	if err != nil || !won {
		return
	}
	// F2-10：核验终局（landed）释放 ACK 后执行期占用。
	h.releaseNodeUpgradeOpHold(nodeID)
	if late {
		log.Printf("node upgrade verification landed late node_id=%d kind=%s target=%s reported=%s (deadline had already fired an alert)", nodeID, rec.Kind, rec.TargetVersion, reported)
	} else {
		log.Printf("node upgrade verification landed node_id=%d kind=%s target=%s reported=%s", nodeID, rec.Kind, rec.TargetVersion, reported)
	}
}

// nodeUpgradeVerifySupersededReason 是 LB-03 结案理由的原文，落
// CloseReason 备查：点名接管方（无目标回退）与未核验事实——结案
// 只说明「这条核验被回退意图取代」，不说明回退落到了哪里。
const nodeUpgradeVerifySupersededReason = "被无目标回退接管（回退回执无 toVersion，落地未经核验）"

// supersedeNodeUpgradeVerificationForTargetlessRollback 在无目标
// 回退（老 agent 回执无代际配对、无 toVersion）成功下发后结案该
// 节点既有未结核验行。没有这一步时（LB-03）：窗内未结的升级行会
// 在回退落地后照样过截止翻 failed、误发「升级未落地」告警（内
// 容为真但与已执行的回退意图相冲），failed 行的红色状态行更会
// 长挂至下一次带目标下发。结案失败只记日志：回退命令已经下发，
// 结案缺失不得反噬回退本身的回执（与落记录同纪律）。
func (h *Handler) supersedeNodeUpgradeVerificationForTargetlessRollback(nodeID int64) {
	if h == nil || h.repo == nil || nodeID <= 0 {
		return
	}
	closed, err := h.repo.SupersedeNodeUpgradeVerification(nodeID, nodeUpgradeVerifySupersededReason, time.Now().UnixMilli())
	if err != nil {
		log.Printf("node upgrade verification supersede failed node_id=%d err=%v", nodeID, err)
		return
	}
	if closed {
		log.Printf("node upgrade verification superseded by targetless rollback node_id=%d", nodeID)
	}
}

// upgradeVerifyFailure 是一条被置 failed 的核验记录的告警素材。
type upgradeVerifyFailure struct {
	NodeID      int64
	NodeName    string
	Kind        string
	Target      string
	FromVersion string
	Observed    string
	// Anchor 是期望哈希的降级形态（F2-12），非空时告警正文点名。
	Anchor string
}

// failDueUpgradeVerifications 把过截止仍 pending 的记录置
// failed（条件翻转，赢家收集素材），节点已删除的记录直接清
// 理。返回本轮新置 failed 的全集，供 sweep 发告警、供测试断言。
func (h *Handler) failDueUpgradeVerifications(now time.Time) []upgradeVerifyFailure {
	if h == nil || h.repo == nil {
		return nil
	}
	due, err := h.repo.ListDueNodeUpgradeVerifications(now.UnixMilli(), 200)
	if err != nil {
		log.Printf("node upgrade verification sweep list failed: %v", err)
		return nil
	}
	failures := make([]upgradeVerifyFailure, 0, len(due))
	for i := range due {
		rec := due[i]
		node, nerr := h.repo.GetNodeByID(rec.NodeID)
		if nerr != nil || node == nil {
			// 节点已删除：核验记录是孤儿，清掉不告警（与掉线告
			// 警的 O8-003 同口径，不发幽灵通知）。
			_ = h.repo.DeleteNodeUpgradeVerification(rec.NodeID)
			continue
		}
		observed := rec.ObservedVersion
		if observed == "" && node.Version.Valid {
			observed = strings.TrimSpace(node.Version.String)
		}
		won, rerr := h.repo.ResolveNodeUpgradeVerification(rec.NodeID, repo.NodeUpgradeVerifyPending, repo.NodeUpgradeVerifyFailed, observed, false, now.UnixMilli())
		if rerr != nil || !won {
			continue
		}
		// F2-10：核验终局（failed）释放 ACK 后执行期占用。
		h.releaseNodeUpgradeOpHold(rec.NodeID)
		failures = append(failures, upgradeVerifyFailure{
			NodeID:      rec.NodeID,
			NodeName:    node.Name,
			Kind:        rec.Kind,
			Target:      rec.TargetVersion,
			FromVersion: rec.FromVersion,
			Observed:    observed,
			Anchor:      rec.SHAAnchor,
		})
	}
	return failures
}

// upgradeVerifyKindLabel 是告警文案中的操作名。
func upgradeVerifyKindLabel(kind string) string {
	if kind == upgradeVerifyKindRollback {
		return "回退"
	}
	return "升级"
}

// upgradeVerifyFailureMessage 组装告警正文：期望与实际双边点名，
// 「从未回连」与「回连了但版本不对」分开表述——两者的处置方向
// 不同（前者查节点存活/网络，后者查换版脚本/自动回滚）。
func upgradeVerifyFailureMessage(f upgradeVerifyFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "节点：%s\n操作：%s（目标版本 %s）\n结果：下发后 %s 内未回连到目标版本",
		f.NodeName, upgradeVerifyKindLabel(f.Kind), f.Target, nodeUpgradeVerifyWindow)
	if f.FromVersion != "" {
		fmt.Fprintf(&b, "\n下发前版本：%s", f.FromVersion)
	}
	if f.Observed != "" {
		fmt.Fprintf(&b, "\n当前自报版本：%s", f.Observed)
	} else {
		b.WriteString("\n当前自报版本：尚未回连")
	}
	if f.Anchor != "" {
		// F2-12：哈希锚降级必须在告警面点名——本次核验只以版
		// 本锚为准，处置方向（先修节点自报格式/等哈希就绪）与
		// 普通未落地不同。
		fmt.Fprintf(&b, "\n期望哈希：未下发（%s），本次仅以版本锚核验", f.Anchor)
	}
	return b.String()
}

// sweepNodeUpgradeVerifications 执行一轮截止扫描并为新 failed
// 记录发告警（复用 notifyTracked 通知面，事件
// node_upgrade_failed；发送失败不回滚 failed 状态——状态是事
// 实，通知只是其广播，与掉线告警的标记语义不同：此处没有第二
// 条提醒通道要靠回滚重试保住，节点列表的未落地标记是持久兜
// 底）。
func (h *Handler) sweepNodeUpgradeVerifications(now time.Time) {
	for _, f := range h.failDueUpgradeVerifications(now) {
		log.Printf("node upgrade verification failed node_id=%d kind=%s target=%s observed=%q", f.NodeID, f.Kind, f.Target, f.Observed)
		h.notifyTracked(notifyEventNodeUpgradeFailed, "节点升级未落地", upgradeVerifyFailureMessage(f), nil)
	}
}

// runNodeUpgradeVerifyLoop 是核验截止扫描的后台循环：启动先扫
// 一轮（面板重启后接管上个进程留下的待核验记录，过期的立即置
// failed 发告警——落库而非内存态的意义正在于此），其后按固定
// 间隔轮扫。
func (h *Handler) runNodeUpgradeVerifyLoop(ctx context.Context) {
	defer h.jobsWG.Done()
	h.sweepNodeUpgradeVerifications(time.Now())
	h.sweepNodePendingUninstalls(time.Now())
	ticker := time.NewTicker(nodeUpgradeVerifySweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.sweepNodeUpgradeVerifications(now)
			// C4-G2：待卸载登记同环收敛（succeeded 删记录收尾、
			// 超时置 unconfirmed），节奏与核验扫描一致即可。
			h.sweepNodePendingUninstalls(now)
		}
	}
}
