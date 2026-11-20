package handler

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

type relayView struct {
	ID            int64               `json:"id"`
	InboundID     int64               `json:"inboundId"`
	ForwardID     int64               `json:"forwardId"`
	Name          string              `json:"name"`
	ShareHost     string              `json:"shareHost"`
	Managed       bool                `json:"managed"`
	Mode          string              `json:"mode"` // "port" or "tunnel"
	ForwardName   string              `json:"forwardName"`
	ForwardStatus int                 `json:"forwardStatus"`
	ForwardExists bool                `json:"forwardExists"`
	TunnelID      int64               `json:"tunnelId"`
	TunnelName    string              `json:"tunnelName"`
	RemoteAddr    string              `json:"remoteAddr"`
	Entries       []repo.ForwardEntry `json:"entries"`
	CreatedTime   int64               `json:"createdTime"`
	Latency       *relayLatency       `json:"latency,omitempty"`
}

type shareEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "direct" or "relay"
	Host string `json:"host"`
	Port int    `json:"port"`
	URI  string `json:"uri"`
	// Surge is the Surge [Proxy] line for the entry.
	Surge string `json:"surge"`
}

func relayMode(tunnelType int) string {
	if tunnelType == 2 {
		return "tunnel"
	}
	return "port"
}

func isLoopbackListen(listen string) bool {
	ip := net.ParseIP(listen)
	return ip != nil && ip.IsLoopback()
}

func isWildcardListen(listen string) bool {
	ip := net.ParseIP(listen)
	return ip != nil && ip.IsUnspecified()
}

// ---- relays ----

