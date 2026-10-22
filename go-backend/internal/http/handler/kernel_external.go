package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/netguard"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// External inbounds are proxy nodes the panel does not deploy (built by a
// script, or on a machine without the agent). They are stored as inbounds
// without a kernel or node; Listen holds the node's public address so relays
// and subscriptions treat them like any other inbound.

// maxImportNodes bounds one import; subscriptions rarely carry more.
const maxImportNodes = 500

// maxSubscriptionBytes bounds a fetched subscription body.
const maxSubscriptionBytes = 5 << 20

// H-15/R-005: SSRF 防护 - 检查 IP 是否为内网/保留地址。
// R2 批1起本函数只是 netguard 的薄封装（判定正本在
// internal/netguard）：段集基底取 agent bypass 集合（含 240/4、
// 192.0.0/24、198.18/15、100::/64，旧手写集合缺 240/4），且
// NAT64/6to4 按内嵌 IPv4 同判、本地 NAT64 整段非公网。新调用方
// 应直接用 netguard.Judge 按自身策略判定，不要再扩散本封装。
func isPrivateIP(ip net.IP) bool {
	return netguard.IsNonPublicIP(ip)
}

// IMP-005：原 validateSubscriptionURL 已删除。该函数是 H-15 时期的订阅 URL
// 校验（DNS 预解析 + 私网判定），早已被 SecureFetch（secure_fetch.go，含逐跳
// 重定向校验、拨号时重解析、元数据硬黑名单）完整取代，但未被生产路径调用，
// 继续保留会误导维护者以为订阅抓取走的是这条通道。SSRF 回归测试见
// secure_fetch_d17_test.go 与本文件的 isPrivateIP 测试。

func isExternalInbound(row *model.NodeInbound) bool {
	return row != nil && row.KernelID == 0
}

// importedProxy returns the stored Mihomo-schema proxy of an imported node,
// or nil for nodes imported before the proxy format (plain ss inbounds).
func importedProxy(row *model.NodeInbound) kernel.Proxy {
	if !isExternalInbound(row) || strings.TrimSpace(row.Extra) == "" {
		return nil
	}
	var p kernel.Proxy
	if err := json.Unmarshal([]byte(row.Extra), &p); err != nil || len(p) == 0 {
		return nil
	}
	return p
}

type externalImportResult struct {
	Created []int64  `json:"created"`
	Errors  []string `json:"errors"`
}

