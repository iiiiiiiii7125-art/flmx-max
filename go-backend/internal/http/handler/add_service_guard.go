package handler

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// R17-B：面板侧 AddService 差集补发幂等守卫（只改 go-backend）。
//
//
// - 面板侧不存在“按 agent 上报的服务表做差集”的代码：agent 无
// ListServices/GetServices 查询命令（go-gost/x/socket/
// websocket_reporter.go 命令分发表无此类型），面板从不读取
// agent 当前服务表。任务书的“差集补发”假说前提在代码中不存在。
// - 实际存在的“补发”是三处 AddService：
// ① syncForwardServicesWithWarnings（control_plane.go:465）：
// UpdateService 报错即回退 AddService（仅 not-found 文本时先删
// 旧服务）；② fallbackForwardPortToDefaultBind（:518）：默认监
// 听回退；③ rebindForwardServiceOnSelfOccupiedPort（:558）：
// 自占端口重绑。另有重复触达面：redeployLocalNodeRuntime
// （upgrade.go:853）每轮对同一转发推两次（先逐隧道、再逐转发），
// retryFailedRedeploys 最多再推 3 次（1s/2s/4s），而 onNodeOnline、
// 升级重部署、备份导入重布、手动批量重部署等多路入口之间没有跨
// 入口串行化（upgradeMu 只护在线重部署记账 map），同一
// (node, 服务) 可并发走到回退 AddService。
// - 两次补发之间不存在确认等待，只有盲 sleep（150ms / 1s/2s/4s）；
// sendNodeCommand 的 tolerateExists=true 会把 agent 的
// already-exists 吞成 nil，重复 AddService 在面板侧不可见，
// 且此前无任何逐服务下发记录。
//
//
// - 以什么为准：“agent 已确认注册态” = 本面板进程内由 agent 对
// AddService/UpdateService 命令的成功响应建立的确认。agent 仅
// 在注册 + 落盘成功后才回成功（go-gost/x/socket/service.go
// createServices/updateServices；already-exists 经 tolerateExists
// 归一化为成功，错误文本本身就是注册态的确认）。确认集无 TTL、
// 无时间窗条目——绝不用“刚发过就跳过”的纯时间窗代替注册态确认。
// - 等什么确认：同一 (nodeID, 服务名集合) 已有在途 AddService 时，
// 后到的补发不另发，等待在途那次的结果（join）：在途成功 → 跳
// 过（已确认）；在途失败/超时 → 自行补发（fail-closed）。等待
// 有界（addServiceGuardJoinTimeout），超时后先清掉疑似僵死的
// 在途条目再自行补发，并记日志。
// - 否定证据优先：本次同步内刚收到的 UpdateService 失败（无论
// 错误文本是 not-found、超时还是 bind 失败）、显式 DeleteService
// 成功、节点离线，立即作废对应确认条目——新鲜的否定证据永远压
// 过旧的肯定确认。三处回退路径调用本守卫时 freshAbsence 一律为
// 真：刚失败的 UpdateService 本身就是否定证据（失败 = 期望注册
// 态未达成）；仅 not-found 才作废是错的——R3-GATE-F3/R4B6 回归证明
// setup 期成功的确认会让 bind 失败后的补发被跳过、把失败误判为
// 成功。因此 stale 确认绝不可能拦住一次真正需要的补发。
// - 超时/无确认时如何 fail-closed：发送超时/失败/无响应 → 不记
// 确认、不跳过；等待者收到失败结果后自行补发；面板/进程重启 →
// 确认集为空 → 一律补发（agent 的 already-exists 拒绝使重复发送
// 安全，落盘去重属 agent 侧 A 路）。
// - 查-发-核三段式说明：因 agent 无服务表查询命令，本路无法实现
// “补发前向 agent 查实时表”；三段式在本路退化为——查：本次同步
// 的 UpdateService 结果 + 内存确认集；发：守卫放行后才发；核：
// AddService/UpdateService 成功响应即核（记确认）+ 下一次同步
// 的 UpdateService 成功再次核。须 agent 侧新增 ListServices 命
// 令才能做真查询，此为未实现项，见 R17-FIX-B-REPORT。
//
// 每条判定记一行，格式固定，可按服务名 grep 到一次补发的
// 完整判定链：
// r17-addservice-guard: node=<id> decision=<send|sent-ok|sent-failed|
// skip-confirmed|skip-joined> reason=<...> services=[<名>,...] err=<...>