func (h *Handler) relayViews(inboundID int64) ([]relayView, error) {
	rows, err := h.repo.ListInboundRelays(inboundID)
	if err != nil {
		return nil, err
	}
	out := make([]relayView, 0, len(rows))
	for _, rw := range rows {
		v := relayView{
			ID:            rw.ID,
			InboundID:     rw.InboundID,
			ForwardID:     rw.ForwardID,
			Name:          rw.Name,
			ShareHost:     rw.ShareHost,
			Managed:       rw.Managed == 1,
			Mode:          relayMode(rw.TunnelType),
			ForwardName:   rw.ForwardName,
			ForwardStatus: rw.ForwardStatus,
			ForwardExists: rw.ForwardExists,
			TunnelID:      rw.TunnelID,
			TunnelName:    rw.TunnelName,
			RemoteAddr:    rw.RemoteAddr,
			Entries:       []repo.ForwardEntry{},
			CreatedTime:   rw.CreatedTime,
			Latency:       h.relayLatencyView(rw.ID),
		}
		if rw.ForwardExists {
			if entries, err := h.repo.ForwardEntries(rw.ForwardID, rw.TunnelID); err == nil && entries != nil {
				v.Entries = entries
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (h *Handler) inboundRelayList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		InboundID int64 `json:"inboundId"`
	}
	_ = decodeJSON(r.Body, &req)
	views, err := h.relayViews(req.InboundID)
	if err != nil {
		respondInternalError(w, "inboundRelayList", err)
		return
	}
	response.WriteJSON(w, response.OK(views))
}

// relayTarget decides where the relay forward dials: the inbound's local
// address when a tunnel exits on the landing node itself, otherwise the
// landing node's public address (port forwards, and tunnels exiting on
// another node such as an IX hop in front of the landing node).
// relayValidationError 是中转落地校验的定型失败：文案由请求态
// （出口集／入站监听地址）唯一确定，不含仓储原文。调用方以
// errors.As 分流后原样回执（Msg 字段访问），仓储故障仍走通用
// 文案。R2-A01：TestR1B3／TestR2B9 的定型恢复。
type relayValidationError struct {
	msg string
}

func (e *relayValidationError) Error() string { return e.msg }

// respondRelayValidationError 是中转落地校验的调用点分流：定型
// 校验失败原样回执、仓储故障走通用文案。门禁口径：msg 字段访问
// 天然沉默，与 kernelVersionGateError 的 errors.As 分流同形。
func respondRelayValidationError(w http.ResponseWriter, op string, err error) {
	var rve *relayValidationError
	if errors.As(err, &rve) {
		response.WriteJSON(w, response.ErrDefault(rve.msg))
		return
	}
	respondInternalError(w, op, err)
}

// relayDeleteError 是中转删除的定型失败（R3-GEN-02②）：文案含
// 「可强制删除」的行动出路，由 removeRelay 在非强制删除失败时
// 构造，不含仓储原文（内层仓储错在源头已分流：标记错原样上抛走
// 泛化、其余失败细节经 %v 拼入本文案——与基线 removeRelay 的
// fmt.Errorf 包装同文本）。inboundRelayDelete 以 errors.As 分流
// 后原样回执（msg 字段访问），与 relayValidationError 同形。
type relayDeleteError struct {
	msg string
}

func (e *relayDeleteError) Error() string { return e.msg }

// respondRelayDeleteErrorWithNote 是 inboundRelayDelete 的回执
// 分流：定型删除失败以 ErrDefault 原样回（基线码口径），note
// （T-001 白名单补偿刷新失败点名）非空时以「；」尾随——与
// respondInternalErrorWithNote 在泛化支的尾随同形制；其余失败
// （含已标记仓储错）走 respondInternalErrorWithNote 泛化＋note。
func respondRelayDeleteErrorWithNote(w http.ResponseWriter, op string, err error, note string) {
	var rde *relayDeleteError
	if errors.As(err, &rde) {
		msg := rde.msg
		if note != "" {
			msg += "；" + note
		}
		response.WriteJSON(w, response.ErrDefault(msg))
		return
	}
	respondInternalErrorWithNote(w, op, err, note)
}

// relayCreateError 是 createRelayForward 的定型回执失败
// （R3-GEN-01）：由 createRelayForward 在每个返回点分类构造——
// 配额/占用哨兵带其既有分流码（与 forwardCreate 对同一仓储哨兵的
// 契约一致：配额 ErrDefault、占用 -2），其余定型校验与数据面同步
// 错为 ErrDefault。已标记仓储错不进本型（relayCreateErr 原样上
// 抛），回执经 respondRelayCreateError 以 msg 字段回执、余者泛化
// ——管理员面 raw-error 门对可能携带仓储原文的 err.Error() 呈现
// 无守卫豁免（其 Synthetic 的 pubMarkRepo 明示标记值直出即违规），
// 故本面不走「守卫＋原文」形态、只走源头定型。
type relayCreateError struct {
	code int
	msg  string
}

func (e *relayCreateError) Error() string { return e.msg }

// relayCreateErr 是 createRelayForward 返回点的分类器：已标记仓
// 储错原样上抛（回执泛化），其余失败（定型校验、数据面同步错）
// 拼入 relayCreateError 由回执原样回。
func relayCreateErr(err error) error {
	if err == nil || isRepoError(err) {
		return err
	}
	return &relayCreateError{code: -1, msg: err.Error()}
}

// respondRelayCreateError 是 inboundRelayCreate 的回执分流：定型
// 失败按其 code 以 msg 字段原样回；其余（含已标记仓储错）走
// respondInternalError 泛化（fail-closed：未定型的未知失败不直
// 出原文）。
func respondRelayCreateError(w http.ResponseWriter, op string, err error) {
	var rce *relayCreateError
	if errors.As(err, &rce) {
		response.WriteJSON(w, response.Err(rce.code, rce.msg))
		return
	}
	respondInternalError(w, op, err)
}

// validateRelayLandingExitSet enforces relayTarget's landing rule
// against an explicit exit set: an inbound whose node is among the exits
// requires that node to be the tunnel's only exit. tunnelUpdate 复跑此
// 判定（新出口集尚未落库，不能走 relayTarget 的 DB 读法）——X-P2-4。
func validateRelayLandingExitSet(row *model.NodeInbound, exits []int64) error {
	for _, id := range exits {
		if id == row.NodeID && len(exits) > 1 {
			return &relayValidationError{msg: "出口为落地节点的隧道只能有这一个出口节点"}
		}
	}
	return nil
}

func (h *Handler) relayTarget(row *model.NodeInbound, tunnel *tunnelRecord) (string, error) {
	if tunnel.Type == 2 {
		exits, err := h.repo.TunnelExitNodeIDs(tunnel.ID)
		if err != nil {
			// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
			return "", markRepoError(err)
		}
		return h.relayTargetWithExits(row, tunnel, exits)
	}
	return h.relayTargetWithExits(row, tunnel, nil)
}

// relayTargetWithExits is relayTarget against an explicit exit set, for
// callers whose tunnel state is not (yet) the stored one — R2X-009:
// tunnelUpdate 预演待提交的出口集时不能走 DB 读法（与
// validateRelayLandingExitSet 的显式出口集变体同理由）。
func (h *Handler) relayTargetWithExits(row *model.NodeInbound, tunnel *tunnelRecord, exits []int64) (string, error) {
	port := strconv.Itoa(row.Port)
	if tunnel.Type == 2 {
		if err := validateRelayLandingExitSet(row, exits); err != nil {
			return "", err
		}
		onLanding := false
		for _, id := range exits {
			if id == row.NodeID {
				onLanding = true
			}
		}
		if onLanding {
			host := row.Listen
			if isWildcardListen(host) {
				host = "127.0.0.1"
			}
			// R35-FIX-F02：与最终分支同口径——监听地址可含 :port 时
			// 先剥离，残值仍畸形则拒收（空值沿旧口径原样进 JoinHostPort）。
			host = stripShareHostPort(host)
			if host != "" && !isBareDialHost(host) {
				return "", &relayValidationError{msg: fmt.Sprintf("入站「%s」的监听地址不正确", row.Name)}
			}
			return net.JoinHostPort(host, port), nil
		}
	}

	if isLoopbackListen(row.Listen) {
		return "", &relayValidationError{msg: "入站只监听本机，只能通过出口为落地节点的隧道转发对接"}
	}
	host := row.Listen
	if isWildcardListen(host) {
		node, err := h.repo.GetNodeByID(row.NodeID)
		if err != nil || node == nil {
			// R2-A01：GetNodeByID 错与行缺失同归定型文案（仓储原文
			// 不进文案，见 relayValidationError）。
			return "", &relayValidationError{msg: "落地节点不存在"}
		}
		host = strings.TrimSpace(node.ServerIP)
		if v4 := strings.TrimSpace(node.ServerIPV4.String); host == "" && v4 != "" {
			host = v4
		}
		// O3-002：IPv4 为空时回退尝试 IPv6，避免纯 IPv6 节点无法做端口转发
		if v6 := strings.TrimSpace(node.ServerIPV6.String); host == "" && v6 != "" {
			host = v6
		}
	}
	if host == "" {
		return "", &relayValidationError{msg: "落地节点没有可用地址"}
	}
	// R28-FIX-F01：ServerIP 可含 :port（写侧 IsValidNodeAddress 显式
	// 放行）；进 JoinHostPort 前须剥离，否则产出不可拨号的
	// [IP:port]:port。stripShareHostPort 与订阅直连面（R25-X-01）
	// 同口径：SplitHostPort 成功取 host，失败原样去括号。
	// R35-FIX-F02：单次剥离对双端口值是 no-op，残值仍含 ":port:port"；
	// 须拒收，不产出 "[IP:port]:port" 畸形 RemoteAddr。
	host = stripShareHostPort(host)
	if !isBareDialHost(host) {
		return "", &relayValidationError{msg: fmt.Sprintf("入站「%s」的落地节点地址不正确", row.Name)}
	}
	return net.JoinHostPort(host, port), nil
}

// shadowTLSRelayUDPWarningText is the warning shown when a ShadowTLS-
// fronted Shadowsocks inbound is used as a relay/forward landing. The
// public face of such an inbound is a shadowtls v3 inbound (TCP-only
// TLS handshake + detour) and its Shadowsocks backend only listens on
// 127.0.0.1 (see singboxShadowTLSFront and Inbound.Network), so UDP
// sent to the public port is silently dropped: TCP relaying works, UDP
// traffic such as games never reaches the exit. The panel warns instead
// of opening a public UDP bypass for the backend — that would expose an
// undisguised Shadowsocks face on the camouflage port.
func shadowTLSRelayUDPWarningText(row *model.NodeInbound) string {
	return fmt.Sprintf("入站「%s」是 ShadowTLS 前置的 Shadowsocks 入站：公网口只接受 TCP，UDP 会被静默丢弃；经此入站转发的 UDP 流量（如游戏）无法到达出口，请改用不带 ShadowTLS 的 SS2022 入站", row.Name)
}

// shadowTLSShadowsocksInboundWarning reports whether a stored inbound
// row is a ShadowTLS-fronted Shadowsocks inbound (the UDP-drop shape
// above). A row whose settings cannot be parsed is not such an inbound
// for this purpose — it is already handled by the inbound validation
// paths, and a warning lookup must never fail a sync (fail-open).
func shadowTLSShadowsocksInboundWarning(row *model.NodeInbound) (string, bool) {
	if row == nil {
		return "", false
	}
	in, err := inboundFromModel(*row)
	if err != nil {
		return "", false
	}
	if in.Protocol != kernel.ProtocolShadowsocks || in.Security.Type != kernel.SecurityShadowTLS {
		return "", false
	}
	return shadowTLSRelayUDPWarningText(row), true
}

// shadowTLSUDPWarningsForForward returns the ShadowTLS UDP-drop
// warning for a forward that reaches a ShadowTLS-fronted Shadowsocks
// inbound, through either reference shape:
//   - a managed relay landing on the inbound (the DB-linked shape), or
//   - a plain forward whose RemoteAddr dials the inbound's public
//     host:port directly (matched on port plus the landing node's
//     addresses, its concrete listen IP, or loopback when the forward
//     enters on the landing node itself).
//
// Either way the forward's UDP leg is forwarded as raw datagrams to a
// port with no UDP socket, so it is silently dropped. A domain-form
// RemoteAddr is deliberately not matched (no DNS resolution in the
// sync path); the relay shape above is exact and covers the panel's
// own managed references. Lookups are advisory: a repository failure
// only logs and yields no warning, so the sync itself is never failed
// by this check.
func (h *Handler) shadowTLSUDPWarningsForForward(forward *forwardRecord) []string {
	if h == nil || forward == nil {
		return nil
	}
	seen := make(map[int64]struct{})
	var warnings []string
	addInbound := func(row *model.NodeInbound) {
		if row == nil {
			return
		}
		if _, ok := seen[row.ID]; ok {
			return
		}
		seen[row.ID] = struct{}{}
		if warning, ok := shadowTLSShadowsocksInboundWarning(row); ok {
			warnings = append(warnings, warning)
		}
	}
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		log.Printf("shadowtls relay warning: list relays failed: %v", err)
	} else {
		for _, rel := range rels {
			if rel.ForwardID != forward.ID || rel.Managed != 1 {
				continue
			}
			row, err := h.repo.GetNodeInbound(rel.InboundID)
			if err != nil || row == nil {
				continue
			}
			addInbound(row)
		}
	}
	shadowInbounds, err := h.shadowTLSShadowsocksInbounds()
	if err != nil {
		log.Printf("shadowtls relay warning: list inbounds failed: %v", err)
		return warnings
	}
	if len(shadowInbounds) == 0 {
		return warnings
	}
	var entryNodeIDs map[int64]bool
	nodes := make(map[int64]*model.Node)
	for _, target := range splitRemoteTargets(forward.RemoteAddr) {
		for i := range shadowInbounds {
			in := &shadowInbounds[i]
			if _, port, err := parseTargetAddress(target); err != nil || port != in.Port {
				continue
			}
			node, ok := nodes[in.NodeID]
			if !ok {
				var nodeErr error
				node, nodeErr = h.repo.GetNodeByID(in.NodeID)
				if nodeErr != nil {
					// 读失败只丢地址匹配（Listen 面仍可判定），警告
					// 本身是建议性的，不因此阻断同步。
					log.Printf("shadowtls relay warning: get node %d failed: %v", in.NodeID, nodeErr)
					node = nil
				}
				nodes[in.NodeID] = node
			}
			if entryNodeIDs == nil {
				entryNodeIDs = make(map[int64]bool)
				if ports, err := h.listForwardPorts(forward.ID); err == nil {
					for _, fp := range ports {
						entryNodeIDs[fp.NodeID] = true
					}
				}
			}
			if targetDialsInbound(target, in, node, entryNodeIDs) {
				addInbound(in)
			}
		}
	}
	return warnings
}

// shadowTLSShadowsocksInbounds returns the stored ShadowTLS-fronted
// Shadowsocks inbound rows (the UDP-drop shape).
func (h *Handler) shadowTLSShadowsocksInbounds() ([]model.NodeInbound, error) {
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	out := make([]model.NodeInbound, 0, len(rows))
	for i := range rows {
		if _, ok := shadowTLSShadowsocksInboundWarning(&rows[i]); ok {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// targetDialsInbound reports whether one RemoteAddr target
// (host:port) dials inbound in's public port. The host must match the
// landing node's addresses, the inbound's concrete listen IP, or be
// loopback with the forward entering on the landing node itself
// (entryNodeIDs); a hostname is never matched (no DNS here).
func targetDialsInbound(target string, in *model.NodeInbound, node *model.Node, entryNodeIDs map[int64]bool) bool {
	host, port, err := parseTargetAddress(target)
	if err != nil || port != in.Port {
		return false
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return entryNodeIDs[in.NodeID]
	}
	if strings.EqualFold(host, "localhost") {
		return entryNodeIDs[in.NodeID]
	}
	if node != nil {
		for _, cand := range []string{strings.TrimSpace(node.ServerIP), strings.TrimSpace(node.ServerIPV4.String), strings.TrimSpace(node.ServerIPV6.String)} {
			if cand != "" && strings.EqualFold(strings.Trim(cand, "[]"), host) {
				return true
			}
		}
	}
	if listen := strings.TrimSpace(in.Listen); listen != "" {
		if ip := net.ParseIP(strings.Trim(listen, "[]")); ip != nil && !ip.IsUnspecified() && !ip.IsLoopback() && strings.EqualFold(strings.Trim(listen, "[]"), host) {
			return true
		}
	}
	return false
}

// shadowTLSRelayWarningsForInbound returns the ShadowTLS UDP-drop
// warning for an inbound that is currently referenced as a landing:
// either by a live managed relay, or by a plain forward dialing its
// public host:port (see targetDialsInbound). An inbound with no live
// reference is not warned: its UDP limitation only bites once
// something points at it, and relay/forward creation re-checks at
// that point.
func (h *Handler) shadowTLSRelayWarningsForInbound(inboundID int64) []string {
	if h == nil || inboundID <= 0 {
		return nil
	}
	row, err := h.repo.GetNodeInbound(inboundID)
	if err != nil || row == nil {
		return nil
	}
	warning, ok := shadowTLSShadowsocksInboundWarning(row)
	if !ok {
		return nil
	}
	views, err := h.repo.ListInboundRelays(inboundID)
	if err != nil {
		log.Printf("shadowtls relay warning: list inbound relays failed: %v", err)
		return nil
	}
	for _, v := range views {
		if v.Managed == 1 && v.ForwardExists {
			return []string{warning}
		}
	}
	ids, err := h.repo.ListForwardIDs()
	if err != nil {
		log.Printf("shadowtls relay warning: list forward ids failed: %v", err)
		return nil
	}
	node, nodeErr := h.repo.GetNodeByID(row.NodeID)
	if nodeErr != nil {
		// 读失败只丢地址匹配（Listen 面仍可判定），警告本身是
		// 建议性的，不因此阻断保存回执。
		log.Printf("shadowtls relay warning: get node %d failed: %v", row.NodeID, nodeErr)
		node = nil
	}
	for _, id := range ids {
		fwd, err := h.getForwardRecord(id)
		if err != nil || fwd == nil {
			continue
		}
		var entryNodeIDs map[int64]bool
		for _, target := range splitRemoteTargets(fwd.RemoteAddr) {
			if _, port, err := parseTargetAddress(target); err != nil || port != row.Port {
				continue
			}
			if entryNodeIDs == nil {
				entryNodeIDs = make(map[int64]bool)
				if ports, err := h.listForwardPorts(fwd.ID); err == nil {
					for _, fp := range ports {
						entryNodeIDs[fp.NodeID] = true
					}
				}
			}
			if targetDialsInbound(target, row, node, entryNodeIDs) {
				return []string{warning}
			}
		}
	}
	return nil
}

// FIX-UDP-FIX5 (R1)：PROXY 发送侧目标能力守卫。转发把 PROXY 协议
// 头打向一个不解析它的入站时，头字节成为目标协议载荷的前缀、TCP
// 握手即断——总验实测：ProxyProtocolSend=1 打向 SS 入站时 TCP 0/50，
// sing-box 与 Xray 双内核一致。目标能否接 PROXY 只看三点：协议、
// 对方 AcceptProxyProtocol、对方所在内核——sing-box 渲染根本不带
// 任何 PROXY 选项（render_singbox.go 全文无 proxy 字段），只有 Xray
// 才在 streamSettings 置 acceptProxyProtocol；SS 入站即使 Xray 置了
// 该选项也不解析头（SS 载荷按 AEAD 认证，头字节即认证失败）。
// 守卫形态取警告（Fix-3 的 ShadowTLS 警告通道同口径），不拦截保存：
// 目标解析对直连拨号形是 IP/端口启发式匹配（域名目标无法匹配、无
// DNS 解析面），且用户可能先在转发侧置位、再去入站侧开接收——警告
// 点名不断序；面板自己推导出的 send（托管中转）则不同，见
// relayProxyProtocolSendForInbound：推导出的死配置面板不得写。
//
// proxyProtocolSendTargetCheck 判定一发 PROXY 头能否被 row 接住。
// ok=false 时 cause 说明拦在哪、fix 说明怎么改。行损坏或内核查不
// 到时 fail-open（ok=true）：损坏行走入站校验面，内核未知时不凭空
// 断言——判定只能证否，不能证伪时不误报。
func proxyProtocolSendTargetCheck(row *model.NodeInbound, kernelType string) (ok bool, cause, fix string) {
	if row == nil {
		return true, "", ""
	}
	in, err := inboundFromModel(*row)
	if err != nil {
		return true, "", ""
	}
	if in.Protocol == kernel.ProtocolShadowsocks {
		return false, "Shadowsocks 入站不解析 PROXY 协议头",
			"关闭发送侧的 PROXY 发送（proxyProtocolSend）"
	}
	if !in.AcceptProxyProtocol {
		return false, "未开启获取真实 IP（PROXY protocol）",
			"先在该入站开启获取真实 IP，或关闭发送侧的 PROXY 发送"
	}
	if kernelType != "" && kernel.CanonicalKernelType(kernelType) != kernel.TypeXray {
		return false, fmt.Sprintf("所在内核是 %s，不支持 PROXY 协议接收", kernelType),
			"将该入站所在内核切到 Xray，或关闭发送侧的 PROXY 发送"
	}
	return true, "", ""
}

// kernelTypeForInbound 返回入站所在内核的登记类型；KernelID 缺失
// 或读失败时返回 ""（调用方按未知处理，见上 fail-open）。
func (h *Handler) kernelTypeForInbound(row *model.NodeInbound) string {
	if h == nil || row == nil || row.KernelID <= 0 {
		return ""
	}
	k, err := h.repo.GetNodeKernel(row.KernelID)
	if err != nil || k == nil {
		return ""
	}
	return k.KernelType
}

// proxyProtocolSendWarningsForForward 返回一条转发上 PROXY 发送侧
// 的守卫警告：send>0 且目标可解析到接不住 PROXY 头的入站时点名。
// 目标解析复用 ShadowTLS 警告的两形（托管中转行精确关联；直连拨
// 号按 targetDialsInbound 的端口＋落地节点地址匹配），域名目标不
// 匹配、无引用不警告。仓储读失败只记日志、不阻断同步（建议性检查）。
func (h *Handler) proxyProtocolSendWarningsForForward(forward *forwardRecord) []string {
	if h == nil || forward == nil {
		return nil
	}
	_, send := normalizeForwardProxyProtocol(forward.ProxyProtocol, forward.ProxyProtocolReceive, forward.ProxyProtocolSend)
	if send <= 0 {
		return nil
	}
	seen := make(map[int64]struct{})
	var warnings []string
	addInbound := func(row *model.NodeInbound) {
		if row == nil {
			return
		}
		if _, ok := seen[row.ID]; ok {
			return
		}
		seen[row.ID] = struct{}{}
		if ok, cause, fix := proxyProtocolSendTargetCheck(row, h.kernelTypeForInbound(row)); ok {
			return
		} else {
			warnings = append(warnings, fmt.Sprintf("转发「%s」开启了 PROXY 发送，但目标入站「%s」%s，TCP 连接会断开；请%s", forward.Name, row.Name, cause, fix))
		}
	}
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		log.Printf("proxy-send guard: list relays failed: %v", err)
	} else {
		for _, rel := range rels {
			if rel.ForwardID != forward.ID || rel.Managed != 1 {
				continue
			}
			row, err := h.repo.GetNodeInbound(rel.InboundID)
			if err != nil || row == nil {
				continue
			}
			addInbound(row)
		}
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		log.Printf("proxy-send guard: list inbounds failed: %v", err)
		return warnings
	}
	if len(rows) == 0 {
		return warnings
	}
	nodes := make(map[int64]*model.Node)
	var entryNodeIDs map[int64]bool
	for _, target := range splitRemoteTargets(forward.RemoteAddr) {
		for i := range rows {
			in := &rows[i]
			if _, port, err := parseTargetAddress(target); err != nil || port != in.Port {
				continue
			}
			node, ok := nodes[in.NodeID]
			if !ok {
				var nodeErr error
				node, nodeErr = h.repo.GetNodeByID(in.NodeID)
				if nodeErr != nil {
					log.Printf("proxy-send guard: get node %d failed: %v", in.NodeID, nodeErr)
					node = nil
				}
				nodes[in.NodeID] = node
			}
			if entryNodeIDs == nil {
				entryNodeIDs = make(map[int64]bool)
				if ports, err := h.listForwardPorts(forward.ID); err == nil {
					for _, fp := range ports {
						entryNodeIDs[fp.NodeID] = true
					}
				}
			}
			if targetDialsInbound(target, in, node, entryNodeIDs) {
				addInbound(in)
			}
		}
	}
	return warnings
}

// relayProxyProtocolSendForInbound 决定托管中转转发可向 row 发送的
// PROXY 版本：对方接得住才发 relayProxyProtocolVersion，否则发 0
// 并附警告。用户在转发上显式置 send 时面板只警告不改值（见上），
// 但这里的 send 是面板按 AcceptProxyProtocol 推导出的——推导出
// 已知必断的配置再写进去，等于面板亲手造死中转，故拦住不写、只
// 警告；真实 IP 经此入站无法传递是警告的另一半事实。Accept 未开
// 时 send=0、无警告（常态）。
func (h *Handler) relayProxyProtocolSendForInbound(row *model.NodeInbound) (int, string) {
	if row == nil || row.AcceptProxyProtocol != 1 {
		return 0, ""
	}
	if ok, cause, fix := proxyProtocolSendTargetCheck(row, h.kernelTypeForInbound(row)); !ok {
		return 0, fmt.Sprintf("入站「%s」已开启获取真实 IP，但%s：中转转发不会向其发送 PROXY 协议头（否则 TCP 会断开），真实 IP 无法经此入站传递；请%s", row.Name, cause, fix)
	}
	return relayProxyProtocolVersion, ""
}

// validateTunnelRelayExits re-runs the landing-exit rule for every
// inbound relayed through a tunnel against a prospective exit set
// (X-P2-4 ①: tunnelUpdate 校验段——新出口集违反「出口为落地必须单出口」
// 时拒绝保存，否则转发继续拨回环地址静默错向)。
func (h *Handler) validateTunnelRelayExits(tunnelID int64, exits []int64) error {
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return markRepoError(err)
	}
	if len(forwards) == 0 {
		return nil
	}
	forwardIDs := make(map[int64]struct{}, len(forwards))
	for _, fwd := range forwards {
		forwardIDs[fwd.ID] = struct{}{}
	}
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return markRepoError(err)
	}
	for _, rel := range rels {
		if _, ok := forwardIDs[rel.ForwardID]; !ok {
			continue
		}
		row, err := h.repo.GetNodeInbound(rel.InboundID)
		if err != nil || row == nil {
			continue
		}
		if err := validateRelayLandingExitSet(row, exits); err != nil {
			return err
		}
	}
	return nil
}

// recomputedRelayRemoteAddr returns the RemoteAddr a managed relay
// forward must dial when attached to the given tunnel, recomputed from
// that tunnel's exit set via relayTarget (X-P2-4 ②: 换隧道/迁移时只改
// 端口不重算会让落地型转发继续拨旧地址）。changed=false 表示非托管
// 中转转发或现址已与目标隧道口径一致。
func (h *Handler) recomputedRelayRemoteAddr(forward *forwardRecord, tunnel *tunnelRecord) (string, bool, error) {
	if tunnel.Type == 2 {
		exits, err := h.repo.TunnelExitNodeIDs(tunnel.ID)
		if err != nil {
			return "", false, err
		}
		return h.recomputedRelayRemoteAddrWithExits(forward, tunnel, exits)
	}
	return h.recomputedRelayRemoteAddrWithExits(forward, tunnel, nil)
}

// recomputedRelayRemoteAddrWithExits is recomputedRelayRemoteAddr against
// an explicit exit set (R2X-009 预演变体，见 relayTargetWithExits)。
func (h *Handler) recomputedRelayRemoteAddrWithExits(forward *forwardRecord, tunnel *tunnelRecord, exits []int64) (string, bool, error) {
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return "", false, markRepoError(err)
	}
	for _, rel := range rels {
		if rel.ForwardID != forward.ID || rel.Managed != 1 {
			continue
		}
		row, err := h.repo.GetNodeInbound(rel.InboundID)
		if err != nil || row == nil {
			continue
		}
		target, err := h.relayTargetWithExits(row, tunnel, exits)
		if err != nil {
			return "", false, err
		}
		return target, target != forward.RemoteAddr, nil
	}
	return "", false, nil
}