// inboundImport creates imported nodes from share links, a base64 / Clash /
// sing-box / Surge subscription body, or a subscription URL to fetch.
func (h *Handler) inboundImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		Links     string `json:"links"`
		RelayOnly bool   `json:"relayOnly"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	text := strings.TrimSpace(req.Links)
	if text == "" {
		response.WriteJSON(w, response.ErrDefault("请粘贴分享链接、订阅内容或订阅地址"))
		return
	}
	if isSubscriptionURL(text) {
		body, err := fetchSubscription(r.Context(), text)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault("获取订阅失败："+err.Error()))
			return
		}
		text = body
	}
	nodes, parseErrs := kernel.ParseImport(text)
	if len(nodes) > maxImportNodes {
		response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("一次最多导入 %d 个节点", maxImportNodes)))
		return
	}

	// B4/F-132: 去重快照与逐条创建必须在同一临界区——external 行共用
	// 0 号端口锁。此前快照在锁外、锁仅罩单条 Create，两个并发导入
	// （或导入与编辑/订阅源更新并发）可同时通过去重检查建出重复行
	// （F-003 后 DB 索引不再兜底 external 行，应用层去重是唯一防线）。
	externalMu := repo.AcquirePortAllocLock(0)
	defer repo.ReleasePortAllocLock(externalMu)

	existing, err := h.repo.ListNodeInbounds()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	seen := make(map[string]bool)
	for _, row := range existing {
		if isExternalInbound(&row) {
			seen[externalKey(row.Listen, row.Port)] = true
		}
	}

	res := externalImportResult{Created: []int64{}, Errors: append([]string{}, parseErrs...)}
	now := time.Now().UnixMilli()
	for _, node := range nodes {
		key := externalKey(node.Host, node.Port)
		if seen[key] {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%s 已经导入过", node.Name, key))
			continue
		}
		seen[key] = true
		row, err := externalRow(node, req.RelayOnly, now)
		if err == nil {
			err = h.repo.CreateNodeInbound(&row)
		}
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s：%v", node.Name, err))
			continue
		}
		res.Created = append(res.Created, row.ID)
	}
	if len(res.Created) == 0 {
		msg := "没有导入任何节点"
		if len(res.Errors) > 0 {
			msg += "：" + res.Errors[0]
		}
		response.WriteJSON(w, response.R{Code: -1, Msg: msg, Data: res, TS: now})
		return
	}
	response.WriteJSON(w, response.OK(res))
}

func externalRow(node kernel.ImportedNode, relayOnly bool, now int64) (model.NodeInbound, error) {
	raw, err := json.Marshal(node.Proxy)
	if err != nil {
		return model.NodeInbound{}, err
	}
	return model.NodeInbound{
		Name:        truncateRunes(node.Name, 100),
		Protocol:    kernel.ProxyProtocol(node.Proxy),
		Listen:      node.Host,
		Port:        node.Port,
		ShareHost:   node.Host,
		Extra:       string(raw),
		RelayOnly:   boolToInt(relayOnly),
		Enabled:     1,
		CreatedTime: now,
		UpdatedTime: now,
	}, nil
}

func isSubscriptionURL(text string) bool {
	if strings.ContainsAny(text, " \n\t") {
		return false
	}
	u, err := url.Parse(text)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// fetchSubscription downloads a subscription. The user agent is
// "flmx-panel/1.0" (see secure_fetch.go), not a Clash/Mihomo impersonation
// (IMP-002: the old comment claiming a Mihomo UA was outdated; doc/guide/import.md
// said the same and has been corrected).
func fetchSubscription(ctx context.Context, rawURL string) (string, error) {
	body, _, err := fetchSubscriptionInfo(ctx, rawURL)
	return body, err
}

// fetchSubscriptionInfo is fetchSubscription plus the provider's
// Subscription-Userinfo header (plan traffic and expiry).
func fetchSubscriptionInfo(ctx context.Context, rawURL string) (string, string, error) {
	// R5-B5 D16: 经 SecureFetch 统一硬化（H-15 残余缺口：0.0.0.0 绕过、
	// DNS rebinding TOCTOU、逐跳重定向校验）。
	// 注意：SecureFetch 强制 https；订阅 URL 为 http 时需用户改用 https。
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// R1 顺手项（C 观察 1）：订阅抓取单独传领域上限 maxSubscriptionBytes
	// （5MB，仓库既有同类上限，下方 len 检查同值），在抓取层即截断，
	// 不再先吞 SecureFetch 的 50MB 全局上限。取值依据：订阅内容合法
	// 体积由 maxSubscriptionBytes 定义（导入/同步共用），抓取层与
	// 业务判定同值即不改变任何已接受行为，只提前拒绝。
	body, header, err := secureFetchWithHeaderLimit(ctx, rawURL, maxSubscriptionBytes)
	if err != nil {
		// IMP-007：公网 http 拒绝错误改中文。SecureFetch 内部返回英文
		// "public http not allowed (use https)"，此处在对外透出前翻译，
		// 与中文 UI 保持一致。
		if strings.Contains(err.Error(), "public http not allowed") {
			return "", "", errors.New("公网 http 订阅地址被拒绝，请改用 https 地址")
		}
		return "", "", err
	}
	if len(body) > maxSubscriptionBytes {
		return "", "", errors.New("订阅内容超过 5MB")
	}
	return string(body), truncate(strings.TrimSpace(header.Get("Subscription-Userinfo")), 255), nil
}

// inboundExternalUpdate edits an imported node: its name, address and
// options, and optionally replaces its parameters with a new share link.
func (h *Handler) inboundExternalUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	var req struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Host      string `json:"host"`
		Port      int    `json:"port"`
		Method    string `json:"method"`
		Password  string `json:"password"`
		Link      string `json:"link"`
		Remark    string `json:"remark"`
		RelayOnly bool   `json:"relayOnly"`
	}
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("请求参数错误"))
		return
	}
	row, err := h.repo.GetNodeInbound(req.ID)
	if err != nil || row == nil || !isExternalInbound(row) {
		response.WriteJSON(w, response.ErrDefault("导入的节点不存在"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 100 {
		response.WriteJSON(w, response.ErrDefault("名称不能为空，且不超过 100 字"))
		return
	}
	host := strings.Trim(strings.TrimSpace(req.Host), "[]")
	if err := kernel.ValidateImportHost(host); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		response.WriteJSON(w, response.ErrDefault("端口不正确"))
		return
	}

	proxy := importedProxy(row)
	if link := strings.TrimSpace(req.Link); link != "" {
		nodes, errs := kernel.ParseImport(link)
		if len(nodes) != 1 {
			msg := "请只粘贴一个节点"
			if len(nodes) == 0 && len(errs) > 0 {
				msg = errs[0]
			}
			response.WriteJSON(w, response.ErrDefault(msg))
			return
		}
		proxy = nodes[0].Proxy
		// The address fields follow the new link unless the user changed them.
		if host == row.Listen && req.Port == row.Port {
			host, req.Port = nodes[0].Host, nodes[0].Port
		}
	}
	if proxy != nil {
		proxy["server"], proxy["port"] = host, req.Port
		raw, err := json.Marshal(proxy)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		row.Extra = string(raw)
		row.Protocol = kernel.ProxyProtocol(proxy)
		row.Listen, row.Port = host, req.Port
	} else {
		// Nodes imported before the proxy format: plain Shadowsocks.
		method := strings.ToLower(strings.TrimSpace(req.Method))
		if !containsMethod(method) {
			response.WriteJSON(w, response.ErrDefault("不支持的加密方式 "+method))
			return
		}
		if req.Password == "" {
			response.WriteJSON(w, response.ErrDefault("密码不能为空"))
			return
		}
		// R3X-008 保存层补点：本分支绕 Normalize 直写 method/password，
		// 须复用导入期同一 SS2022 密钥校验 helper，否则导入收口只
		// 完成一半（坏 key 可经旧格式编辑面重新写回）。
		if err := kernel.ValidateSS2022Password(method, req.Password); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		in, err := inboundFromModel(*row)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		in.Listen, in.Port = host, req.Port
		in.Settings.Method, in.Settings.Password = method, req.Password
		if err := applyInboundToModel(in, row); err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
	}
	row.Name = name
	row.ShareHost = host
	row.Remark = strings.TrimSpace(req.Remark)
	row.RelayOnly = boolToInt(req.RelayOnly)
	row.UpdatedTime = time.Now().UnixMilli()
	// IMP-004：修改地址后做去重校验。地址变更不经过导入的 seen 去重表，
	// 之前可以直接改出与另一个导入节点相同的 host:port，违背"同一地址和
	// 端口不会重复导入"的约束。
	// B4/F-132: 去重检查与保存须与导入路径共用 0 号锁串行，此前无锁：
	// 并发导入可在检查后、保存前建出同 (host,port) 行。
	externalMu := repo.AcquirePortAllocLock(0)
	if rows, err := h.repo.ListNodeInbounds(); err == nil {
		key := externalKey(row.Listen, row.Port)
		for i := range rows {
			other := &rows[i]
			if other.ID == row.ID || !isExternalInbound(other) {
				continue
			}
			if externalKey(other.Listen, other.Port) == key {
				repo.ReleasePortAllocLock(externalMu)
				response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("已存在相同地址和端口的导入节点（%s），不能重复", key)))
				return
			}
		}
	}
	if err := h.repo.SaveNodeInbound(row); err != nil {
		repo.ReleasePortAllocLock(externalMu)
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	repo.ReleasePortAllocLock(externalMu)
	// Relay forwards dial the stored address; point them at the new one.
	if problems := h.retargetRelays(row); len(problems) > 0 {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但部分中转更新失败: " + strings.Join(problems, "；"), TS: row.UpdatedTime})
		return
	}
	// So do kernels chained through the node.
	if warnings := h.applyChainReferrers(row.ID); len(warnings) > 0 {
		response.WriteJSON(w, response.R{Code: 0, Msg: "已保存，但" + strings.Join(warnings, "；"), TS: row.UpdatedTime})
		return
	}
	response.WriteJSON(w, response.OKEmpty())
}

func externalKey(host string, port int) string {
	return net.JoinHostPort(strings.ToLower(host), strconv.Itoa(port))
}

func containsMethod(method string) bool {
	for _, m := range kernel.SSMethods() {
		if m == method {
			return true
		}
	}
	return false
}

// retargetRelays updates the remote address of every managed relay forward
// of an inbound after its address changed.
func (h *Handler) retargetRelays(row *model.NodeInbound) []string {
	relays, err := h.repo.ListInboundRelays(row.ID)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
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
		target, err := h.relayTarget(row, tunnel)
		if err != nil {
			problems = append(problems, rel.Name+": "+err.Error())
			continue
		}
		if target == fwd.RemoteAddr {
			continue
		}
		if err := h.repo.UpdateForward(fwd.ID, fwd.Name, fwd.TunnelID, target, fwd.Strategy, time.Now().UnixMilli(),
			fwd.SpeedID, fwd.MaxConn, fwd.IPMaxConn, fwd.IPSpeedID, 0, fwd.ProxyProtocolReceive, fwd.ProxyProtocolSend); err != nil {
			problems = append(problems, rel.Name+": "+err.Error())
			continue
		}
		updated, err := h.getForwardRecord(fwd.ID)
		if err != nil || updated == nil {
			continue
		}
		if err := h.syncForwardServices(updated, "UpdateService", true); err != nil {
			problems = append(problems, rel.Name+": "+err.Error())
		}
	}
	return problems
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