// r17AddServiceLogf 是守卫日志的输出口，默认 fmt.Printf，与
// upgrade.go 重部署路径的 fmt.Printf 口径一致。仅测试可替换为记录器。
var r17AddServiceLogf func(format string, args ...interface{}) = func(format string, args ...interface{}) {
	fmt.Printf(format, args...)
}

// addServiceGuardJoinTimeout 是等待同一 (node, 服务名集合) 在途
// AddService 结果的上界。生产发送经 sendNodeCommand 只有 6s 默认
// 超时，30s 意味着在途条目已僵死，超时后按 fail-closed 自行补发。
const addServiceGuardJoinTimeout = 30 * time.Second

type addServiceGuardKey struct {
	nodeID int64
	names  string // 排序后服务名，以 "\x00" 连接
}

type addServiceGuardCall struct {
	done chan struct{}
	err  error
}

// extractAddServiceNames 从 AddService/UpdateService 载荷中提取去重
// 后的服务名（排序、去空、去重）。取不到名字时返回空，调用方须
// fail-closed（无法去重则直接发）。
func extractAddServiceNames(services []map[string]interface{}) []string {
	seen := make(map[string]struct{}, len(services))
	names := make([]string, 0, len(services))
	for _, svc := range services {
		raw, _ := svc["name"].(string)
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (h *Handler) addSvcGuardInitLocked() {
	if h.addSvcInFlight == nil {
		h.addSvcInFlight = make(map[addServiceGuardKey]*addServiceGuardCall)
	}
	if h.addSvcConfirmed == nil {
		h.addSvcConfirmed = make(map[int64]map[string]struct{})
	}
}

func (h *Handler) addSvcGuardConfirmedLocked(nodeID int64, names []string) bool {
	confirmed, ok := h.addSvcConfirmed[nodeID]
	if !ok {
		return false
	}
	for _, name := range names {
		if _, ok := confirmed[name]; !ok {
			return false
		}
	}
	return true
}

// noteAddServiceConfirmed 把 agent 成功响应确认的服务名记入确认集。
// 只由 agent 成功响应调用（guardedAddService 成功、UpdateService 成功）。
func (h *Handler) noteAddServiceConfirmed(nodeID int64, names []string) {
	if h == nil || nodeID <= 0 || len(names) == 0 {
		return
	}
	h.addSvcMu.Lock()
	defer h.addSvcMu.Unlock()
	h.addSvcGuardInitLocked()
	confirmed := h.addSvcConfirmed[nodeID]
	if confirmed == nil {
		confirmed = make(map[string]struct{}, len(names))
		h.addSvcConfirmed[nodeID] = confirmed
	}
	for _, name := range names {
		confirmed[name] = struct{}{}
	}
}

// invalidateAddServiceNames 作废指定服务的确认条目（新鲜否定证据：
// UpdateService not-found、DeleteService 成功）。
func (h *Handler) invalidateAddServiceNames(nodeID int64, names []string) {
	if h == nil || nodeID <= 0 || len(names) == 0 {
		return
	}
	h.addSvcMu.Lock()
	defer h.addSvcMu.Unlock()
	confirmed, ok := h.addSvcConfirmed[nodeID]
	if !ok {
		return
	}
	for _, name := range names {
		delete(confirmed, name)
	}
}

// invalidateAddServiceNode 作废整个节点的确认集（节点离线：agent 运
// 行时态不再可信，下次同步经 UpdateService/AddService 成功重建确认）。
// 不碰在途条目：在途发送自会以节点离线错误失败且不记确认。
func (h *Handler) invalidateAddServiceNode(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.addSvcMu.Lock()
	defer h.addSvcMu.Unlock()
	delete(h.addSvcConfirmed, nodeID)
}

// addServiceSend 是 AddService 的发送缝：默认走 sendNodeCommand
// （tolerateExists=true，与三处原调用点一致）；addSvcSend 非空时
// 用注入的实现，仅测试使用，生产代码不得设置。
func (h *Handler) addServiceSend(nodeID int64, services []map[string]interface{}) error {
	if h.addSvcSend != nil {
		return h.addSvcSend(nodeID, services)
	}
	_, err := h.sendNodeCommand(nodeID, "AddService", services, true, false)
	return err
}

func logR17AddServiceGuard(nodeID int64, decision, reason string, names []string, err error) {
	errText := "-"
	if err != nil {
		errText = err.Error()
	}
	r17AddServiceLogf("r17-addservice-guard: node=%d decision=%s reason=%s services=[%s] err=%s\n",
		nodeID, decision, reason, strings.Join(names, ","), errText)
}

// guardedAddService 是三处转发 AddService 回退的唯一出口。
// freshAbsence=true 表示调用方本次刚拿到新鲜否定证据（刚失败的
// UpdateService、或刚删过这些服务）：先作废确认再决定，绝不跳过。
// freshAbsence=false 时，已确认注册的服务直接跳过（记 skip-confirmed）。
// 返回 skipped=true 表示本次未发送（已确认或加入在途成功），err=nil。
func (h *Handler) guardedAddService(nodeID int64, services []map[string]interface{}, reason string, freshAbsence bool) (bool, error) {
	names := extractAddServiceNames(services)
	if len(names) == 0 {
		// 无法识别服务名则无法去重：fail-closed，直接发。
		logR17AddServiceGuard(nodeID, "send", reason+"-noname-fail-closed", names, nil)
		return false, h.addServiceSend(nodeID, services)
	}
	if freshAbsence {
		h.invalidateAddServiceNames(nodeID, names)
	}
	key := addServiceGuardKey{nodeID: nodeID, names: strings.Join(names, "\x00")}

	for {
		h.addSvcMu.Lock()
		h.addSvcGuardInitLocked()
		if !freshAbsence && h.addSvcGuardConfirmedLocked(nodeID, names) {
			h.addSvcMu.Unlock()
			logR17AddServiceGuard(nodeID, "skip-confirmed", reason, names, nil)
			return true, nil
		}
		if call, ok := h.addSvcInFlight[key]; ok {
			h.addSvcMu.Unlock()
			select {
			case <-call.done:
				if call.err == nil {
					logR17AddServiceGuard(nodeID, "skip-joined", reason, names, nil)
					return true, nil
				}
				// 在途那次失败：fail-closed，重新循环（可能加入更新
				// 的在途调用，或自己注册发送）。
				logR17AddServiceGuard(nodeID, "send", reason+"-join-failed", names, call.err)
				continue
			case <-time.After(addServiceGuardJoinTimeout):
				// 在途条目疑似僵死：先清掉它再重新循环自行补发。
				h.addSvcMu.Lock()
				if cur, ok := h.addSvcInFlight[key]; ok && cur == call {
					delete(h.addSvcInFlight, key)
				}
				h.addSvcMu.Unlock()
				logR17AddServiceGuard(nodeID, "send", reason+"-join-timeout", names, nil)
				continue
			}
		}
		call := &addServiceGuardCall{done: make(chan struct{})}
		h.addSvcInFlight[key] = call
		h.addSvcMu.Unlock()

		logR17AddServiceGuard(nodeID, "send", reason, names, nil)
		err := h.addServiceSend(nodeID, services)
		h.addSvcMu.Lock()
		delete(h.addSvcInFlight, key)
		call.err = err
		close(call.done)
		if err == nil {
			confirmed := h.addSvcConfirmed[nodeID]
			if confirmed == nil {
				confirmed = make(map[string]struct{}, len(names))
				h.addSvcConfirmed[nodeID] = confirmed
			}
			for _, name := range names {
				confirmed[name] = struct{}{}
			}
		}
		h.addSvcMu.Unlock()
		if err == nil {
			logR17AddServiceGuard(nodeID, "sent-ok", reason, names, nil)
			return false, nil
		}
		// 发送失败不记确认：下一次仍补发（fail-closed）。
		logR17AddServiceGuard(nodeID, "sent-failed", reason, names, err)
		return false, err
	}
}