// validateTunnelRelayRemoteAddrComputable pre-flights every managed relay
// of a tunnel against a prospective tunnel state (R2X-009): a migration
// whose RemoteAddr cannot be recomputed (e.g. type 2→1 for a
// loopback-only inbound) must fail here, before anything is stored, so
// no silent stale-address state is left behind.
func (h *Handler) validateTunnelRelayRemoteAddrComputable(tunnelID int64, prospective *tunnelRecord, exits []int64) error {
	forwards, err := h.repo.ListForwardsByTunnel(tunnelID)
	if err != nil {
		// R2-F4-01：仓储直通，标记后由调用方守卫泛化。
		return markRepoError(err)
	}
	for i := range forwards {
		if _, _, err := h.recomputedRelayRemoteAddrWithExits(&forwards[i], prospective, exits); err != nil {
			return err
		}
	}
	return nil
}

// applyRelayRemoteAddr stores a recomputed RemoteAddr and re-syncs the
// forward's services so the data plane follows immediately.
func (h *Handler) applyRelayRemoteAddr(forward *forwardRecord, newAddr string) error {
	if err := h.repo.UpdateForward(forward.ID, forward.Name, forward.TunnelID, newAddr, forward.Strategy, time.Now().UnixMilli(),
		forward.SpeedID, forward.MaxConn, forward.IPMaxConn, forward.IPSpeedID, 0, forward.ProxyProtocolReceive, forward.ProxyProtocolSend); err != nil {
		return err
	}
	updated, err := h.getForwardRecord(forward.ID)
	if err != nil || updated == nil {
		return err
	}
	return h.syncForwardServices(updated, "UpdateService", true)
}

// resyncManagedRelayRemoteAddrsForTunnel recomputes the RemoteAddr of
// every managed relay forward of a tunnel from the tunnel's stored state
// (R2X-009: 隧道提交落库后调用，与白名单刷新同位置）。变更则落库并
// 重下发；失败逐条汇入返回的 warnings，不静默。
func (h *Handler) resyncManagedRelayRemoteAddrsForTunnel(tunnelID int64) []string {
	tunnel, err := h.getTunnelRecord(tunnelID)
	if err != nil || tunnel == nil {
		return []string{fmt.Sprintf("中转目标地址重算失败: 隧道不存在 (%v)", err)}
	}
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return []string{fmt.Sprintf("中转目标地址重算失败: %v", err)}
	}
	var warnings []string
	for i := range forwards {
		fwd := &forwards[i]
		newAddr, changed, rerr := h.recomputedRelayRemoteAddr(fwd, tunnel)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("中转「%s」目标地址重算失败: %v", fwd.Name, rerr))
			continue
		}
		if !changed {
			continue
		}
		if err := h.applyRelayRemoteAddr(fwd, newAddr); err != nil {
			warnings = append(warnings, fmt.Sprintf("中转「%s」目标地址同步失败: %v", fwd.Name, err))
		}
	}
	return warnings
}

// resyncManagedRelayRemoteAddrForForward is the single-forward member
// of the resync funnel (R3X-002): one forward's managed relay is
// recomputed from its (newly stored) tunnel with the same
// recomputedRelayRemoteAddr + applyRelayRemoteAddr steps the batch
// tunnel change uses (:4288 口径), for callers — forwardUpdate after a
// tunnel switch — whose commit has already landed. A non-managed
// forward is a no-op (recomputedRelayRemoteAddr reports no change);
// failures are returned as warnings, never silent.
func (h *Handler) resyncManagedRelayRemoteAddrForForward(forwardID int64) []string {
	fwd, err := h.getForwardRecord(forwardID)
	if err != nil || fwd == nil {
		return []string{fmt.Sprintf("中转目标地址重算失败: 转发不存在 (%v)", err)}
	}
	tunnel, err := h.getTunnelRecord(fwd.TunnelID)
	if err != nil || tunnel == nil {
		return []string{fmt.Sprintf("中转目标地址重算失败: 隧道不存在 (%v)", err)}
	}
	newAddr, changed, rerr := h.recomputedRelayRemoteAddr(fwd, tunnel)
	if rerr != nil {
		return []string{fmt.Sprintf("中转「%s」目标地址重算失败: %v", fwd.Name, rerr)}
	}
	if !changed {
		return nil
	}
	if err := h.applyRelayRemoteAddr(fwd, newAddr); err != nil {
		return []string{fmt.Sprintf("中转「%s」目标地址同步失败: %v", fwd.Name, err)}
	}
	return nil
}

// resyncManagedRelayRemoteAddrsForInbound is the inbound-side twin: every
// managed relay of the inbound is recomputed from its own tunnel's stored
// state (R2X-009: 入站 Port/Listen 变更、落地节点改址后调用）。
func (h *Handler) resyncManagedRelayRemoteAddrsForInbound(inboundID int64) []string {
	relays, err := h.repo.ListInboundRelays(inboundID)
	if err != nil {
		return []string{fmt.Sprintf("中转目标地址重算失败: %v", err)}
	}
	var warnings []string
	for _, rel := range relays {
		if !rel.ForwardExists || rel.Managed != 1 {
			continue
		}
		fwd, err := h.getForwardRecord(rel.ForwardID)
		if err != nil || fwd == nil {
			continue
		}
		tunnel, err := h.getTunnelRecord(fwd.TunnelID)
		if err != nil || tunnel == nil {
			continue
		}
		newAddr, changed, rerr := h.recomputedRelayRemoteAddr(fwd, tunnel)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("中转「%s」目标地址重算失败: %v", fwd.Name, rerr))
			continue
		}
		if !changed {
			continue
		}
		if err := h.applyRelayRemoteAddr(fwd, newAddr); err != nil {
			warnings = append(warnings, fmt.Sprintf("中转「%s」目标地址同步失败: %v", fwd.Name, err))
		}
	}
	return warnings
}

func (h *Handler) inboundRelayCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		InboundID int64  `json:"inboundId"`
		TunnelID  int64  `json:"tunnelId"`
		InPort    int    `json:"inPort"`
		Name      string `json:"name"`
		ShareHost string `json:"shareHost"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	userID, _, err := userRoleFromRequest(r)
	if err != nil {
		response.WriteJSON(w, response.Err(401, "无效的token或token已过期"))
		return
	}
	// R2X-004：创建面接入 Idempotency-Key（机制见 idempotency.go）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, userID, "inbound-relay:create")
		defer finish()
		if replayed {
			return
		}
	}
	row, err := h.repo.GetNodeInbound(req.InboundID)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("入站不存在"))
		return
	}
	if row.CompanionOf > 0 {
		response.WriteJSON(w, response.ErrDefault("计量伴生入站由系统管理，不能建中转"))
		return
	}
	tunnel, err := h.getTunnelRecord(req.TunnelID)
	if err != nil || tunnel == nil {
		response.WriteJSON(w, response.ErrDefault("隧道不存在"))
		return
	}
	if tunnel.Status != 1 {
		response.WriteJSON(w, response.ErrDefault("隧道已禁用"))
		return
	}
	remoteAddr, err := h.relayTarget(row, tunnel)
	if err != nil {
		// BOXFIX：落地校验定型文案走既有分流（与 tunnelUpdate 同
		// 口径）——此前直走 respondInternalError 把 relayValidation
		// Error 的定型文案一并泛化，TestInboundRelayValidation
		// Contract 的 loopback 两格因此被吞。
		respondRelayValidationError(w, "inboundRelayCreate", err)
		return
	}

	// W3/IRL-3（后端半）：中转创建去重闸——同一入站经同一隧道、且
	// 入口端口也相同的中转已存在时点名拒绝，不再建第二条。此前本面
	// 无任何查重，前端双击/重复提交（或不带 Idempotency-Key 的直调）
	// 即建出两条同入站同目标中转及两条转发与两份服务。口径：托管
	// （Managed=1）且转发仍存续的中转行，(隧道, 入站, 入口端口) 全
	// 等才算重复。R18-B4-05：去重键与落地地址无关——落地节点的地址
	// 会变（IP 迁移/重解析），旧实现拿转发 RemoteAddr 字符串比对，
	// 地址一变闸就认不出同一条中转；同隧道同入口端口本就只能有一
	// 条转发（forward_port 唯一索引兜底），地址不同不是另一条中转。
	// 同隧道不同出口集（多出口变体走 relayTargetWithExits 的创建面
	// 不在本端点）算出的目标不同、不误杀；同隧道同目标、入口端口不同
	// 是用户要的多条中转，放行。自动选口（InPort<=0）时闸跑在选口
	// 之前、尚无端口可比，故跳过本闸：选口本就排除已用端口，落库又有
	// CreateForwardTx 的逐节点端口锁＋锁内复检＋forward_port 唯一索引
	// 兜底，撞口换口重试，不会与既有中转落到同一端口。查重失败 fail-closed
	// 中止创建（与本仓去重门同口径：查不了就不写）。
	if req.InPort > 0 {
		existingRelays, err := h.repo.ListInboundRelays(row.ID)
		if err != nil {
			respondInternalError(w, "inboundRelayCreate", err)
			return
		}
		for _, rel := range existingRelays {
			if rel.Managed != 1 || !rel.ForwardExists {
				continue
			}
			fwd, ferr := h.getForwardRecord(rel.ForwardID)
			if ferr != nil || fwd == nil {
				continue
			}
			if fwd.TunnelID != tunnel.ID {
				continue
			}
			// 转发的入口端口在 forward_port 行上（多入口节点同口）：
			// 任一行与本次请求的端口相同即算重复。
			ports, perr := h.repo.ListForwardPorts(rel.ForwardID)
			if perr != nil || len(ports) == 0 {
				continue
			}
			for _, p := range ports {
				if p.Port == req.InPort {
					response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("该入站已建有经同一隧道到相同落地目标的中转「%s」，请勿重复创建", rel.Name)))
					return
				}
			}
		}
	}

	tunnelName, _ := h.repo.GetTunnelName(tunnel.ID)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = fmt.Sprintf("%s → %s", firstNonEmpty(tunnelName, "中转"), row.Name)
	}
	if len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("名称不超过 100 字"))
		return
	}
	shareHost := normalizeShareHost(req.ShareHost)
	if len(shareHost) > 255 {
		response.WriteJSON(w, response.ErrDefault("分享地址过长"))
		return
	}
	if shareHost != "" {
		// F-A-01/C-01：中转 ShareHost 与入站 custom 分享地址同标准。
		if err := validateShareHostValue(shareHost); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}

	// FIX-UDP-FIX5 (R1)：ppSend 不再只看 AcceptProxyProtocol==1——
	// 对方接不住 PROXY 头时（SS 入站、或非 Xray 内核）发了即断
	// TCP，见 relayProxyProtocolSendForInbound；接不住时 send 置 0
	// 并把警告带进下方回执，不写死配置。
	ppSend, ppWarning := h.relayProxyProtocolSendForInbound(row)
	forwardID, err := h.createRelayForward(userID, name, tunnel, remoteAddr, req.InPort, ppSend)
	if err != nil {
		// R3-GEN-01：定型分流回执（createRelayForward 已在源头把可
		// 回执失败定型为 relayCreateError、仓储错已标记）——定型按
		// 其码以 msg 字段原样回，余者泛化。此前直走
		// respondInternalError 把整族定型文案全量泛化。
		respondRelayCreateError(w, "inboundRelayCreate: createRelayForward failed", err)
		return
	}
	rel := &model.NodeInboundRelay{
		InboundID:   row.ID,
		ForwardID:   forwardID,
		Name:        name,
		ShareHost:   shareHost,
		Managed:     1,
		CreatedTime: time.Now().UnixMilli(),
	}
	if err := h.repo.CreateInboundRelay(rel); err != nil {
		if fwd, ferr := h.getForwardRecord(forwardID); ferr == nil && fwd != nil {
			_ = h.removeForwardWithServices(fwd)
		}
		respondInternalError(w, "inboundRelayCreate", err)
		return
	}
	// LIVE-215 红 1：createRelayForward 内的首次同步发生在 relay 行
	// 落库前，connlogEntryForwardIDs() 当时算不出该 forward、入口
	// 服务以下发时无 connlogEntry 标记；落库后不重推则标记永不补
	// 发，新建中转的连接日志零行。relay 行落库后立即重推一次，
	// 使入口标记随新建中转一起落地，无需手动重推。nft 隧道无
	// gost 服务可标（其创建面只做 nft reconcile），跳过重推；
	// 总开关关时该 forward 不在入口集内，重推亦无标记可补，跳过。
	var warnings []string
	if isNft, _, nftErr := h.tunnelUsesNftables(tunnel.ID); nftErr != nil {
		warnings = append(warnings, fmt.Sprintf("中转入口标记重推失败：隧道模式读取失败（%v），连接日志暂收不到该中转入口的记录", nftErr))
	} else if !isNft {
		if entryIDs, entryErr := h.connlogEntryForwardIDs(); entryErr != nil {
			warnings = append(warnings, fmt.Sprintf("中转入口标记重推失败：入口集合读取失败（%v），连接日志暂收不到该中转入口的记录", entryErr))
		} else if entryIDs[forwardID] {
			if fwd, ferr := h.getForwardRecord(forwardID); ferr != nil || fwd == nil {
				warnings = append(warnings, "中转入口标记重推失败：转发记录读取失败，连接日志暂收不到该中转入口的记录")
			} else if err := h.syncForwardServices(fwd, "UpdateService", true); err != nil {
				warnings = append(warnings, fmt.Sprintf("中转入口标记重推失败（%v），连接日志暂收不到该中转入口的记录", err))
			}
		}
	}
	// T-001：刷新失败进回执 warning——创建本身已落库成功，但内核
	// 仍按旧名单放行，新中转可能无法连通。
	if err := h.refreshInboundWhitelist(row.ID); err != nil {
		warnings = append(warnings, whitelistRefreshFailureNote([]int64{row.ID})+"，请检查节点内核状态")
	}
	// FIX-UDP-ENTRY (a)：本中转的落地入站若是 ShadowTLS 前置的
	// SS 入站，其公网口 UDP 会被静默丢弃——创建回执须点名（经
	// OKWithWarning 的 warning 字段，前端中转弹窗已逐条 toast）。
	// createRelayForward 内的首次同步走丢 warnings 的薄封装，故
	// 在此显式补算，不能只依赖 syncForwardServicesWithWarnings。
	warnings = append(warnings, h.shadowTLSRelayWarningsForInbound(row.ID)...)
	// FIX-UDP-FIX5 (R1)：接不住 PROXY 头的入站上开 Accept 时，本
	// 中转的 send 已被上文置 0——警告随回执点名（真实 IP 传不过
	// 去），不静默。
	if ppWarning != "" {
		warnings = append(warnings, ppWarning)
	}
	if len(warnings) > 0 {
		response.WriteJSON(w, response.OKWithWarning(map[string]interface{}{"id": rel.ID, "forwardId": forwardID}, strings.Join(warnings, "；")))
		return
	}
	response.WriteJSON(w, response.OK(map[string]interface{}{"id": rel.ID, "forwardId": forwardID}))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// createRelayForward mirrors the admin path of forwardCreate: same port
// validation, persistence and runtime sync, without user-facing quota checks.
func (h *Handler) createRelayForward(userID int64, name string, tunnel *tunnelRecord, remoteAddr string, inPort int, proxyProtocolSend int) (int64, error) {
	// R3-GEN-01：全部可回执失败在返回点经 relayCreateErr 定型
	// （已标记仓储错原样上抛走泛化，定型校验/数据面错入
	// relayCreateError 由回执以 msg 字段原样回）。
	entryNodes, err := h.tunnelEntryNodeIDs(tunnel.ID)
	if err != nil {
		return 0, relayCreateErr(err)
	}
	if len(entryNodes) == 0 {
		return 0, relayCreateErr(errors.New("隧道没有入口节点"))
	}
	isNftTunnel, _, err := h.tunnelUsesNftables(tunnel.ID)
	if err != nil {
		return 0, relayCreateErr(err)
	}
	if isNftTunnel {
		// R18-C3-03：此处无 BindIP 在手（中继创建只定目标），传 ""；
		// 混用族由渲染层 ValidatePlan 兜底。
		if err := h.validateNftablesForwardRequest(tunnel, remoteAddr, entryNodes, ""); err != nil {
			return 0, relayCreateErr(err)
		}
	}
	// PortAlloc Fix B/C：选口＋预检＋探针＋落库＋下发走换口重试循环
	// （与 forwardCreate 的 createForwardWithPortRetry 同口径）。
	// 撞口信号（DB 预检占用 / live 探针命中 / 锁内复检 ErrPortInUse /
	// 下发绑口失败）只对自动口换口重试、显式口原样失败；其余失败
	// （规格错、配额哨兵、仓储错、取行失败、非绑口下发错）立即回。
	auto := inPort <= 0
	tried := make(map[int]bool)
	var lastErr error
	for attempt := 0; attempt < forwardPortRetryAttempts; attempt++ {
		port := inPort
		if auto {
			picked, pickErr := h.pickTunnelPortExcluding(tunnel.ID, tried)
			if pickErr != nil {
				return 0, relayCreateErr(pickErr)
			}
			port = picked
		}
		if port <= 0 || port > 65535 {
			return 0, relayCreateErr(errors.New("没有可用的入口端口"))
		}
		// 预检：规格错立即回（自动口在规格内恒成立）；占用错换口重试。
		// L4-02：节点记录读失败 fail-closed，不得跳过该节点校验。
		precheckFailed := false
		for _, nodeID := range entryNodes {
			node, nodeErr := h.getNodeRecord(nodeID)
			if nodeErr != nil {
				// L4-02：与 forwardCreate/Update 的 validateForwardEntryNodes
				// 同口径 fail-closed——节点记录读失败不得 continue 跳过该
				// 节点的端口规格校验后仍落库（占用面虽有
				// CreateForwardTx 锁内四源复检兜底，规格面无兜底）。
				// getNodeRecord 的错误恒为已标记仓储错（其源头已标），
				// 回执走泛化即正确分类，故此处不经 relayCreateErr 定型。
				return 0, nodeErr
			}
			if err := validateLocalNodePort(node, port); err != nil {
				return 0, relayCreateErr(err)
			}
			if err := h.validateForwardPortAvailability(node, port, 0); err != nil {
				if isRepoError(err) {
					return 0, err
				}
				lastErr = err
				if !auto {
					return 0, relayCreateErr(err)
				}
				tried[port] = true
				precheckFailed = true
				break
			}
		}
		if precheckFailed {
			continue
		}
		// live 探针：只探自动口（与 forwardCreate 同口径）。
		if auto {
			if msg := h.probeForwardEntryPortLive(entryNodes, port); msg != "" {
				lastErr = errors.New(msg)
				tried[port] = true
				continue
			}
		}

		now := time.Now().UnixMilli()
		userName := h.repo.GetUsernameByID(userID)
		if userName == "" {
			userName = "admin"
		}
		forwardID, err := h.repo.CreateForwardTx(userID, userName, name, tunnel.ID, remoteAddr, "fifo", now,
			h.repo.NextIndex("forward"), entryNodes, port, "", nil, 0, 0, nil, 0, 0, proxyProtocolSend)
		if err != nil {
			// R3-GEN-01：配额/占用哨兵在此定型（带与 forwardCreate 对同
			// 一仓储哨兵的既有分流码：配额 -1、占用 -2，msg 取哨兵原文）；
			// 其余仓储失败标记后上抛、回执泛化。
			if errors.Is(err, repo.ErrForwardNumExceeded) || errors.Is(err, repo.ErrUserTunnelForwardNumExceeded) {
				return 0, &relayCreateError{code: -1, msg: err.Error()}
			}
			if errors.Is(err, repo.ErrPortInUse) {
				lastErr = err
				if !auto {
					return 0, &relayCreateError{code: -2, msg: err.Error()}
				}
				tried[port] = true
				continue
			}
			return 0, markRepoError(err)
		}
		created, err := h.getForwardRecord(forwardID)
		if err != nil {
			_ = h.deleteForwardByID(forwardID)
			return 0, relayCreateErr(err)
		}
		if isNftTunnel {
			if err := h.reconcileNftablesNodeByRequest(entryNodes[0]); err != nil {
				// R18-B4-02：同 createForwardWithPortRetry——补偿先清
				// 数据面再删库，残留写进返回错误。
				if compErr := h.compensateFailedForwardCreate(created); compErr != nil {
					err = fmt.Errorf("%w；创建失败补偿清理未完成：%v", err, compErr)
				}
				return 0, relayCreateErr(err)
			}
			return forwardID, nil
		}
		if err := h.syncForwardServices(created, "UpdateService", true); err != nil {
			// R18-B4-02：同上。
			if compErr := h.compensateFailedForwardCreate(created); compErr != nil {
				err = fmt.Errorf("%w；创建失败补偿清理未完成：%v", err, compErr)
			}
			if auto && isBindAddressInUseError(err) {
				lastErr = err
				tried[port] = true
				continue
			}
			return 0, relayCreateErr(err)
		}
		return forwardID, nil
	}
	// 耗尽：末次碰撞错按既有分流回（占用 -2，其余 -1）。
	if errors.Is(lastErr, repo.ErrPortInUse) {
		return 0, &relayCreateError{code: -2, msg: lastErr.Error()}
	}
	return 0, relayCreateErr(lastErr)
}

// removeForwardWithServices mirrors forwardDelete for a forward the panel
// manages on behalf of an inbound.
func (h *Handler) removeForwardWithServices(forward *forwardRecord) error {
	var nftNodeID int64
	if nftMode, entryNodeIDs, err := h.tunnelUsesNftables(forward.TunnelID); err != nil {
		return err
	} else if nftMode {
		// R35-FIX-F01：遗留/竞态混合态下拒绝删除而不是只 reconcile
		// 首节点后删库（gost/realm 服务会残留监听）。
		if err := h.rejectMixedNftForwardState(forward.TunnelID, nftMode, entryNodeIDs); err != nil {
			return err
		}
		if len(entryNodeIDs) > 0 {
			nftNodeID = entryNodeIDs[0]
		}
	} else if err := h.controlForwardServices(forward, "DeleteService", true); err != nil {
		return err
	}
	if err := h.deleteForwardByID(forward.ID); err != nil {
		return err
	}
	if nftNodeID > 0 {
		return h.reconcileNftablesNodeByRequest(nftNodeID)
	}
	return nil
}

// compensateFailedForwardCreate removes a just-created forward whose
// sync failed, clearing already-pushed data-plane services first
// (R18-B4-02): a bare deleteForwardByID leaves live services on nodes
// that already received UpdateService, with no DB record left to clean
// them up. removeForwardWithServices deletes the services first; because
// it aborts on the first hard per-node error, a per-node best-effort
// sweep (mode-aware: gost DeleteService, nft reconcile, realm re-render)
// follows any failure so a node later in port order is not skipped.
// Only then do we fall back to a DB-only delete, logging the residue and
// reporting it so the caller can name the affected nodes in the failure
// receipt. A nil error means the forward is fully gone, services included.
func (h *Handler) compensateFailedForwardCreate(fwd *forwardRecord) error {
	if fwd == nil {
		return errors.New("invalid forward compensation context")
	}
	if err := h.removeForwardWithServices(fwd); err == nil {
		return nil
	} else {
		firstErr := err
		var sweepErrs []string
		if ports, perr := h.listForwardPorts(fwd.ID); perr != nil {
			log.Printf("create compensation: list forward ports failed forward_id=%d err=%v", fwd.ID, perr)
		} else {
			for _, nodeID := range forwardPortNodeIDs(ports) {
				if serr := h.deleteForwardServicesOnNodeBatch(fwd, nodeID); serr != nil {
					sweepErrs = append(sweepErrs, fmt.Sprintf("节点 %s: %v", h.nodeLabel(nodeID), serr))
				}
			}
		}
		if derr := h.deleteForwardByID(fwd.ID); derr != nil {
			return fmt.Errorf("data-plane cleanup failed: %v; per-node sweep: %v; DB delete also failed: %w", firstErr, strings.Join(sweepErrs, "; "), derr)
		}
		if len(sweepErrs) > 0 {
			return fmt.Errorf("data-plane cleanup failed: %v; per-node sweep left residue: %s", firstErr, strings.Join(sweepErrs, "; "))
		}
		return fmt.Errorf("data-plane cleanup failed: %w", firstErr)
	}
}

func (h *Handler) inboundRelayDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID    int64 `json:"id"`
		Force bool  `json:"force"`
	}
	if err := decodeJSON(r.Body, &req); err != nil || req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	rel, err := h.repo.GetInboundRelay(req.ID)
	if err != nil || rel == nil {
		response.WriteJSON(w, response.ErrDefault("中转不存在"))
		return
	}
	warning, err := h.removeRelay(rel, req.Force)
	if err != nil {
		// R5X-004(d)：非 force 路径 removeForwardWithServices 先删
		// 转发行、后做 nft reconcile——reconcile 失败时转发已删
		// （ForwardExists 转假、该 relay 应即刻掉出 allow 计算）而
		// relay 行尚存；DeleteInboundRelay 失败面同理。失败返回前
		// 无条件补刷该入站白名单（按现行 DB 现算，未删部分的刷新
		// 为同值空转）；T-001：补偿刷新失败时回执须点名——否则已删
		// 转发的拨号地址残留在 allow 中而回执只字不提。错误本体按
		// 门禁口径泛化，经 respondInternalErrorWithNote 一并点名。
		refreshNote := ""
		if rerr := h.refreshInboundWhitelist(rel.InboundID); rerr != nil {
			refreshNote = whitelistRefreshFailureNote([]int64{rel.InboundID})
		}
		// R3-GEN-02②：定型删除失败（relayDeleteError）经分流原样
		// 回（含「可强制删除」出路），其余仍泛化；note 两支同尾随。
		// 原注释「错误本体按门禁口径泛化」仅对非定型支成立。
		respondRelayDeleteErrorWithNote(w, "inboundRelayDelete", err, refreshNote)
		return
	}
	// T-001：刷新失败进回执 warning——删除本身已落库成功，但内核
	// 仍按旧名单放行；与 removeRelay 的强制删除残留警告合并发出。
	if err := h.refreshInboundWhitelist(rel.InboundID); err != nil {
		refreshNote := whitelistRefreshFailureNote([]int64{rel.InboundID}) + "，请检查节点内核状态"
		if warning != "" {
			warning += "；" + refreshNote
		} else {
			warning = refreshNote
		}
	}
	if warning != "" {
		response.WriteJSON(w, response.OKWithWarning(nil, warning))
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

// removeRelay deletes a managed relay's forward and the relay row.
// It returns a warning string when the force path deleted the DB rows
// while some data-plane cleanup stayed unconfirmed (R18-B4-03); the
// caller surfaces it in the receipt instead of a bare success.
func (h *Handler) removeRelay(rel *model.NodeInboundRelay, force bool) (string, error) {
	warning := ""
	if rel.Managed == 1 {
		if fwd, err := h.getForwardRecord(rel.ForwardID); err == nil && fwd != nil {
			if err := h.removeForwardWithServices(fwd); err != nil {
				if !force {
					// R3-GEN-02②：仓储标记错原样上抛（回执守卫泛化，
					// 此稀有 DB 故障情形不带强制删除提示）；其余失败
					// 定型为 relayDeleteError 携带「可强制删除」出路。
					if isRepoError(err) {
						return "", err
					}
					return "", &relayDeleteError{msg: fmt.Sprintf("删除中转转发失败: %v（可强制删除）", err)}
				}
				warning = h.forceRemoveRelayForward(fwd)
			}
		}
	}
	if err := h.repo.DeleteInboundRelay(rel.ID); err != nil {
		// R3-GEN-02②：仓储直通先标记，回执点守卫泛化。
		return "", markRepoError(err)
	}
	return warning, nil
}

// forceRemoveRelayForward is the R18-B4-03 force path for a managed
// relay forward whose normal removal failed: delete the DB row, but
// never wipe the retry carrier, and best-effort clean the data plane
// before returning. It returns the residue warning for the receipt
// ("" when everything was confirmed clean).
func (h *Handler) forceRemoveRelayForward(fwd *forwardRecord) string {
	var notes []string
	// 1. Snapshot the entry nodes and ports before the DB row disappears.
	ports, portsErr := h.listForwardPorts(fwd.ID)
	if portsErr != nil {
		log.Printf("relay force delete: list forward ports failed forward_id=%d err=%v", fwd.ID, portsErr)
	}
	// 2. nft: register the pending-delete ledger BEFORE the DB delete
	// and keep it through the cascade, so the 60s retry loop can still
	// converge the DNAT rule after the forward row is gone.
	keepLedger := false
	if nftMode, nftNodes, modeErr := h.tunnelUsesNftables(fwd.TunnelID); modeErr != nil {
		log.Printf("relay force delete: tunnel mode check failed forward_id=%d err=%v", fwd.ID, modeErr)
		notes = append(notes, "隧道模式无法确认，数据面清理未执行")
	} else if nftMode && len(nftNodes) > 0 {
		keepLedger = true
		if merr := h.repo.MarkForwardPendingDelete(fwd.ID, nftNodes[0], time.Now()); merr != nil {
			log.Printf("relay force delete: mark pending delete failed forward_id=%d err=%v", fwd.ID, merr)
			notes = append(notes, "nft 待删台账登记失败，残留规则须人工确认清理")
		}
	}
	// 3. Delete the DB row (ledger-preserving for nft).
	var derr error
	if keepLedger {
		derr = h.repo.DeleteForwardCascadeKeepNftLedger(fwd.ID)
	} else {
		derr = h.deleteForwardByID(fwd.ID)
	}
	if derr != nil {
		log.Printf("relay force delete: DB delete failed forward_id=%d err=%v", fwd.ID, derr)
		notes = append(notes, "转发数据库记录删除失败，请检查面板日志")
	}
	// 4. Best-effort data-plane cleanup per entry node (mode-aware:
	// gost DeleteService, nft reconcile, realm re-render). Failures
	// are named per node; the nft ledger (when kept) lets the retry
	// loop converge them, the gost/realm residue waits for the agent
	// reconnect reconcile.
	nodeIDs := forwardPortNodeIDs(ports)
	if len(nodeIDs) == 0 {
		if entryIDs, terr := h.tunnelEntryNodeIDs(fwd.TunnelID); terr == nil {
			nodeIDs = entryIDs
		}
	}
	for _, nodeID := range nodeIDs {
		if serr := h.deleteForwardServicesOnNodeBatch(fwd, nodeID); serr != nil {
			notes = append(notes, fmt.Sprintf("节点 %s 数据面清理未确认：%v", h.nodeLabel(nodeID), serr))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "中转已强制删除，但部分节点侧规则清理未确认（残留规则需人工确认清理）：" + strings.Join(notes, "；")
}

// removeInboundRelays deletes the forwards the panel created for an inbound.
func (h *Handler) removeInboundRelays(inboundID int64) {
	rels, err := h.repo.ListInboundRelays(inboundID)
	if err != nil {
		return
	}
	for _, rel := range rels {
		rel := rel.NodeInboundRelay
		if warning, err := h.removeRelay(&rel, true); err != nil {
			log.Printf("relay cleanup failed relay_id=%d err=%v", rel.ID, err)
		} else if warning != "" {
			log.Printf("relay cleanup left residue relay_id=%d warning=%s", rel.ID, warning)
		}
	}
}

// ---- share links & subscription ----

// firstVersionToken scans a reported version string for the first
// whitespace-separated token that strictly parses as a dotted version
// (PORT-043): kernel binaries report themselves as a full banner —
// "Xray 26.11.9 (Xray, Penetrates Everything.) 676eaf6 (go1.27.3
// linux/amd64)" or "sing-box version 1.14.4" — whose leading token is
// not a version, so feeding the whole banner to parseVersionParts can
// never succeed. Only a token that strictly parses counts; a string
// with no version-shaped token yields nothing and the caller stays
// fail-closed.
func firstVersionToken(raw string) (string, bool) {
	for _, tok := range strings.Fields(raw) {
		tok = strings.TrimPrefix(tok, "v")
		if _, err := parseVersionParts(tok); err == nil {
			return tok, true
		}
	}
	return "", false
}

// kernelSupportsPostQuantumReality reports whether a kernel can run the
// X25519MLKEM768 Reality key exchange: Xray only, with a version at
// least 25.5.16 (the first Xray release with ML-KEM-768 Reality
// support). The version is read from the agent-reported installed
// banner first — it names the binary actually running — falling back
// to the clean pinned Version when the banner carries no parseable
// token. A version that cannot be established on either side never
// qualifies — advertising PQ to a kernel that cannot do it would break
// the handshake.
func kernelSupportsPostQuantumReality(k *model.NodeKernel) bool {
	if k == nil || k.KernelType != kernel.TypeXray {
		return false
	}
	version, ok := firstVersionToken(k.InstalledVersion)
	if !ok {
		version, ok = firstVersionToken(k.Version)
	}
	if !ok {
		return false
	}
	return !versionBelowPadded(version, 25, 5, 16)
}

// inboundEndpoints lists every address clients can use for the inbounds:
// the landing node directly (unless relay-only or local-only) and each
// relay entry.
func (h *Handler) inboundEndpoints(rows []model.NodeInbound) ([]kernel.Endpoint, []string) {
	relays, err := h.relayViews(0)
	if err != nil {
		// M-003：中转视图查询失败会导致所有中转端点静默丢失，必须记录
		log.Printf("inboundEndpoints: relayViews failed, relay endpoints will be missing: %v", err)
	}
	byInbound := make(map[int64][]relayView)
	for _, rel := range relays {
		byInbound[rel.InboundID] = append(byInbound[rel.InboundID], rel)
	}
	nodes := make(map[int64]*model.Node)
	// Per-kernel cache for the post-quantum Reality gate: many inbounds
	// share one kernel, and the lookup hits the DB.
	pqByKernel := make(map[int64]bool)
	pqFor := func(kernelID int64) bool {
		if v, ok := pqByKernel[kernelID]; ok {
			return v
		}
		v := false
		if kernelID > 0 {
			if k, err := h.repo.GetNodeKernel(kernelID); err == nil {
				v = kernelSupportsPostQuantumReality(k)
			}
		}
		pqByKernel[kernelID] = v
		return v
	}

	var endpoints []kernel.Endpoint
	var kinds []string
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		if row.CompanionOf > 0 {
			// 计量伴生入站不直接产出端点：它只经 subEndpoints 的改指
			// 服务分享订阅，节点自身链接/普通订阅永不指向它。
			continue
		}
		in, err := inboundFromModel(row)
		if err != nil {
			// M-002/M-003：入站解析失败会导致该入站从所有订阅中消失，必须记录
			// 旧逻辑：静默 continue，管理员无从察觉
			log.Printf("inboundEndpoints: inboundFromModel failed for inbound ID %d (node %d), skipping: %v", row.ID, row.NodeID, err)
			continue
		}
		node, ok := nodes[row.NodeID]
		if !ok {
			var err error
			node, err = h.repo.GetNodeByID(row.NodeID)
			if err != nil {
				log.Printf("inboundEndpoints: GetNodeByID failed for node %d: %v", row.NodeID, err)
			}
			nodes[row.NodeID] = node
		}
		proxy := importedProxy(&row)
		// The PQ Reality marker belongs to the landing inbound's hosting
		// kernel; imported nodes (proxy != nil, no local kernel) never
		// carry it.
		postQuantum := proxy == nil && in.Protocol == kernel.ProtocolVLESS &&
			in.Security.Type == kernel.SecurityReality && pqFor(row.KernelID)
		switch {
		case row.RelayOnly == 1:
			// R2-02（防静默）：纯中转入站不产直连端点，只经其面板
			// 中转端点出现。此前此分支无声——relayOnly 源导入的节
			// 点被选入订阅后四档拉取全空、保存回执无提示、装配日
			// 志零行，管理员无从察觉。行为不变（仍不产直连端点），
			// 但每次装配跳过必须留日志点名；保存面另有回执点名
			// （见 relayOnlySelectedWarning），建源面有源级提示
			// （见 relayOnlySourceWarning）。
			log.Printf("inboundEndpoints: relay-only inbound %q (inbound %d, node %d) serves no direct endpoint; only its relay endpoints, if any, are included", row.Name, row.ID, row.NodeID)
		case isLoopbackListen(row.Listen):
			// 同门另一静默分支：只监听本机地址的入站同样不产直连
			// 端点（远端客户端无从拨节点的本机地址），与 RelayOnly
			// 同口径留痕，只经其面板中转端点出现。
			log.Printf("inboundEndpoints: local-only inbound %q (inbound %d, node %d, listen %s) serves no direct endpoint; only its relay endpoints, if any, are included", row.Name, row.ID, row.NodeID, row.Listen)
		default:
			host := inboundShareAddress(&row, node)
			mode := inboundShareMode(&row)
			switch {
			case host == "":
				// F-A-02：显式选了 ipv4/ipv6/custom 却解析不出地址
				// （如节点后来改了地址族）时不能静默消失——与上面
				// M-002/M-003 同级留日志，管理员才有从察觉。
				if mode != "auto" {
					log.Printf("inboundEndpoints: share address unresolved for inbound ID %d (node %d), mode %s, skipping endpoint", row.ID, row.NodeID, mode)
				}
			case mode == "custom" && validateShareHostValue(host) != nil:
				// F-A-01/C-01：存量坏 ShareHost（写入面校验之前落库
				// 的）在拉取期隔离，不让结构字符裸拼进订阅；写入面
				// 已同标准校验，这里只兜历史数据。
				log.Printf("inboundEndpoints: invalid custom share host for inbound ID %d (node %d), skipping endpoint", row.ID, row.NodeID)
			case validateShareHostValue(host) != nil:
				// R3-E-01：auto/ipv4/ipv6 的地址源是节点 ServerIP /
				// ServerIPV4 / ServerIPV6——导入、联邦同步、备份恢复
				// 落库的值此前在拉取期无任何兜底，结构字符可改写
				// 订阅权威段、回环字面量对远端客户端亦无意义，与
				// custom 同标准同处置（跳过 + 日志）。此判定只作用
				// 于订阅端点生成路径：节点连接、转发拨号与诊断面对
				// ServerIP 的使用不在此列。
				log.Printf("inboundEndpoints: invalid share host for inbound ID %d (node %d), mode %s, skipping endpoint", row.ID, row.NodeID, mode)
			default:
				endpoints = append(endpoints, kernel.Endpoint{Name: row.Name, Host: host, Port: row.Port, Inbound: in, Direct: true, Proxy: proxy, SourceID: row.SourceID, PostQuantumReality: postQuantum})
				kinds = append(kinds, "direct")
			}
		}
		for _, rel := range byInbound[row.ID] {
			if !rel.ForwardExists || rel.ForwardStatus != 1 {
				// R-031：中转未就绪（转发不存在或未启用）时该中转端点
				// 被跳过——跳过本身正确，但此前无声，订阅拉取缺端点
				// 时管理员无从察觉；与本函数其余跳过分支同口径留日志。
				log.Printf("inboundEndpoints: relay %q (relay %d, inbound %d, node %d) not ready (forwardExists=%v, forwardStatus=%d), skipping relay endpoints", rel.Name, rel.ID, row.ID, row.NodeID, rel.ForwardExists, rel.ForwardStatus)
				continue
			}
			if len(rel.Entries) == 0 {
				// R-032：隧道无入口（Entries 为空）时内层循环零次执行，
				// 此前同样无声；与 R-031 同根，在此留日志。
				log.Printf("inboundEndpoints: relay %q (relay %d, inbound %d, node %d) has no entries, skipping relay endpoints", rel.Name, rel.ID, row.ID, row.NodeID)
				continue
			}
			for i, entry := range rel.Entries {
				name := rel.Name
				if len(rel.Entries) > 1 {
					name = fmt.Sprintf("%s %d", rel.Name, i+1)
				}
				host := entry.Host
				if rel.ShareHost != "" {
					if validateShareHostValue(rel.ShareHost) == nil {
						host = shareHostValue(rel.ShareHost)
					} else {
						// F-A-01/C-01：存量坏中转 ShareHost 回退入口
						// 自身地址下发，不裸拼也不整条丢掉中转端点。
						log.Printf("inboundEndpoints: invalid relay share host for relay ID %d (inbound %d), falling back to entry host", rel.ID, row.ID)
					}
				}
				if validateShareHostValue(host) != nil {
					// R3-E-01：入口自身地址（forward_port.in_ip 或
					// 入口节点 server_ip）同过订阅面标准。entry 已是
					// 最底层地址源、无更下层可回退，坏值跳过该
					// entry + 日志，不裸拼下发。
					log.Printf("inboundEndpoints: invalid relay entry host for relay ID %d (inbound %d), skipping entry", rel.ID, row.ID)
					continue
				}
				endpoints = append(endpoints, kernel.Endpoint{Name: name, Host: host, Port: entry.Port, Inbound: in, Proxy: proxy, SourceID: row.SourceID, RelayID: rel.ID, PostQuantumReality: postQuantum})
				kinds = append(kinds, "relay")
			}
		}
	}
	return endpoints, kinds
}

func (h *Handler) inboundShare(w http.ResponseWriter, r *http.Request) {
	id := idFromBody(r, w)
	if id <= 0 {
		return
	}
	row, err := h.repo.GetNodeInbound(id)
	if err != nil || row == nil {
		response.WriteJSON(w, response.ErrDefault("入站不存在"))
		return
	}
	if row.CompanionOf > 0 {
		// 伴生入站的密码是身份 PSK，绝不能单独渲染成链接。
		response.WriteJSON(w, response.ErrDefault("计量伴生入站不提供分享链接"))
		return
	}
	enabled := *row
	enabled.Enabled = 1 // share links are useful to prepare even while disabled
	endpoints, kinds := h.inboundEndpoints([]model.NodeInbound{enabled})
	out := make([]shareEntry, 0, len(endpoints))
	for i, e := range endpoints {
		uri, _ := kernel.ShareURI(e)
		surge, _ := kernel.SurgeLine(e)
		out = append(out, shareEntry{Name: e.Name, Kind: kinds[i], Host: e.Host, Port: e.Port, URI: uri, Surge: surge})
	}
	response.WriteJSON(w, response.OK(out))
}

// C1-02（1.9.5 发版后审计）：本端点此前是全部凭据面中唯一完全
// 无限流者——错误 token 每次直达查库、有效 token 每次触发全量
// 渲染，对单连接 SQLite 面板是低成本请求放大面。按源 IP 限流，
// 量级与同族开放面齐平（gh 镜像/flow 均为 60 次/分钟）：订阅
// 客户端的拉取间隔以分钟计，60/分钟远高于正常用量；键走
// rateLimitKeyForRequest 收口，桶为 allowWithLimit 独立桶（不查
// 登录紧缩集、不与 login 共用，两面互不外溢，与 sub_store 同型）。
var openSubLimiter = &loginRateLimiter{buckets: make(map[string]*loginAttemptBucket)}

const openSubMaxRequestsPerMinute = 60

// openSubscription serves GET /api/v1/open_api/sub/{token}. It is outside
// JWT auth; the 128-bit token is the credential and is compared in constant
// time.
func (h *Handler) openSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !openSubLimiter.allowWithLimit(rateLimitKeyForRequest(r, "open_api sub rate limit"), time.Now(), openSubMaxRequestsPerMinute) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/open_api/sub/"), "/")
	// R1-02-L1：不得按长度前置过滤——迁移面保留长度 >32 的旧默认
	// token、旧 SubUser token 更是逐字复制（repository_subscription
	// 迁移），恰 32 位的硬门会让这批存量链接在查库前就永久 404，
	// 与「升级链接不变」的承诺冲突。现行生成面恒发 32 位；拉取面
	// 只按实存 token 精确查验（下方查库＋常时比较即完备凭据校验，
	// 长度不是安全边界）。
	if token == "" {
		http.NotFound(w, r)
		return
	}
	sub, err := h.repo.GetSubscriptionByToken(token)
	if err != nil || sub == nil || !constantTimeEqual(token, sub.Token) {
		http.NotFound(w, r)
		return
	}
	// B4/F-058: 删除墓碑对订阅链接等同已删除（404）——删除对用户立即
	// 生效，只有节点侧凭据下发允许后台重试。
	if sub.PendingDelete == 1 {
		http.NotFound(w, r)
		return
	}

	// X-P3-2：别名解析与计数侧共用 subFormatAlias，防两处再分叉。
	format := subFormatAlias(r.URL.Query().Get("format"))
	if format == "" {
		format = kernel.FormatForUserAgent(r.UserAgent())
	}
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// C-08：保留未经过任何请求筛选的全量行——占位节点只在订阅本身
	// 无任何产出时才渲染；?user=/?relay=/?inbound= 筛空与订阅自身
	// 的中转范围筛空是筛选器的答案，须保持 F-080 空渲染（个性化拉
	// 取计数 0、中转范围拉取不含直连节点，均由契约测试锁定）。
	// 深拷贝：下方 ?inbound= 过滤用 rows[:0] 复用同一底层数组。
	allRows := append([]model.NodeInbound(nil), rows...)
	if only := r.URL.Query().Get("inbound"); only != "" {
		id, _ := strconv.ParseInt(only, 10, 64)
		filtered := rows[:0]
		for _, row := range rows {
			if row.ID == id {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	// Filter by user's assigned inbounds/relays: ?user=<userID>&sig=<hmac>
	// The sig binds the user ID to the backend secret (issued by the
	// authenticated /user-inbound/mine endpoint); v2 sigs additionally bind
	// the subscription token (K-002). Without a valid signature
	// the user parameter is rejected, so a leaked subscription token cannot
	// be used to enumerate other users' nodes (IDOR).
	// An assignment with relayID=0 grants the inbound's direct endpoints;
	// an assignment with relayID>0 grants that relay's endpoints.
	var userAssignments []repo.UserInboundAssignment
	if userFilter := r.URL.Query().Get("user"); userFilter != "" {
		if userID, err := strconv.ParseInt(userFilter, 10, 64); err == nil && userID > 0 {
			// K-002：先验 token 绑定的 v2 签名，再兼容 v1 遗留签名。
			if !h.verifySubURLSigForToken(token, userID, r.URL.Query().Get("sig")) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			assignments, assignErr := h.repo.ListUserInboundAssignments(userID)
			if assignErr != nil {
				// R3-MR-01：分配读失败不得装空集——空集会把该用户
				// 的已分配入站全部过滤、渲染出一份空订阅当成功。
				// 与上方 ListNodeInbounds 失败同口径回 500。
				log.Printf("open subscription: list user inbound assignments failed user_id=%d err=%v", userID, assignErr)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			// R18-B2-01：先查订阅模型分配——被分配了本订阅的用户
			// 直接放行全量（与不带 ?user= 的分配卡片链接同口径；
			// token 本身已是凭据，订阅分配即授权）。只有未被分配
			// 本订阅的用户才走旧入站分配过滤。分配读失败同样
			// fail-closed 回 500，不得装「未分配」继续。
			assignedSub := false
			if subIDs, subErr := h.repo.ListUserSubscriptionIDs(userID); subErr != nil {
				log.Printf("open subscription: list user subscription ids failed user_id=%d err=%v", userID, subErr)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			} else {
				for _, id := range subIDs {
					if id == sub.ID {
						assignedSub = true
						break
					}
				}
			}
			if !assignedSub {
				userAssignments = assignments
				rows = filterInboundRowsByUserAssignments(rows, userAssignments)
			}
		} else {
			// A-02 (AUDIT-R215): ?user= 在场但不是正整数时 fail-closed——
			// 不得丢弃 userFilter、按裸 token 出全量。与已签名用户零分配
			// 同口径：行集清空，后续正常渲染为 200 空节点列表。
			rows = nil
		}
	}
	endpoints := h.subRenderEndpoints(sub, rows)
	// If ?user= is set, further restrict endpoints to the assigned relays:
	// direct endpoints require a relayID=0 assignment; relay endpoints
	// require an assignment for that specific relay.
	if len(userAssignments) > 0 {
		endpoints = filterEndpointsByUserAssignments(endpoints, userAssignments)
	}
	// Filter by specific relay: ?relay=<id> for a relay, ?relay=direct for direct only
	if relayFilter := r.URL.Query().Get("relay"); relayFilter != "" {
		var filtered []kernel.Endpoint
		if relayFilter == "direct" {
			for _, e := range endpoints {
				if e.Direct {
					filtered = append(filtered, e)
				}
			}
		} else if relayID, err := strconv.ParseInt(relayFilter, 10, 64); err == nil {
			for _, e := range endpoints {
				if e.RelayID == relayID {
					filtered = append(filtered, e)
				}
			}
		}
		endpoints = filtered
	}
	// B-L4-NEW：kind=own 在产品路径上不存在——save 强制 share、
	// 启动迁移全量转 share，Userinfo 头的非分享分支是死代码。
	// 订阅恒按自身计量计数与 TrafficLimit 上报；手工造 own 行
	// 的防御仍在 isShare 各守卫（本分支之外不碰）。
	var up, down, total int64
	expire := sub.ExpireTime / 1000
	up, down, total = sub.UploadBytes, sub.DownloadBytes, sub.TrafficLimit

	// F-080: 不再对"端点为 0"单设 503。此前同一"无可用节点"状态有两种
	// 语义：端点 0 个返回 503，端点被目标格式全过滤却返回 200 空体。
	// 503 会被订阅客户端当服务端故障：保留上一份节点列表（可能含已删
	// 节点）并反复重试；统一为 200 + 该格式的空渲染，客户端以空列表
	// 如实更新，下个周期有节点时自动恢复。停用/过期/超限同理不再回
	// 403：改为只含一个原因占位节点的配置（见下），客户端据此换掉旧
	// 列表并看到原因，而不是停在旧配置上无从知晓。
	pipe := parsePipeline(sub.Options)
	subOpts := h.subOptions(pipe)
	// sing-box 客户端 ≥1.14 才下发导入的 Snell 节点；UA 解析不出版本
	// 一律不给（fail-closed，老客户端拿到也渲染不了）。
	subOpts.SingboxSnell = kernel.SingboxClientHasSnell(r.UserAgent())
	// R9X-005 连带格：sing-box 客户端 ≥1.13 才下发 Naive 节点；低于
	// 1.13 略过该节点（其余节点照常下发），不整份拒绝。
	subOpts.SingboxNaive = kernel.SingboxClientHasNaive(r.UserAgent())
	var body, contentType string
	if reason := subInactiveReason(sub, time.Now()); reason != "" {
		body, contentType, err = kernel.Subscription(format, nil, kernel.SubOptions{Notices: []string{reason}})
	} else if len(endpoints) == 0 && h.subYieldsNothing(sub, allRows) {
		// C-08：订阅启用且未过期未超限、但自身无任何产出（无源／源全
		// 失效）时，此前按所选格式渲染空体——客户端只看到空列表、无
		// 任何原因。改走与停用／过期／超限相同的占位节点机制，原因按
		// 成因逐级定。筛选器筛空时不进此分支（见上 allRows 注释）；
		// F-080 的 200 语义不变：仍是 200 + 占位节点，客户端如实更
		// 新、不保留旧列表。
		body, contentType, err = kernel.Subscription(format, nil, kernel.SubOptions{Notices: []string{h.subZeroEndpointReason(sub, allRows, time.Now())}})
	} else {
		body, contentType, err = kernel.Subscription(format, endpoints, subOpts)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", up, down, total, expire))
	w.Header().Set("Profile-Update-Interval", "12")
	// Clients use the file name as the profile name.
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(sub.Name))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(body))
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}
