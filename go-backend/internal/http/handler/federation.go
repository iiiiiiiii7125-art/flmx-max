package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go-backend/internal/config"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/http/client"
	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/netguard"
	"go-backend/internal/store/repo"
	"go-backend/internal/ws"
)

type federationTunnelRequest struct {
	Protocol   string `json:"protocol"`
	RemotePort int    `json:"remotePort"`
	Target     string `json:"target"`
}

type createPeerShareRequest struct {
	Name           string `json:"name"`
	NodeID         int64  `json:"nodeId"`
	MaxBandwidth   int64  `json:"maxBandwidth"`
	ExpiryTime     int64  `json:"expiryTime"`
	PortRangeStart int    `json:"portRangeStart"`
	PortRangeEnd   int    `json:"portRangeEnd"`
	AllowedDomains string `json:"allowedDomains"`
	AllowedIPs     string `json:"allowedIps"`
}

type deletePeerShareRequest struct {
	ID int64 `json:"id"`
}

type resetPeerShareFlowRequest struct {
	ID int64 `json:"id"`
}

type updatePeerShareRequest struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	MaxBandwidth   int64  `json:"maxBandwidth"`
	ExpiryTime     int64  `json:"expiryTime"`
	PortRangeStart int    `json:"portRangeStart"`
	PortRangeEnd   int    `json:"portRangeEnd"`
	AllowedDomains string `json:"allowedDomains"`
	AllowedIPs     string `json:"allowedIps"`
}

type nodeImportRequest struct {
	RemoteURL string `json:"remoteUrl"`
	Token     string `json:"token"`
}

type federationRuntimeReservePortRequest struct {
	ResourceKey   string `json:"resourceKey"`
	Protocol      string `json:"protocol"`
	RequestedPort int    `json:"requestedPort"`
}

type federationRuntimeTarget struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// D3-04: 目标是另一个 share 的角色服务时，消费方附上目标 share
	// 的 relay 凭据，提供方据此为 middle 的 chain connector 写 auth。
	RelayUsername string `json:"relayUsername,omitempty"`
	RelayPassword string `json:"relayPassword,omitempty"`
}

type federationRuntimeApplyRoleRequest struct {
	ReservationID string                    `json:"reservationId"`
	ResourceKey   string                    `json:"resourceKey"`
	Role          string                    `json:"role"`
	Protocol      string                    `json:"protocol"`
	Strategy      string                    `json:"strategy"`
	Targets       []federationRuntimeTarget `json:"targets"`
}

type federationRuntimeReleaseRoleRequest struct {
	BindingID     string `json:"bindingId"`
	ReservationID string `json:"reservationId"`
	ResourceKey   string `json:"resourceKey"`
}

func federationRuntimeChainName(bindingID string) string {
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return ""
	}
	return "fed_chain_" + bindingID
}

func buildFederationMiddleChainConfig(chainName string, runtimeID int64, protocol, strategy string, targets []federationRuntimeTarget, interfaceName string) (map[string]interface{}, error) {
	chainName = strings.TrimSpace(chainName)
	if chainName == "" {
		return nil, fmt.Errorf("chain name is required")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("targets are required for middle role")
	}
	protocol = defaultString(protocol, "tls")
	nodeItems := make([]map[string]interface{}, 0, len(targets))
	for i, target := range targets {
		host := strings.TrimSpace(target.Host)
		if host == "" || target.Port <= 0 {
			return nil, fmt.Errorf("Invalid target")
		}
		targetProtocol := defaultString(target.Protocol, protocol)
		connector := map[string]interface{}{
			"type": "relay",
		}
		// D3-04: 目标是带鉴权的角色服务时必须携带其 share 凭据，
		// 否则新版提供方的 relay handler 会拒绝连接。
		if auth := relayAuthConfig(target.RelayUsername, target.RelayPassword); auth != nil {
			connector["auth"] = auth
		}
		// F-UDP-7: 与本地链 builder 共用 tunnelRelayMetadata，
		// 防止两套生成面的 mux 参数漂移。
		if connectorMetadata := tunnelRelayMetadata(targetProtocol); len(connectorMetadata) > 0 {
			connector["metadata"] = connectorMetadata
		}
		nodeItems = append(nodeItems, map[string]interface{}{
			"name":      fmt.Sprintf("node_%d", i+1),
			"addr":      processServerAddress(fmt.Sprintf("%s:%d", host, target.Port)),
			"connector": connector,
			"dialer":    buildTunnelDialerConfig(targetProtocol),
		})
	}

	chainData := map[string]interface{}{
		"name": chainName,
		"hops": []map[string]interface{}{
			{
				"name": fmt.Sprintf("hop_%d", runtimeID),
				"selector": map[string]interface{}{
					"strategy":    runtimeTunnelStrategy(strategy),
					"maxFails":    1,
					"failTimeout": int64(600000000000),
				},
				"nodes": nodeItems,
			},
		},
	}
	if strings.TrimSpace(interfaceName) != "" {
		hops := chainData["hops"].([]map[string]interface{})
		hops[0]["interface"] = interfaceName
	}
	return chainData, nil
}

func updateChainPayload(chainName string, chainData map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"chain": chainName,
		"data":  chainData,
	}
}

type federationRuntimeDiagnoseRequest struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Count    int    `json:"count"`
	Timeout  int    `json:"timeout"`
	Protocol string `json:"protocol"`
}

// maxFederationDiagnoseCount 是对端诊断的 ping 次数上限（合法调用方
// control_plane 恒传 4，留一倍多余量）。
const maxFederationDiagnoseCount = 10

// federationDiagnoseLookupIP 是诊断目标主机名的解析入口（默认
// net.LookupIP），抽成 var 以便测试注入假解析结果。解析口径与
// SecureFetch 的 validateFetchURL 一致：解析后逐地址判定、任一
// 不合法整体拒绝，防 DNS rebinding 把诊断引向内网。
var federationDiagnoseLookupIP = net.LookupIP

// resolveFederationDiagnoseTarget 校验诊断目标并把它钉死为一个已
// 校验的 IP 字面量（F-086 + D3-07）。判定不新写一套：R2 批1起走
// netguard 普通策略（诊断无放行通道），与 SecureFetch 同源。命中
// 内网/保留段（RFC1918、回环、链路本地含云元数据整段、ULA、
// CGNAT、未指定、组播、0.0.0.0/8、240/4 与翻译内嵌形态）一律拒绝
// ——否则提供方节点会被对端当作探测内网的跳板。目标是主机名时先
// 解析，对每个返回地址逐一判定，任一不合法整体拒绝；解析失败无法
// 验证，按 fail-closed 拒绝。
//
// D3-07：此前两个下发点只做校验、却把原始主机名发给节点，节点侧
// 二次解析与面板校验之间存在 DNS rebinding 窗口（校验时公网、节点
// 解析时内网）。现在校验与钉死同体：下发的一律是此处解析并通过
// 判定的 IP，主机名不再离开面板（TcpPing 协议字段 ip 本就收 IP
// 字面量，节点侧无需改动）。
func resolveFederationDiagnoseTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	// R2X-006：判定改走 netguard 普通策略（诊断无放行通道）——
	// NAT64/6to4 按内嵌同判、本地 NAT64 整段拒、段集含 240/4 等
	// bypass 基底；旧 isPrivateIP 口径对翻译形态全漏。
	if parsed := net.ParseIP(target); parsed != nil {
		if d := netguard.Judge(parsed, netguard.PolicyPublicOnly, false); !d.Allowed {
			return "", errors.New("不允许对内网/保留地址发起诊断")
		}
		return parsed.String(), nil
	}
	ips, err := federationDiagnoseLookupIP(target)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("诊断目标无法解析: %s", target)
	}
	for _, ip := range ips {
		if d := netguard.Judge(ip, netguard.PolicyPublicOnly, false); !d.Allowed {
			return "", errors.New("不允许对内网/保留地址发起诊断")
		}
	}
	return ips[0].String(), nil
}

// validateFederationDiagnoseTarget 校验诊断目标并钳制次数，返回
// 钳制后的 count（签名保留，既有调用方与测试的契约不变）。
func validateFederationDiagnoseTarget(target string, count int) (int, error) {
	if _, err := resolveFederationDiagnoseTarget(target); err != nil {
		return 0, err
	}
	if count <= 0 {
		count = 4
	}
	if count > maxFederationDiagnoseCount {
		count = maxFederationDiagnoseCount
	}
	return count, nil
}

type federationRuntimeCommandRequest struct {
	CommandType string      `json:"commandType"`
	Data        interface{} `json:"data"`
}

type peerShareUsedPort struct {
	RuntimeID   int64  `json:"runtimeId"`
	Port        int    `json:"port"`
	Role        string `json:"role"`
	Protocol    string `json:"protocol"`
	ResourceKey string `json:"resourceKey"`
	Applied     int    `json:"applied"`
	UpdatedTime int64  `json:"updatedTime"`
}

type peerShareListItem struct {
	repo.PeerShare
	UsedPorts        []int               `json:"usedPorts"`
	UsedPortDetails  []peerShareUsedPort `json:"usedPortDetails"`
	ActiveRuntimeNum int                 `json:"activeRuntimeNum"`
}

type remoteUsageBindingItem struct {
	BindingID       int64  `json:"bindingId"`
	TunnelID        int64  `json:"tunnelId"`
	TunnelName      string `json:"tunnelName"`
	ChainType       int    `json:"chainType"`
	HopInx          int    `json:"hopInx"`
	AllocatedPort   int    `json:"allocatedPort"`
	ResourceKey     string `json:"resourceKey"`
	RemoteBindingID string `json:"remoteBindingId"`
	UpdatedTime     int64  `json:"updatedTime"`
}

type remoteUsageNodeItem struct {
	NodeID           int64                    `json:"nodeId"`
	NodeName         string                   `json:"nodeName"`
	RemoteURL        string                   `json:"remoteUrl"`
	ShareID          int64                    `json:"shareId"`
	PortRangeStart   int                      `json:"portRangeStart"`
	PortRangeEnd     int                      `json:"portRangeEnd"`
	MaxBandwidth     int64                    `json:"maxBandwidth"`
	CurrentFlow      int64                    `json:"currentFlow"`
	ExpiryTime       int64                    `json:"expiryTime"`
	UsedPorts        []int                    `json:"usedPorts"`
	Bindings         []remoteUsageBindingItem `json:"bindings"`
	ActiveBindingNum int                      `json:"activeBindingNum"`
	SyncError        string                   `json:"syncError,omitempty"`
	// FIX2-B（V5-3）：对端面板版本与版本提示（旧对端为空）。
	PanelVersion   string `json:"panelVersion,omitempty"`
	VersionWarning string `json:"versionWarning,omitempty"`
}

// flmxNoPrivateBypassName 是 agent 内置 bypass 的名字（FIX2-C #8，
// FIX-I 未竟项）：exit 角色服务带上它后，agent 在 relay 连接时拒绝
// 目标为私网/特殊地址的连接（agent 侧实现见 go-gost/x/bypass 的
// NewNoPrivateBypass，名字在解析层认领、不依赖配置下发 bypass
// 定义）。没有这层时 exit 服务的目标完全由连接方决定（D3-04 的
// 开放中继面只能靠 share auth 收窄到持凭据方，目标面仍任意）。
const flmxNoPrivateBypassName = "flmx-no-private"

// flmxNoPrivateMinAgentVersion 是首个内置 flmx-no-private 的 agent
// 版本（与面板同仓同版发布，即本版 1.3.4）。
const flmxNoPrivateMinAgentVersion = "1.3.4"

// agentSupportsNoPrivateBypass 判断节点 agent 是否内置了
// flmx-no-private。版本未知/不可解析一律按不支持处理：旧 agent
// 对未知 bypass 名是 fail-open（注册表 wrapper 对未注册名的
// Contains 恒 false——已在 go-gost/x/registry 源码核实），向其
// 下发该字段只会造成「以为已防护」的假象，故宁可不发，待节点
// 升级到 1.3.4 后由期望状态重下发自动补上。
func agentSupportsNoPrivateBypass(agentVersion string) bool {
	// R4-N1：agentVersion 是握手上报的 fullVersion（带发行版/架构
	// 后缀），经上报型比较提取首段语义版本；提取失败（含 dev）仍
	// 按不支持处理，fail-closed 口径不变。
	cmp, err := compareWithReportedVersion(flmxNoPrivateMinAgentVersion, agentVersion)
	return err == nil && cmp <= 0
}

// applyFederationExitPrivateGuard 为 exit 角色服务挂上私网目标
// 防护（仅当目标节点 agent 版本支持内置 bypass 时）。
func applyFederationExitPrivateGuard(service map[string]interface{}, role, agentVersion string) {
	if role == "exit" && agentSupportsNoPrivateBypass(agentVersion) {
		service["bypass"] = flmxNoPrivateBypassName
	}
}

func buildFederationServiceConfig(serviceName, addr, protocol, role, chainName string, targetCount int, interfaceName string) map[string]interface{} {
	service := map[string]interface{}{
		"name": serviceName,
		"addr": addr,
		"handler": map[string]interface{}{
			"type": "relay",
		},
		"listener": buildTunnelListenerConfig(protocol),
	}
	// F-UDP-7: 与本地链 builder 共用 tunnelRelayMetadata。
	handlerMetadata := tunnelRelayMetadata(protocol)
	// KU10: relay BIND 逐跳门控、逐跳传递（同
	// buildTunnelChainServiceConfig 注释）：middle 未开 bind 时
	// BIND 在第一跳即被 "relay: BIND is disabled" 拒绝、到不了
	// exit。本 builder 经 applyPeerShareRoleRuntime 只产出
	// middle/exit 两种角色服务（入口硬校验），两者都是 BIND
	// 逐跳路径上的 relay handler，故无条件开 bind opt-in。
	// KU9 的 exit-only 口径已被 Phase F 收尾复验真机证伪，
	// 勿回退。
	handlerMetadata["bind"] = true
	if len(handlerMetadata) > 0 {
		service["handler"].(map[string]interface{})["metadata"] = handlerMetadata
	}
	if role == "middle" {
		service["handler"].(map[string]interface{})["chain"] = chainName
		if targetCount > 1 {
			service["handler"].(map[string]interface{})["retries"] = targetCount - 1
		}
	}
	if role == "exit" && strings.TrimSpace(interfaceName) != "" {
		service["metadata"] = map[string]interface{}{"interface": interfaceName}
	}
	return service
}

// writePeerInternalError 是 peer 面向端点（authPeer 之后）的内部错误
// 出口（D3-08）：原始 DB/存储错误只记日志、只回分类文案，与 authPeer
// 的 O12-001 硬化口径一致。此前 reserve-port / tunnel/create 等端点以
// response.Err(-2, err.Error()) 把 SQLite 约束原文直接回吐给持 token
// 对端（R5 T1.3 实测坐实）。仅用于 peer 端点；管理员端点不在此列。
// 注意：节点命令类错误（节点不在线、agent 返回的业务消息）是消费方
// 排障必需的操作信息，不走本出口、保持原文透传。
func writePeerInternalError(w http.ResponseWriter, op string, err error) {
	log.Printf("federation peer endpoint %s failed: %v", op, err)
	response.WriteJSON(w, response.Err(-2, "Internal error"))
}

// relayAuthConfig 组装 GOST 的内联 auth 配置（handler/connector 的
// auth 字段同形：{username, password}，见 go-gost/x 的 AuthConfig 与
// ParseAutherFromAuth / connector.AuthOption）。凭据不全时返回 nil
// （不下发半截 auth，避免把服务锁死在错误凭据上）。
func relayAuthConfig(username, password string) map[string]interface{} {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil
	}
	return map[string]interface{}{
		"username": username,
		"password": password,
	}
}

// ensurePeerShareRelayCredentials 是 D3-04 凭据的懒补发：share 缺
// relay 凭据时生成并落库（新 share 在创建时生成、存量 share 由
// repo 启动回填，两处之外的路径——如回填失败——由此兜底）。补发
// 失败只记日志并返回现有值：Connect/apply 流程不因凭据落库失败而
// 整体失败，但凭据为空时角色服务不会带 auth（见 apply 侧注入），
// 运维可从日志定位。
func (h *Handler) ensurePeerShareRelayCredentials(share *repo.PeerShare) {
	if share == nil || strings.TrimSpace(share.RelayPassword) != "" {
		return
	}
	userSuffix, err := randomToken(6)
	if err != nil {
		log.Printf("federation: generate relay username for share %d failed: %v", share.ID, err)
		return
	}
	relayPassword, err := randomToken(24)
	if err != nil {
		log.Printf("federation: generate relay password for share %d failed: %v", share.ID, err)
		return
	}
	share.RelayUsername = "share_" + userSuffix
	share.RelayPassword = relayPassword
	share.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.UpdatePeerShareRelayCredentials(share.ID, share.RelayUsername, share.RelayPassword, share.UpdatedTime); err != nil {
		log.Printf("federation: backfill relay credentials for share %d failed: %v", share.ID, err)
	}
}

// validateFederationRoleTargets 校验 middle 角色的 relay 目标
// （D3-04）：目标主机逐个判定——字面 IP 直接判定，主机名解析后逐
// 地址判定，任一命中私网/保留段（netguard，与 SecureFetch 同源）
// 整体拒绝；仅 SecureFetch 的私网放行策略（isPrivateAllowed，
// 同 env）命中时放行，硬拒层前置于该放行（R2X-006）。此前 middle 的 chain 由提供方节点按消费方
// 给定的任意 host:port 出站，等于让持 token 对端借提供方节点探测/
// 连接其内网（agent 侧 bypass 私网过滤需 FIX-H 的 agent 配合，本
// 层校验是面板侧的主要防线）。
func validateFederationRoleTargets(targets []federationRuntimeTarget) error {
	// R2X-006：改走 netguard——硬拒层前置于 isPrivateAllowed（旧口径
	// 元数据在 allow_private 下可被放行），翻译形态按内嵌同判。
	judge := func(host string, ip net.IP) error {
		eff := netguard.EffectiveIP(ip)
		if d := netguard.Judge(ip, netguard.PolicyAllowPrivate, eff != nil && isPrivateAllowed(eff)); !d.Allowed {
			if d.Result.HardDeny {
				return fmt.Errorf("relay target %s is a blocked metadata or hard-denied address", host)
			}
			return fmt.Errorf("relay target %s is a private or reserved address", host)
		}
		return nil
	}
	for _, target := range targets {
		host := strings.TrimSpace(target.Host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if err := judge(host, ip); err != nil {
				return err
			}
			continue
		}
		ips, err := federationDiagnoseLookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("relay target %s cannot be resolved", host)
		}
		for _, ip := range ips {
			if err := judge(host, ip); err != nil {
				return fmt.Errorf("relay target %s resolves to a private or reserved address", host)
			}
		}
	}
	return nil
}

func (h *Handler) federationShareList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	shares, err := h.repo.ListPeerShares()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	items := make([]peerShareListItem, 0, len(shares))
	for i := range shares {
		share := shares[i]
		// R7-014 全集复扫同族：share 的 relay 凭据（D3-04）前端零
		// 消费（管理页只消费 token 作展示/复制，凭据经 Connect 协
		// 议面下发给消费方当事人），不得随列表 DTO 回传——置空
		// 副本字段后再序列化，落库值与 Connect 下发不受影响。
		share.RelayUsername = ""
		share.RelayPassword = ""
		runtimes, err := h.repo.ListActivePeerShareRuntimesByShareID(share.ID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}

		usedSet := make(map[int]struct{}, len(runtimes))
		details := make([]peerShareUsedPort, 0, len(runtimes))
		for _, runtime := range runtimes {
			if runtime.Port > 0 {
				usedSet[runtime.Port] = struct{}{}
			}
			details = append(details, peerShareUsedPort{
				RuntimeID:   runtime.ID,
				Port:        runtime.Port,
				Role:        runtime.Role,
				Protocol:    runtime.Protocol,
				ResourceKey: runtime.ResourceKey,
				Applied:     runtime.Applied,
				UpdatedTime: runtime.UpdatedTime,
			})
		}

		usedPorts := make([]int, 0, len(usedSet))
		for port := range usedSet {
			usedPorts = append(usedPorts, port)
		}
		sort.Ints(usedPorts)

		sort.Slice(details, func(i, j int) bool {
			if details[i].Port == details[j].Port {
				return details[i].RuntimeID < details[j].RuntimeID
			}
			return details[i].Port < details[j].Port
		})

		items = append(items, peerShareListItem{
			PeerShare:        share,
			UsedPorts:        usedPorts,
			UsedPortDetails:  details,
			ActiveRuntimeNum: len(details),
		})
	}

	response.WriteJSON(w, response.OK(items))
}

func (h *Handler) federationShareCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	var req createPeerShareRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	// R2X-004：创建面接入 Idempotency-Key（机制见 idempotency.go）。
	{
		var finish func()
		var replayed bool
		w, finish, replayed = h.idemBegin(w, r, idemUserID(r), "federation-share:create")
		defer finish()
		if replayed {
			return
		}
	}

	if req.Name == "" || req.NodeID == 0 {
		response.WriteJSON(w, response.ErrDefault("Name and NodeID are required"))
		return
	}

	if req.MaxBandwidth < 0 {
		response.WriteJSON(w, response.ErrDefault("Max bandwidth cannot be negative"))
		return
	}

	if req.ExpiryTime < 0 {
		response.WriteJSON(w, response.ErrDefault("Expiry time cannot be negative"))
		return
	}

	if req.PortRangeStart < 0 || req.PortRangeStart > 65535 || req.PortRangeEnd < 0 || req.PortRangeEnd > 65535 {
		response.WriteJSON(w, response.ErrDefault("Invalid port range"))
		return
	}

	if req.PortRangeStart > req.PortRangeEnd {
		response.WriteJSON(w, response.ErrDefault("Port range start cannot be greater than end"))
		return
	}

	allowedIPs, err := normalizePeerShareAllowedIPs(req.AllowedIPs)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// 复扫 R-08：域名白名单与 IP 白名单同口径在写面校验。
	if err := validatePeerShareAllowedDomains(req.AllowedDomains); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	node, err := h.repo.GetNodeByID(req.NodeID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if node == nil {
		response.WriteJSON(w, response.ErrDefault("Node not found"))
		return
	}
	if node.IsRemote == 1 {
		response.WriteJSON(w, response.ErrDefault("Only local nodes can be shared"))
		return
	}

	now := time.Now().UnixMilli()
	token, err := randomToken(32)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	relayUserSuffix, err := randomToken(6)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	relayPassword, err := randomToken(24)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	share := &repo.PeerShare{
		Name:           req.Name,
		NodeID:         req.NodeID,
		Token:          token,
		MaxBandwidth:   req.MaxBandwidth,
		ExpiryTime:     req.ExpiryTime,
		PortRangeStart: req.PortRangeStart,
		PortRangeEnd:   req.PortRangeEnd,
		IsActive:       1,
		CreatedTime:    now,
		UpdatedTime:    now,
		AllowedDomains: req.AllowedDomains,
		AllowedIPs:     allowedIPs,
		// D3-04: 每个 share 创建时即生成独立 relay 凭据。
		RelayUsername: "share_" + relayUserSuffix,
		RelayPassword: relayPassword,
	}

	if err := h.repo.CreatePeerShare(share); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) federationShareDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	var req deletePeerShareRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}

	share, err := h.repo.GetPeerShare(req.ID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if share != nil {
		// Revoke new allocations before cleanup; failed deletions keep the
		// disabled share and its reservations available for retry.
		share.IsActive = 0
		share.UpdatedTime = time.Now().UnixMilli()
		if err := h.repo.UpdatePeerShare(share); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
	}

	if err := h.cleanupPeerShareRuntimes(req.ID); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	failedTunnels, err := h.cleanupFederationTunnels(req.ID)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	if err := h.repo.DeletePeerShare(req.ID); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// R2-F3-02：个别联邦隧道清理失败时共享照常删除（其运行时已在上
	// 一步释放），但未清隧道必须在回执点名披露，不得以无条件成功
	// 掩盖孤儿残留——管理员据 ID 在隧道列表手工清理。
	if len(failedTunnels) > 0 {
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"uncleanedTunnelIds": failedTunnels,
		}))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) federationShareResetFlow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	var req resetPeerShareFlowRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("Share ID is required"))
		return
	}

	share, err := h.repo.GetPeerShare(req.ID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if share == nil {
		response.WriteJSON(w, response.ErrDefault("Share not found"))
		return
	}

	if err := h.repo.ResetPeerShareCurrentFlow(req.ID, time.Now().UnixMilli()); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) federationShareUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	var req updatePeerShareRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	if req.ID <= 0 {
		response.WriteJSON(w, response.ErrDefault("Share ID is required"))
		return
	}

	share, err := h.repo.GetPeerShare(req.ID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}
	if share == nil {
		response.WriteJSON(w, response.ErrDefault("Share not found"))
		return
	}

	if req.Name == "" {
		response.WriteJSON(w, response.ErrDefault("Name is required"))
		return
	}

	if req.MaxBandwidth < 0 {
		response.WriteJSON(w, response.ErrDefault("Max bandwidth cannot be negative"))
		return
	}

	if req.ExpiryTime < 0 {
		response.WriteJSON(w, response.ErrDefault("Expiry time cannot be negative"))
		return
	}

	if req.PortRangeStart < 0 || req.PortRangeStart > 65535 || req.PortRangeEnd < 0 || req.PortRangeEnd > 65535 {
		response.WriteJSON(w, response.ErrDefault("Invalid port range"))
		return
	}

	if req.PortRangeStart > req.PortRangeEnd {
		response.WriteJSON(w, response.ErrDefault("Port range start cannot be greater than end"))
		return
	}

	allowedIPs, err := normalizePeerShareAllowedIPs(req.AllowedIPs)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	// 复扫 R-08：与创建面同口径。
	if err := validatePeerShareAllowedDomains(req.AllowedDomains); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	share.Name = req.Name
	share.MaxBandwidth = req.MaxBandwidth
	share.ExpiryTime = req.ExpiryTime
	share.PortRangeStart = req.PortRangeStart
	share.PortRangeEnd = req.PortRangeEnd
	share.AllowedDomains = req.AllowedDomains
	share.AllowedIPs = allowedIPs
	share.UpdatedTime = time.Now().UnixMilli()

	if err := h.repo.UpdatePeerShare(share); err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// B13-001（R3-K9，分诊 §5.3 裁定）：端口段是分享对对端的授权边界，
	// 段改小后段外存量 runtime 不会被任何对账判据回收（只有停用/过期/
	// 超额），其监听会无限期越界存活。落库后对新段之外的 active
	// runtime 走既有释放路径回收；释放失败的端口在回执中点名，不静默
	// （失败者已置 ReleasePending，由对账继续重试）。
	releasedPorts, failedPorts := h.releaseOutOfRangePeerShareRuntimes(share)
	if len(releasedPorts) == 0 && len(failedPorts) == 0 {
		response.WriteJSON(w, response.OKEmpty())
		return
	}
	data := map[string]interface{}{
		"releasedPorts": releasedPorts,
	}
	if len(failedPorts) > 0 {
		data["releaseFailedPorts"] = failedPorts
		data["warning"] = fmt.Sprintf("端口段已更新，但段外端口 %v 的存量运行时释放失败（已标记待重试），请稍后核对", failedPorts)
	}
	response.WriteJSON(w, response.OK(data))
}

// releaseOutOfRangePeerShareRuntimes 回收分享端口段之外的 active
// runtime（B13-001）：逐条走 releasePeerShareRuntime 既有释放语义
// （本地可释放者即时 CompletePeerShareRuntimeRelease/资源墓碑，带
// service/chain 者下发删除命令，失败置 ReleasePending 待对账重试），
// 返回（已释放端口, 释放失败端口）供调用方在回执中点名。端口段未
// 定义（起止非正）时无授权边界可依，不做任何回收。
func (h *Handler) releaseOutOfRangePeerShareRuntimes(share *repo.PeerShare) (released []int, failed []int) {
	released, failed = []int{}, []int{}
	if h == nil || h.repo == nil || share == nil {
		return released, failed
	}
	if share.PortRangeStart <= 0 || share.PortRangeEnd < share.PortRangeStart {
		return released, failed
	}
	runtimes, err := h.repo.ListActivePeerShareRuntimesByShareID(share.ID)
	if err != nil {
		log.Printf("federation: share %d 改段后枚举段外 runtime 失败: %v", share.ID, err)
		return released, failed
	}
	for i := range runtimes {
		rt := &runtimes[i]
		if rt.Port <= 0 || (rt.Port >= share.PortRangeStart && rt.Port <= share.PortRangeEnd) {
			continue
		}
		if err := h.releasePeerShareRuntime(rt); err != nil {
			log.Printf("federation: share %d 段外 runtime %d（端口 %d）释放失败，已标记待重试: %v", share.ID, rt.ID, rt.Port, err)
			failed = append(failed, rt.Port)
			continue
		}
		released = append(released, rt.Port)
	}
	return released, failed
}

func (h *Handler) federationRemoteUsageList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	remoteNodes, err := h.repo.ListRemoteNodes()
	if err != nil {
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	fc := client.NewFederationClient()
	localDomain := h.federationLocalDomain()

	// F-085: 此前逐节点串行 Connect（单个超时 10s），远端多/慢时接口
	// 线性变慢。改为有界并发预取（上限 10，与 syncRemoteNodeStatuses
	// 的 O12-003 信号量同口径），结果按下标回填，随后仍按原顺序串行
	// 组装——结果顺序与错误语义不变。
	type remoteUsageSync struct {
		shareID        int64
		maxBandwidth   int64
		currentFlow    int64
		expiryTime     int64
		portRangeStart int
		portRangeEnd   int
		syncError      string
		panelVersion   string
		versionWarning string
	}
	syncs := make([]remoteUsageSync, len(remoteNodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 10)
	for i, node := range remoteNodes {
		s := &syncs[i]
		s.shareID, s.maxBandwidth, s.currentFlow, s.expiryTime, s.portRangeStart, s.portRangeEnd = parseRemoteShareUsageConfig(node.RemoteConfig.String)
		url := strings.TrimSpace(node.RemoteURL.String)
		token := strings.TrimSpace(node.RemoteToken.String)
		if url == "" || token == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, nodeID int64, url, token string) {
			defer wg.Done()
			defer func() { <-sem }()
			info, connectErr := fc.Connect(url, token, localDomain)
			if connectErr != nil {
				// R1-FED-01：与状态同步面同映射成码（此前存原文，
				// 用量视图的专设文案分支同样永不可达）。
				if code := federationSyncErrorCode(connectErr.Error()); code != "" {
					syncs[idx].syncError = code
				} else {
					syncs[idx].syncError = connectErr.Error()
				}
				return
			}
			if info == nil {
				return
			}
			syncs[idx].shareID = info.ShareID
			syncs[idx].maxBandwidth = info.MaxBandwidth
			syncs[idx].currentFlow = info.CurrentFlow
			syncs[idx].expiryTime = info.ExpiryTime
			syncs[idx].portRangeStart = info.PortRangeStart
			syncs[idx].portRangeEnd = info.PortRangeEnd
			// FIX2-B（V5-3）：对端版本与版本提示随同步回执呈现 +
			// panelVersion 落 RemoteConfig 留痕。
			syncs[idx].panelVersion = info.PanelVersion
			syncs[idx].versionWarning = info.VersionWarning
			logFederationVersionWarning(info)

			configData, _ := json.Marshal(map[string]interface{}{
				"shareId":        info.ShareID,
				"maxBandwidth":   info.MaxBandwidth,
				"currentFlow":    info.CurrentFlow,
				"expiryTime":     info.ExpiryTime,
				"portRangeStart": info.PortRangeStart,
				"portRangeEnd":   info.PortRangeEnd,
				// D3-04: 同步刷新 relay 凭据（存量 RemoteConfig 的
				// 凭据自愈路径之一，另一处见 applyFederationRuntime）。
				"relayUsername": info.RelayUsername,
				"relayPassword": info.RelayPassword,
				"panelVersion":  info.PanelVersion,
			})
			_ = h.repo.UpdateNodeRemoteConfig(nodeID, string(configData))
		}(i, node.ID, url, token)
	}
	wg.Wait()

	items := make([]remoteUsageNodeItem, 0)
	for i, node := range remoteNodes {
		nodeID := node.ID
		nodeName := node.Name

		shareID := syncs[i].shareID
		maxBandwidth := syncs[i].maxBandwidth
		currentFlow := syncs[i].currentFlow
		expiryTime := syncs[i].expiryTime
		portRangeStart := syncs[i].portRangeStart
		portRangeEnd := syncs[i].portRangeEnd
		syncError := syncs[i].syncError
		url := strings.TrimSpace(node.RemoteURL.String)

		bindingRows, err := h.repo.ListActiveBindingsForNode(nodeID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}
		forwardPortRows, err := h.repo.ListActiveForwardPortsForNode(nodeID)
		if err != nil {
			response.WriteJSON(w, response.Err(-2, err.Error()))
			return
		}

		usedSet := make(map[int]struct{})
		bindings := make([]remoteUsageBindingItem, 0, len(bindingRows)+len(forwardPortRows))
		for _, b := range bindingRows {
			bindings = append(bindings, remoteUsageBindingItem{
				BindingID:       b.ID,
				TunnelID:        b.TunnelID,
				TunnelName:      b.TunnelName,
				ChainType:       b.ChainType,
				HopInx:          b.HopInx,
				AllocatedPort:   b.AllocatedPort,
				ResourceKey:     b.ResourceKey,
				RemoteBindingID: b.RemoteBindingID,
				UpdatedTime:     b.UpdatedTime,
			})
			if b.AllocatedPort > 0 {
				usedSet[b.AllocatedPort] = struct{}{}
			}
		}
		for _, fp := range forwardPortRows {
			bindings = append(bindings, remoteUsageBindingItem{
				BindingID:       -fp.ForwardID,
				TunnelID:        fp.TunnelID,
				TunnelName:      fp.TunnelName,
				ChainType:       1,
				HopInx:          0,
				AllocatedPort:   fp.Port,
				ResourceKey:     fmt.Sprintf("forward:%d", fp.ForwardID),
				RemoteBindingID: "",
				UpdatedTime:     fp.UpdatedTime,
			})
			if fp.Port > 0 {
				usedSet[fp.Port] = struct{}{}
			}
		}

		sort.Slice(bindings, func(i, j int) bool {
			if bindings[i].AllocatedPort == bindings[j].AllocatedPort {
				return bindings[i].BindingID < bindings[j].BindingID
			}
			return bindings[i].AllocatedPort < bindings[j].AllocatedPort
		})

		usedPorts := make([]int, 0, len(usedSet))
		for port := range usedSet {
			usedPorts = append(usedPorts, port)
		}
		sort.Ints(usedPorts)

		items = append(items, remoteUsageNodeItem{
			NodeID:           nodeID,
			NodeName:         nodeName,
			RemoteURL:        url,
			ShareID:          shareID,
			PortRangeStart:   portRangeStart,
			PortRangeEnd:     portRangeEnd,
			MaxBandwidth:     maxBandwidth,
			CurrentFlow:      currentFlow,
			ExpiryTime:       expiryTime,
			UsedPorts:        usedPorts,
			Bindings:         bindings,
			ActiveBindingNum: len(bindings),
			SyncError:        syncError,
			PanelVersion:     syncs[i].panelVersion,
			VersionWarning:   syncs[i].versionWarning,
		})
	}

	response.WriteJSON(w, response.OK(items))
}

func remoteNodePortRange(node *nodeRecord) (int, int) {
	if node == nil || node.IsRemote != 1 || node.RemoteConfig == "" {
		return 0, 0
	}
	_, _, _, _, portRangeStart, portRangeEnd := parseRemoteShareUsageConfig(node.RemoteConfig)
	return portRangeStart, portRangeEnd
}

func validateRemoteNodePort(node *nodeRecord, port int) error {
	if node == nil || node.IsRemote != 1 || port <= 0 {
		return nil
	}
	start, end := remoteNodePortRange(node)
	if start <= 0 || end <= 0 {
		return nil
	}
	if port < start || port > end {
		return fmt.Errorf("远程节点端口 %d 超出允许范围 %d-%d", port, start, end)
	}
	return nil
}

func parseRemoteShareUsageConfig(raw string) (int64, int64, int64, int64, int, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, 0, 0, 0, 0
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return 0, 0, 0, 0, 0, 0
	}

	shareID := asInt64(cfg["shareId"], 0)
	maxBandwidth := asInt64(cfg["maxBandwidth"], 0)
	currentFlow := asInt64(cfg["currentFlow"], 0)
	expiryTime := asInt64(cfg["expiryTime"], 0)
	portRangeStart := int(asInt64(cfg["portRangeStart"], 0))
	portRangeEnd := int(asInt64(cfg["portRangeEnd"], 0))
	return shareID, maxBandwidth, currentFlow, expiryTime, portRangeStart, portRangeEnd
}

// mergeRemoteConnectCredentials 把一次 Connect 回执中的 relay 凭据与
// 对端版本合并进既有 RemoteConfig（保留其余键），供消费方凭据自愈的
// 两条路径共用（建隧道前的 ensureRemoteNodeRelayAuth 与节点状态同步
// syncRemoteNodeStatuses）。R2-F3-01：提供方从旧备份恢复到空库后凭据
// 被重新补发，消费方存量凭据随之失效——旧实现仅在本地密码为空时才
// 刷新（建隧道路径）或只在用量同步时覆写，持旧非空凭据的消费方数据
// 面无限期被拒且面板无感。现两路均在每次成功 Connect 后比对刷新：
// 回执未携带凭据（旧提供方）时保留存量值；凭据或版本变化时落盘并
// 返回合并后的 JSON 与 true（调用方据此更新内存副本）；落盘失败记
// 日志并按未变更处理，下次同步再试，不静默吞。
func (h *Handler) mergeRemoteConnectCredentials(nodeID int64, rawConfig string, info *client.RemoteNodeInfo) (string, bool) {
	if info == nil || strings.TrimSpace(info.RelayPassword) == "" {
		return rawConfig, false
	}
	cfg := map[string]interface{}{}
	if trimmed := strings.TrimSpace(rawConfig); trimmed != "" {
		_ = json.Unmarshal([]byte(trimmed), &cfg)
	}
	oldUser, oldPass := parseRemoteShareRelayAuth(rawConfig)
	changed := oldUser != strings.TrimSpace(info.RelayUsername) || oldPass != strings.TrimSpace(info.RelayPassword)
	cfg["relayUsername"] = info.RelayUsername
	cfg["relayPassword"] = info.RelayPassword
	if panelVersion := strings.TrimSpace(info.PanelVersion); panelVersion != "" {
		if asString(cfg["panelVersion"]) != panelVersion {
			changed = true
		}
		cfg["panelVersion"] = panelVersion
	}
	if !changed {
		return rawConfig, false
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return rawConfig, false
	}
	if err := h.repo.UpdateNodeRemoteConfig(nodeID, string(data)); err != nil {
		log.Printf("federation credential self-heal: persist remote config for node %d: %v", nodeID, err)
		return rawConfig, false
	}
	if oldPass != "" && oldPass != strings.TrimSpace(info.RelayPassword) {
		log.Printf("federation credential self-heal: remote node %d relay credentials rotated by provider, refreshed", nodeID)
	}
	return string(data), true
}

// parseRemoteShareRelayAuth 从远端节点的 RemoteConfig 中取该 share
// 的 relay 凭据（D3-04；Connect 时由 nodeImport / 用量同步写入）。
func parseRemoteShareRelayAuth(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", ""
	}
	return strings.TrimSpace(asString(cfg["relayUsername"])), strings.TrimSpace(asString(cfg["relayPassword"]))
}

var (
	errInvalidRemoteURLFormat = errors.New("Invalid Remote URL format")
	errUnsafeRemoteURL        = errors.New("禁止将远程节点地址设置为内部网络")
)

// validateRemoteNodeURL 是远端节点 URL 的唯一校验实现（D3-02）：
// nodeImport 与 nodeCreate 直填 remoteUrl 共用。格式（http/https
// scheme）+ checkFederationRemoteAddr（解析后逐 IP 判定）两段，
// 任一入口不得绕过。此前 nodeCreate 直填路径零校验，对端给什么就
// 存什么、后续 Connect/同步/命令通道照单全打。
// B13-001：地址判定与联邦传输层同口径（checkFederationRemoteAddr）——
// 硬拒段恒拒，非公网地址在运维显式开启私网放行策略时放行，不再用
// IsSafeRemoteAddr 的公网-only 口径无条件拒绝内网/Tailscale 联邦。
func validateRemoteNodeURL(raw string) error {
	rURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (rURL.Scheme != "http" && rURL.Scheme != "https") || rURL.Host == "" {
		return errInvalidRemoteURLFormat
	}
	if err := checkFederationRemoteAddr(rURL.Host); err != nil {
		return errUnsafeRemoteURL
	}
	return nil
}

func writeRemoteURLValidationError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnsafeRemoteURL) {
		response.WriteJSON(w, response.Err(403, err.Error()))
		return
	}
	response.WriteJSON(w, response.ErrDefault(err.Error()))
}

// findImportedRemoteNode locates an already-imported remote node for
// the same provider share (B13-001 dedupe key: remoteUrl + token or
// shareId). A row matches when its stored remote URL equals the import
// URL (modulo surrounding spaces / trailing slashes) and either the
// stored token equals the presented token, or the shareId recorded in
// its RemoteConfig equals the connected share's ID — the latter covers
// a provider that rotated the token for the same share. Returns the
// node ID, or 0 when no row matches (including on lookup errors, in
// which case the import proceeds to create and surfaces any real DB
// error there).
func (h *Handler) findImportedRemoteNode(remoteURL, token string, shareID int64) int64 {
	rows, err := h.repo.ListRemoteNodes()
	if err != nil {
		return 0
	}
	wantURL := normalizeRemoteURLForMatch(remoteURL)
	for _, row := range rows {
		if !row.RemoteURL.Valid || normalizeRemoteURLForMatch(row.RemoteURL.String) != wantURL {
			continue
		}
		if token != "" && row.RemoteToken.Valid && row.RemoteToken.String == token {
			return row.ID
		}
		if shareID > 0 && row.RemoteConfig.Valid {
			if existingShareID, _, _, _, _, _ := parseRemoteShareUsageConfig(row.RemoteConfig.String); existingShareID == shareID {
				return row.ID
			}
		}
	}
	return 0
}

func normalizeRemoteURLForMatch(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (h *Handler) nodeImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	var req nodeImportRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}

	if req.RemoteURL == "" || req.Token == "" {
		response.WriteJSON(w, response.ErrDefault("Remote URL and Token are required"))
		return
	}
	if err := validateRemoteNodeURL(req.RemoteURL); err != nil {
		writeRemoteURLValidationError(w, err)
		return
	}

	domainCfg, _ := h.repo.GetConfigByName("panel_domain")
	localDomain := ""
	if domainCfg != nil {
		localDomain = domainCfg.Value
	}

	// R8-T-06: the import-node Connect gets the explicit runtime
	// budget instead of the 10s default (see
	// federationRuntimeClientTimeout in control_plane.go).
	fc := client.NewFederationClientWithTimeout(federationRuntimeClientTimeout)
	info, err := fc.Connect(req.RemoteURL, req.Token, localDomain)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, "Failed to connect: "+err.Error()))
		return
	}
	// FIX2-B（V5-3）：对端版本提示记日志并随导入回执返回（加法字段）。
	logFederationVersionWarning(info)

	// Prepare config json for local storage (metadata about limits)
	configData := map[string]interface{}{
		"shareId":        info.ShareID,
		"maxBandwidth":   info.MaxBandwidth,
		"currentFlow":    info.CurrentFlow,
		"expiryTime":     info.ExpiryTime,
		"portRangeStart": info.PortRangeStart,
		"portRangeEnd":   info.PortRangeEnd,
		// D3-04: 该 share 的 relay 凭据，建链时写入 connector auth。
		"relayUsername": info.RelayUsername,
		"relayPassword": info.RelayPassword,
		"panelVersion":  info.PanelVersion,
	}
	configBytes, _ := json.Marshal(configData)

	portRange := "0"
	if info.PortRangeStart > 0 && info.PortRangeEnd >= info.PortRangeStart {
		portRange = fmt.Sprintf("%d-%d", info.PortRangeStart, info.PortRangeEnd)
	}

	now := time.Now().UnixMilli()

	// R3-E-01：对端面板提供的节点地址在本侧唯一落库点——含结构
	// 字符的值不落库。远端节点的 ServerIP 在本侧仅作展示/分享
	// 信息（联邦流量走 RemoteURL），清空不影响导入与用量同步；
	// 合法形态（主机名/IP/回环）原样保留，与保存面同一字符集。
	serverIP := info.ServerIP
	if kernel.HostHasStructuralChars(serverIP) {
		log.Printf("nodeImport: remote node %q reported a structurally invalid server address, storing empty", info.NodeName)
		serverIP = ""
	}

	// B13-001：同一 (remoteUrl, token/shareId) 重复导入不再新建同名
	// 远端节点——命中既有行则原地刷新其远端信息，回执明确提示
	// 「已存在、已刷新」（轻微行为变更，Round 2 分诊已知悉）。
	if existingID := h.findImportedRemoteNode(req.RemoteURL, req.Token, info.ShareID); existingID > 0 {
		if err := h.repo.RefreshRemoteNode(
			existingID,
			fmt.Sprintf("%s (Remote)", info.NodeName),
			serverIP,
			portRange,
			now,
			info.Status,
			req.Token,
			string(configBytes),
		); err != nil {
			response.WriteJSON(w, response.Err(-2, "Database error: "+err.Error()))
			return
		}
		data := map[string]interface{}{
			"refreshed": true,
			"nodeId":    existingID,
			// R2-F3-06：对端版本恒随导入回执呈现（此前仅在有版本
			// 警告时附带，无警告时回执看不到对端版本，与 relay.md
			// 「导入回执可见」不符；用量/状态同步两面本就恒透出）。
			"panelVersion": info.PanelVersion,
		}
		if strings.TrimSpace(info.VersionWarning) != "" {
			data["versionWarning"] = info.VersionWarning
		}
		resp := response.OK(data)
		resp.Msg = "远端节点已存在，已刷新"
		response.WriteJSON(w, resp)
		return
	}

	inx := h.repo.NextIndex("node")
	nodeSecret, randErr := randomToken(16)
	if randErr != nil {
		response.WriteJSON(w, response.Err(-2, randErr.Error()))
		return
	}
	if err = h.repo.CreateRemoteNode(
		fmt.Sprintf("%s (Remote)", info.NodeName),
		nodeSecret,
		serverIP,
		portRange,
		now,
		info.Status,
		inx,
		req.RemoteURL,
		req.Token,
		string(configBytes),
	); err != nil {
		response.WriteJSON(w, response.Err(-2, "Database error: "+err.Error()))
		return
	}

	// R2-F3-06：对端版本恒随导入回执呈现（同刷新路径口径）。
	data := map[string]interface{}{
		"panelVersion": info.PanelVersion,
	}
	if strings.TrimSpace(info.VersionWarning) != "" {
		data["versionWarning"] = info.VersionWarning
	}
	response.WriteJSON(w, response.OK(data))
}

// peerFailLimiter 返回 Handler 的 federation 对端鉴权失败限流器
// （惰性初始化，兼容以结构体字面量构造的测试 Handler）。
func (h *Handler) peerFailLimiter() *flowAuthFailLimiter {
	h.peerAuthFailMu.Lock()
	defer h.peerAuthFailMu.Unlock()
	if h.peerAuthFail == nil {
		h.peerAuthFail = newFlowAuthFailLimiter()
	}
	return h.peerAuthFail
}

// peerAuthThrottleKey 解析本请求的限流键，并报告该来源是否已达
// 对端鉴权失败上限（已达上限时 authPeer 应跳过查库直接回拒）。
// 键走本包统一收口 rateLimitKeyForRequest（与 flow 面同口径）。
func (h *Handler) peerAuthThrottleKey(r *http.Request) (key string, throttled bool) {
	key = rateLimitKeyForRequest(r, "federation peer auth fail limit")
	return key, h.peerFailLimiter().blocked(key, time.Now())
}

// recordPeerAuthFailure 记一次对端鉴权失败（未知令牌）。
// key 必须来自同请求的 peerAuthThrottleKey。
func (h *Handler) recordPeerAuthFailure(key string) {
	h.peerFailLimiter().recordFailure(key, time.Now())
}

func (h *Handler) authPeer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// R3-C1-01：对端令牌是本面（7 条 federation 路由共用此
		// 收口）唯一凭据，未知令牌的每次尝试此前都直达
		// GetPeerShareByToken 查库、无任何失败节流，与 flow 面
		// R8-060 同型。按同口径补齐：复用 flow 限流器类型与同值
		// （60 次/分钟/IP、独立实例），只记未知令牌失败，有效
		// 令牌永不计数；达上限后不再查库，回包与未知令牌同形
		// （401 Invalid token），线协议不变。查库错误回 500 的
		// 路径不记账——非鉴权失败（设计目标见
		// flow_auth_fail_limit.go 文件头）。
		failKey, throttled := h.peerAuthThrottleKey(r)
		if throttled {
			response.WriteJSON(w, response.Err(401, "Invalid token"))
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			response.WriteJSON(w, response.Err(401, "Missing Authorization header"))
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			response.WriteJSON(w, response.Err(401, "Invalid Authorization format"))
			return
		}

		token := parts[1]
		share, err := h.repo.GetPeerShareByToken(token)
		if err != nil {
			// O12-001：DB 错误详情不得返回给未鉴权调用方，记录日志后返回通用 500。
			// 旧逻辑：response.Err(-2, err.Error()) 直接泄露 DB 错误详情。
			log.Printf("authPeer: GetPeerShareByToken failed: %v", err)
			response.WriteJSON(w, response.Err(-2, "Internal error"))
			return
		}
		if share == nil {
			h.recordPeerAuthFailure(failKey)
			response.WriteJSON(w, response.Err(401, "Invalid token"))
			return
		}

		if strings.TrimSpace(share.AllowedIPs) != "" {
			clientIP := federationClientIP(r)
			if clientIP == nil {
				response.WriteJSON(w, response.Err(403, "Unable to determine client IP"))
				return
			}
			if !isPeerIPAllowed(clientIP, share.AllowedIPs) {
				// R5-B2 D5: 拒绝时记录取证信息（解析后 IP、原始头、命中规则）
				log.Printf("federation peer denied ip=%s xff=%q allowed=%q", clientIP.String(), r.Header.Get("X-Forwarded-For"), strings.TrimSpace(share.AllowedIPs))
				response.WriteJSON(w, response.Err(403, "IP not allowed"))
				return
			}
		}

		if share.AllowedDomains != "" {
			clientDomain := r.Header.Get("X-Panel-Domain")
			if clientDomain == "" {
				response.WriteJSON(w, response.Err(403, "Domain verification required"))
				return
			}
			allowed := false
			domains := strings.Split(share.AllowedDomains, ",")
			for _, d := range domains {
				if strings.TrimSpace(d) == clientDomain {
					allowed = true
					break
				}
			}
			if !allowed {
				response.WriteJSON(w, response.Err(403, "Domain not allowed"))
				return
			}
		}

		// R1-FED-01：停用/到期/超额三因分开点名——此前合并为一句
		// "Share is inactive, expired, or over quota"，消费方无从
		// 区分、只能一律映射「已禁用」（其为过期备的专设文案永不
		// 可达）。消费方映射见 federationSyncErrorCode，与本处三
		// 文案逐字对齐；超额文案与 apply-role 面既有文案一致。
		if !isFederationCleanupRequest(r) {
			switch {
			case share.IsActive != 1:
				response.WriteJSON(w, response.Err(403, "Share is inactive"))
				return
			case share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli():
				response.WriteJSON(w, response.Err(403, "Share has expired"))
				return
			case isPeerShareFlowExceeded(share):
				response.WriteJSON(w, response.Err(403, "Share traffic limit exceeded"))
				return
			}
		}

		next(w, r)
	}
}

// Revoked allocation privileges must not revoke cleanup privileges. Inspect
// only supported destructive commands, preserving the body for the handler.
func isFederationCleanupRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/federation/runtime/release-role":
		return true
	case "/api/v1/federation/runtime/command":
		if r.Body == nil {
			return false
		}
		var copied bytes.Buffer
		original := r.Body
		var request federationRuntimeCommandRequest
		err := json.NewDecoder(io.TeeReader(io.LimitReader(original, 1<<20), &copied)).Decode(&request)
		r.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.MultiReader(&copied, original), Closer: original}
		if err != nil || !isFederationRuntimeCommandAllowed(request.CommandType) {
			return false
		}
		_, action := federationResourceCommandKind(request.CommandType)
		return action == "delete"
	default:
		return false
	}
}

// federationRelayAuthPanelVersion 是 D3-04 relay 凭据鉴权上线的面板
// 版本（FIX-I 随 1.3.4 发布）。低于此版本的消费方不识别 Connect 下发
// 的 relay 凭据、connector 恒无 auth，会被已升级提供方的 role 服务
// 拒绝——即 V5-3 的静默断裂格。
const federationRelayAuthPanelVersion = "1.3.4"

// federationVersionWarning 是给「版本过旧的对端」的统一提示文案
// （提供方回执与日志、消费方日志共用）。升级顺序按 V5 纠正口径：
// 先消费方、后提供方（新消费方对旧提供方零断裂，窗口最短）。
func federationVersionWarning(peerVersion string) string {
	return fmt.Sprintf("联邦对端面板版本 %s 低于 %s（relay 凭据鉴权上线版本）：role 链数据面将被拒绝而控制面仍显示正常。请先升级消费方、再升级提供方，并尽快两方同版。", peerVersion, federationRelayAuthPanelVersion)
}

// federationConsumerVersionWarning 判定一次 Connect 的消费方是否过旧：
// 未上报版本 = 凭据感知版本之前的旧消费方，必警告；上报且可解析时
// 低于基线才警告；不可解析或 dev 构建不臆断、不警告（compareVersions
// 把 dev 定义为最低版本是升级防降级的口径，此处不沿用——自构建版本
// 无从判定新旧，警告只会误报）。
func federationConsumerVersionWarning(consumerVersion string) string {
	v := strings.TrimSpace(consumerVersion)
	if v == "" {
		return federationVersionWarning("未上报（< " + federationRelayAuthPanelVersion + "）")
	}
	if v == "dev" {
		return ""
	}
	// R4-N1：对端自述版本可能带后缀（如 fullVersion 形态），按首段
	// 语义版本判定；提取失败维持「不可解析不臆断」口径、不警告。
	// R2-033（B-sub-05）：两处失败原先静默吞掉——本函数是
	// compareVersions 家族唯一吞错消费点，运维侧连「为什么没警告」
	// 都无从查起。现各落一条警告日志，判定与返回值不变（仍不警告）。
	rep, ok := extractLeadingSemver(v)
	if !ok {
		log.Printf("federation: consumer version %q has no leading semantic version, version warning skipped", v)
		return ""
	}
	cmp, err := compareVersions(rep, federationRelayAuthPanelVersion)
	if err != nil {
		log.Printf("federation: consumer version compare failed for %q: %v, version warning skipped", v, err)
		return ""
	}
	if cmp < 0 {
		return federationVersionWarning(v)
	}
	return ""
}

// logFederationVersionWarning 把提供方回执的版本提示记入消费方日志
// （消费方各 Connect 消费点共用；提示本体来自对端回执，不含凭据）。
func logFederationVersionWarning(info *client.RemoteNodeInfo) {
	if info != nil && strings.TrimSpace(info.VersionWarning) != "" {
		log.Printf("federation: provider version warning: %s", strings.TrimSpace(info.VersionWarning))
	}
}

func (h *Handler) federationConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	nodeInfo, err := h.repo.GetNodeBasicInfo(share.NodeID)
	if err != nil {
		response.WriteJSON(w, response.Err(-2, "Node not found"))
		return
	}

	// D3-04: 消费方在 Connect 时取得本 share 的 relay 凭据并存入其
	// RemoteConfig，之后建链时写入 connector auth。存量 share 在此
	// 懒补发（repo 启动回填之外的第二道网）。
	h.ensurePeerShareRelayCredentials(share)

	// FIX2-B（V5-3）：版本可见性——回执恒带本方 panelVersion；消费方
	// 版本缺失（旧版）或低于凭据感知版本时，回执带 versionWarning
	// 且本方记日志。旧消费方忽略未知字段，但提供方侧信号不静默。
	consumerVersion := strings.TrimSpace(r.Header.Get("X-Panel-Version"))
	versionWarning := federationConsumerVersionWarning(consumerVersion)
	if versionWarning != "" {
		log.Printf("federation connect: share_id=%d consumer_panel_version=%q warning=%s", share.ID, consumerVersion, versionWarning)
	}

	data := map[string]interface{}{
		"shareId":        share.ID,
		"shareName":      share.Name,
		"nodeId":         share.NodeID,
		"nodeName":       nodeInfo.Name,
		"relayUsername":  share.RelayUsername,
		"relayPassword":  share.RelayPassword,
		"serverIp":       nodeInfo.ServerIP,
		"status":         nodeInfo.Status,
		"maxBandwidth":   share.MaxBandwidth,
		"currentFlow":    share.CurrentFlow,
		"expiryTime":     share.ExpiryTime,
		"portRangeStart": share.PortRangeStart,
		"portRangeEnd":   share.PortRangeEnd,
		"panelVersion":   currentPanelVersion(),
	}
	if versionWarning != "" {
		data["versionWarning"] = versionWarning
	}
	response.WriteJSON(w, response.OK(data))
}

func (h *Handler) federationTunnelCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}
	if isPeerShareFlowExceeded(share) {
		response.WriteJSON(w, response.Err(403, "Share traffic limit exceeded"))
		return
	}

	var req federationTunnelRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}

	// F-UDP-4: protocol 落入隧道记录后会作为段协议参与后续生成，
	// 空值沿既有默认（tcp），非空必须落在合法集内并小写归一。
	if strings.TrimSpace(req.Protocol) != "" {
		if err := validateTunnelProtocol(req.Protocol); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		req.Protocol = normalizeTunnelProtocol(req.Protocol)
	}

	// D-03：分享端口段未设置（0-0）或半设置时 fail-closed，与
	// pickPeerSharePort 同口径；此前仅凭 RemotePort 与段端比较，
	// RemotePort=0 在 0-0 下两边都不成立而漏过，生成 Port=0 的 runtime。
	if share.PortRangeStart <= 0 || share.PortRangeEnd <= 0 || share.PortRangeEnd < share.PortRangeStart {
		response.WriteJSON(w, response.Err(403, "No available port"))
		return
	}
	if req.RemotePort < share.PortRangeStart || req.RemotePort > share.PortRangeEnd {
		response.WriteJSON(w, response.Err(403, "Port out of range"))
		return
	}

	// 复扫 R-07：对端提交的转发目标与本地 forwardCreate 同口径补
	// 语法层空标签判（只判语法、不做 DNS）；空值放行（既有语义）。
	if err := validateNoEmptyLabelAddr(req.Target); err != nil {
		response.WriteJSON(w, response.ErrDefault("转发目标地址主机名格式无效："+err.Error()))
		return
	}

	// B4/F-031: 「查占用 → 建隧道+runtime」必须与兄弟入口
	// federationRuntimeReservePort 一样在 peerRoleRuntimeMu 内串行，
	// 此前无锁：两个并发请求可同时通过三处占用检查，为同节点同口
	// 建出两条联邦隧道。
	peerRoleRuntimeMu.Lock()
	defer peerRoleRuntimeMu.Unlock()

	// X-P2-13（L-08-1）：联邦端口分配接入 per-node 端口锁，与本地
	// forwardCreate 的锁内复检同域串行（锁序恒为 peerRoleRuntimeMu →
	// portAllocLock；本地路径从不取 peerRoleRuntimeMu，无反序）。
	portMu := repo.AcquirePortAllocLock(share.NodeID)
	defer repo.ReleasePortAllocLock(portMu)

	// R10-07（I-C2-02）：重投回放。首投已建成隧道+runtime、回执在网络
	// 上丢失（客户端调用有专设超时）时本地按失败返回，用户同表单重试
	// 曾被下方三处占位检查恒 403 挡死——该联邦隧道再无法经正常路径建
	// 成、对端留孤儿隧道+端口占用须人工清理。故占位检查之前先查同
	// (share, port) 的既有 tunnel/create 产物：Target 与 Protocol 与
	// 首投一致即回放既有 tunnelId（与 reserve-port 的同 ResourceKey
	// 回放同口径）；不一致维持 403 防占用。
	existingRuntime, err := h.repo.GetActiveForwardPeerShareRuntimeByPort(share.ID, req.RemotePort)
	if err != nil {
		writePeerInternalError(w, "tunnel/create runtime lookup", err)
		return
	}
	if existingRuntime != nil {
		if tunnelID, ok := h.federationTunnelReplayID(existingRuntime, share.ID, req); ok {
			response.WriteJSON(w, response.OK(map[string]interface{}{
				"tunnelId": tunnelID,
			}))
			return
		}
	}

	usedPorts, err := h.repo.ListUsedPortsOnNode(share.NodeID)
	if err != nil {
		writePeerInternalError(w, "tunnel/create list used ports", err)
		return
	}
	for _, port := range usedPorts {
		if port == req.RemotePort {
			response.WriteJSON(w, response.Err(403, "Port already in use"))
			return
		}
	}

	if existingRuntime != nil {
		response.WriteJSON(w, response.Err(403, "Port already in use"))
		return
	}
	existsOnNodePort, err := h.repo.ExistsActivePeerShareRuntimeOnNodePort(share.NodeID, req.RemotePort)
	if err != nil {
		writePeerInternalError(w, "tunnel/create runtime port check", err)
		return
	}
	if existsOnNodePort {
		response.WriteJSON(w, response.Err(403, "Port already in use"))
		return
	}

	now := time.Now().UnixMilli()
	tunnelID, err := h.repo.CreateFederationTunnel(
		fmt.Sprintf("Share-%d-Port-%d", share.ID, req.RemotePort),
		1,
		req.Protocol,
		now,
		share.NodeID,
		req.RemotePort,
	)
	if err != nil {
		writePeerInternalError(w, "tunnel/create create tunnel", err)
		return
	}

	reservationID, randErr := randomToken(24)
	if randErr != nil {
		h.rollbackFailedFederationTunnel(w, "tunnel/create reservation id", tunnelID, randErr)
		return
	}
	runtime := &repo.PeerShareRuntime{
		ShareID:       share.ID,
		NodeID:        share.NodeID,
		ReservationID: reservationID,
		ResourceKey:   fmt.Sprintf("federation-forward-%d-%d-%d", share.ID, tunnelID, req.RemotePort),
		BindingID:     "",
		Role:          "forward",
		ChainName:     "",
		ServiceName:   "",
		Protocol:      defaultString(req.Protocol, "tcp"),
		Strategy:      "fifo",
		Port:          req.RemotePort,
		Target:        strings.TrimSpace(req.Target),
		Applied:       0,
		Status:        1,
		CreatedTime:   now,
		UpdatedTime:   now,
	}
	if err := h.repo.CreatePeerShareRuntime(runtime); err != nil {
		h.rollbackFailedFederationTunnel(w, "tunnel/create create runtime", tunnelID, err)
		return
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"tunnelId": tunnelID,
	}))
}

// rollbackFailedFederationTunnel 是 federationTunnelCreate 在隧道行已
// 建、后续步骤失败时的回滚出口（R2-F3-05）：回滚删除成功走既有内部
// 错误出口；回滚本身失败时不许静默留孤儿——日志记全错误，回执按
// D3-08 口径只回分类文案但点名孤儿隧道 ID，供消费方向提供方管理员
// 报障时定位清理（原始存储错误不回吐对端）。
func (h *Handler) rollbackFailedFederationTunnel(w http.ResponseWriter, op string, tunnelID int64, cause error) {
	if derr := h.deleteTunnelByID(tunnelID); derr != nil {
		log.Printf("federation %s: rollback delete of tunnel %d failed: %v (original error: %v); orphaned tunnel requires manual cleanup", op, tunnelID, derr, cause)
		response.WriteJSON(w, response.Err(-2, fmt.Sprintf("Internal error: tunnel %d was created but cleanup after a failed create did not complete; the orphaned tunnel must be removed manually", tunnelID)))
		return
	}
	writePeerInternalError(w, op, cause)
}

// normalizeFederationTarget 把联邦转发目标归一为可比形态（R2-F3-04）：
// 主机名大小写不敏感、主机名尾随点等价、端口按数值归一（前导零等
// 价）、IP 字面量按解析后标准形。无法按 host:port 解析时退化为去空
// 格＋小写＋去尾点比较。回放判定两侧同走本归一，避免同义不同写被
// 判为不同请求而漏回放落 403；落库的目标原文不改。
func normalizeFederationTarget(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return strings.TrimSuffix(strings.ToLower(s), ".")
	}
	host = strings.TrimSpace(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
	}
	if port, perr := strconv.Atoi(portStr); perr == nil {
		portStr = strconv.Itoa(port)
	}
	return net.JoinHostPort(host, portStr)
}

func federationTargetsEqual(a, b string) bool {
	return normalizeFederationTarget(a) == normalizeFederationTarget(b)
}

// parseFederationForwardResourceKey extracts the tunnel ID recorded in a
// tunnel/create runtime's ResourceKey
// ("federation-forward-<shareID>-<tunnelID>-<port>"). Only keys minted by
// federationTunnelCreate itself parse; forward service-binding runtimes
// ("forward-runtime:…") and any other shape are rejected, as are keys
// whose share/port do not match the lookup.
func parseFederationForwardResourceKey(resourceKey string, shareID int64, port int) (int64, bool) {
	parts := strings.Split(resourceKey, "-")
	if len(parts) != 5 || parts[0] != "federation" || parts[1] != "forward" {
		return 0, false
	}
	keyShare, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || keyShare != shareID {
		return 0, false
	}
	tunnelID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || tunnelID <= 0 {
		return 0, false
	}
	keyPort, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || keyPort != int64(port) {
		return 0, false
	}
	return tunnelID, true
}

// federationTunnelReplayID decides whether an active forward runtime on
// (shareID, req.RemotePort) is the product of an earlier tunnel/create
// carrying the same target and protocol — i.e. this request is a retry
// of a create whose response was lost — and if so returns the original
// tunnel ID to replay (R10-07 / I-C2-02). Any mismatch (a runtime left
// by service binding, a release in flight, a different target/protocol,
// or a tunnel row deleted locally in the meantime) is not a replay: the
// caller's ordinary occupancy checks then decide, preserving the 403.
func (h *Handler) federationTunnelReplayID(runtime *repo.PeerShareRuntime, shareID int64, req federationTunnelRequest) (int64, bool) {
	if runtime == nil || runtime.ReleasePending != 0 {
		return 0, false
	}
	tunnelID, ok := parseFederationForwardResourceKey(runtime.ResourceKey, shareID, req.RemotePort)
	if !ok {
		return 0, false
	}
	if !federationTargetsEqual(runtime.Target, req.Target) {
		return 0, false
	}
	if runtime.Protocol != defaultString(req.Protocol, "tcp") {
		return 0, false
	}
	name, err := h.repo.GetTunnelName(tunnelID)
	if err != nil {
		return 0, false
	}
	if synthetic := fmt.Sprintf("Share-%d-Port-%d", shareID, req.RemotePort); name != synthetic {
		// R2-F3-03 收养：隧道身份由 runtime 的 ResourceKey 恒记
		// (share, tunnelId, port)，名称只是初建时的合成标签。提供方
		// 管理员在本地改名后，旧实现以名称不等判非回放、同参重试落
		// 403 且孤儿无从收养——现同 share/同参数（目标经归一、协议）
		// 即视为同一笔建隧道的重投回放，改名一事记日志留痕。
		log.Printf("federation tunnel/create: replay adopted renamed tunnel %d (share %d, port %d, current name %q)", tunnelID, shareID, req.RemotePort, name)
	}
	return tunnelID, true
}

func (h *Handler) federationRuntimeReservePort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	var req federationRuntimeReservePortRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	req.ResourceKey = strings.TrimSpace(req.ResourceKey)
	if req.ResourceKey == "" {
		response.WriteJSON(w, response.ErrDefault("resourceKey is required"))
		return
	}

	// F-UDP-4: 预留的 protocol 将原样成为角色服务的 listener 类型，
	// 落库前按合法集校验（空值沿既有默认 tls）并小写归一。
	req.Protocol = normalizeTunnelProtocol(defaultString(req.Protocol, "tls"))
	if err := validateTunnelProtocol(req.Protocol); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	peerRoleRuntimeMu.Lock()
	defer peerRoleRuntimeMu.Unlock()
	// X-P2-13（L-08-1）：同 federationTunnelCreate，接入 per-node 端口锁。
	portMu := repo.AcquirePortAllocLock(share.NodeID)
	defer repo.ReleasePortAllocLock(portMu)
	share, err = h.repo.GetPeerShare(share.ID)
	if err != nil || share == nil || share.IsActive != 1 || (share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli()) {
		response.WriteJSON(w, response.Err(403, "Share is unavailable"))
		return
	}

	if isPeerShareFlowExceeded(share) {
		response.WriteJSON(w, response.Err(403, "Share traffic limit exceeded"))
		return
	}

	// B13-003：分配前惰性回收本 share 的过期纯预留，弃单端口先回到
	// 可用池再参与本次 pickPeerSharePort 的占用计算。回收失败只记
	// 日志、不挡本次预留（最坏情况退回旧行为：过期行继续占位）。
	if expired, expErr := h.repo.ExpireStalePeerShareReservations(share.ID, time.Now().Add(-peerShareReservationTTL).UnixMilli()); expErr != nil {
		log.Printf("federation: expire stale reservations for share %d failed: %v", share.ID, expErr)
	} else if expired > 0 {
		log.Printf("federation: expired %d stale reservation(s) for share %d", expired, share.ID)
	}

	existing, err := h.repo.GetPeerShareRuntimeByResourceKey(share.ID, req.ResourceKey)
	if err != nil {
		writePeerInternalError(w, "reserve-port runtime lookup", err)
		return
	}
	if existing != nil && existing.ReleasePending != 0 {
		response.WriteJSON(w, response.ErrDefault("Runtime release is pending"))
		return
	}
	if existing != nil && existing.Status == 1 {
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"reservationId": existing.ReservationID,
			"allocatedPort": existing.Port,
			"bindingId":     existing.BindingID,
		}))
		return
	}

	allocatedPort, err := h.pickPeerSharePort(share, req.RequestedPort)
	if err != nil {
		// pickPeerSharePort 的分类文案（无可用端口/越界）是给对端的操作
		// 信息，保持透传；其余为 repo 原始错误，按 D3-08 脱敏。
		switch err.Error() {
		case "No available port", "Port out of range", "share not found":
			response.WriteJSON(w, response.ErrDefault(err.Error()))
		default:
			writePeerInternalError(w, "reserve-port pick port", err)
		}
		return
	}

	now := time.Now().UnixMilli()
	if existing != nil {
		reservationID, randErr := randomToken(24)
		if randErr != nil {
			writePeerInternalError(w, "reserve-port reservation id", randErr)
			return
		}
		existing.ReservationID = reservationID
		existing.Protocol = defaultString(req.Protocol, "tls")
		existing.Port = allocatedPort
		existing.BindingID = ""
		existing.Role = ""
		existing.ChainName = ""
		existing.ServiceName = ""
		existing.Strategy = "round"
		existing.Target = ""
		existing.Applied = 0
		existing.Status = 1
		existing.UpdatedTime = now
		if err := h.repo.UpdatePeerShareRuntime(existing); err != nil {
			writePeerInternalError(w, "reserve-port runtime update", err)
			return
		}
		response.WriteJSON(w, response.OK(map[string]interface{}{
			"reservationId": existing.ReservationID,
			"allocatedPort": existing.Port,
			"bindingId":     existing.BindingID,
		}))
		return
	}

	reservationID, randErr := randomToken(24)
	if randErr != nil {
		writePeerInternalError(w, "reserve-port reservation id", randErr)
		return
	}
	runtime := &repo.PeerShareRuntime{
		ShareID:       share.ID,
		NodeID:        share.NodeID,
		ReservationID: reservationID,
		ResourceKey:   req.ResourceKey,
		BindingID:     "",
		Role:          "",
		ChainName:     "",
		ServiceName:   "",
		Protocol:      defaultString(req.Protocol, "tls"),
		Strategy:      "round",
		Port:          allocatedPort,
		Target:        "",
		Applied:       0,
		Status:        1,
		CreatedTime:   now,
		UpdatedTime:   now,
	}
	if err := h.repo.CreatePeerShareRuntime(runtime); err != nil {
		writePeerInternalError(w, "reserve-port runtime create", err)
		return
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"reservationId": runtime.ReservationID,
		"allocatedPort": runtime.Port,
		"bindingId":     runtime.BindingID,
	}))
}

func (h *Handler) federationRuntimeApplyRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	var req federationRuntimeApplyRoleRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Role != "middle" && req.Role != "exit" {
		response.WriteJSON(w, response.ErrDefault("Invalid role"))
		return
	}

	// F-UDP-4: 本次请求携带的 protocol（角色协议与每个 target 的
	// 拨号协议）落库/生成前必须落在合法集内并小写归一；空值沿
	// 既有继承语义（target 继承角色协议、角色协议继承预留值），
	// 继承后的生效值在 runtime 载入后另行校验。
	if strings.TrimSpace(req.Protocol) != "" {
		if err := validateTunnelProtocol(req.Protocol); err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		req.Protocol = normalizeTunnelProtocol(req.Protocol)
	}
	// R1-03-L1：strategy 同口径立门——合法集本体是
	// kernel.PeerRuntimeStrategyLegal 单点。此前原文落库，集外值
	// 经联邦链渲染直达 agent 后被 default 为轮询（声明≠生效、无
	// 回执）。空值沿既有缺省语义（下方 defaultString 落 round），
	// 非空先 TrimSpace 再判、落库归一值。
	if strings.TrimSpace(req.Strategy) != "" {
		req.Strategy = strings.TrimSpace(req.Strategy)
		if !kernel.PeerRuntimeStrategyLegal(req.Strategy) {
			response.WriteJSON(w, response.ErrDefault(fmt.Sprintf("不支持的负载策略 %q，合法值为 fifo、round、rand、hash、best", req.Strategy)))
			return
		}
	}
	for i := range req.Targets {
		if strings.TrimSpace(req.Targets[i].Protocol) != "" {
			if err := validateTunnelProtocol(req.Targets[i].Protocol); err != nil {
				response.WriteJSON(w, response.ErrDefault(err.Error()))
				return
			}
			req.Targets[i].Protocol = normalizeTunnelProtocol(req.Targets[i].Protocol)
		}
	}

	peerRoleRuntimeMu.Lock()
	defer peerRoleRuntimeMu.Unlock()
	share, err = h.repo.GetPeerShare(share.ID)
	if err != nil || share == nil || share.IsActive != 1 || (share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli()) {
		response.WriteJSON(w, response.ErrDefault("Share is unavailable"))
		return
	}

	var runtime *repo.PeerShareRuntime
	if strings.TrimSpace(req.ReservationID) != "" {
		runtime, err = h.repo.GetPeerShareRuntimeByReservationID(share.ID, strings.TrimSpace(req.ReservationID))
	} else {
		runtime, err = h.repo.GetPeerShareRuntimeByResourceKey(share.ID, strings.TrimSpace(req.ResourceKey))
	}
	if err != nil {
		writePeerInternalError(w, "apply-role runtime lookup", err)
		return
	}
	if runtime == nil || runtime.Status == 0 {
		response.WriteJSON(w, response.ErrDefault("Reservation not found"))
		return
	}

	// F-UDP-4: 生效协议（含修复前已落库的继承值）同样必须合法，
	// 防止存量非法值绕过请求面校验继续直达 agent 生成。
	if err := validateTunnelProtocol(defaultString(req.Protocol, runtime.Protocol)); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	if runtime.ReleasePending != 0 {
		response.WriteJSON(w, response.ErrDefault("Runtime release is pending"))
		return
	}
	// B13-003：纯预留过期后不得再被 apply——先把该行本地释放，再按
	// 预留不存在处理（对端可重新 reserve）。已带 role 的 runtime 是
	// 在途/已生效的期望状态，不在此列（重复 apply 是修复路径）。
	if peerShareReservationExpired(runtime, time.Now()) {
		if err := h.repo.CompletePeerShareRuntimeRelease(runtime.ID); err != nil {
			writePeerInternalError(w, "apply-role expire stale reservation", err)
			return
		}
		response.WriteJSON(w, response.ErrDefault("Reservation not found"))
		return
	}
	if isPeerShareFlowExceeded(share) {
		response.WriteJSON(w, response.Err(403, "Share traffic limit exceeded"))
		return
	}
	if runtime.Role != "" && runtime.Role != req.Role {
		response.WriteJSON(w, response.ErrDefault("Runtime role cannot change without release"))
		return
	}
	if share.PortRangeStart > 0 && share.PortRangeEnd > 0 && (runtime.Port < share.PortRangeStart || runtime.Port > share.PortRangeEnd) {
		response.WriteJSON(w, response.ErrDefault("Reserved port is outside share range"))
		return
	}
	// D3-04: middle 的 relay 目标不得指向提供方内网/保留地址。
	if req.Role == "middle" {
		if err := validateFederationRoleTargets(req.Targets); err != nil {
			response.WriteJSON(w, response.Err(403, err.Error()))
			return
		}
	}
	// LB-REQ-05：binding_id 在下方快照点之前生成，先捕获原值供失败
	// 回滚恢复（纯预留的 binding_id 本应为空，reserve-port 重用行时
	// 亦按空重置——它属于「纯预留状态」定义的一部分）。
	prevBindingID := runtime.BindingID
	if strings.TrimSpace(runtime.BindingID) == "" {
		bindingID, randErr := randomToken(24)
		if randErr != nil {
			response.WriteJSON(w, response.Err(-2, randErr.Error()))
			return
		}
		runtime.BindingID = bindingID
	}
	// B13-002：apply 前快照本次要改写的占用字段。applyPeerShareRoleRuntime
	// 会先把期望状态落库再下发命令（为崩溃恢复保留期望状态）；但对
	// 「首次 apply 的纯预留」而言，同步 API 一旦失败即向对端返回失败，
	// 若不回滚，预留会停在 role/service_name 已占用、applied=0 的
	// 终态——换角色重试被 role-cannot-change 拒、release 与 share
	// 删除又被离线节点卡死，端口无限期占用。故仅当 apply 前是纯预留
	// （无 role/service/chain）时，失败回滚到快照，预留回到可重新
	// apply 或可本地释放的状态。已有 role 的 runtime（曾成功下发或
	// 期望状态待对账）失败时保留新期望状态待重连恢复——那是
	// TestPeerShareUnacknowledgedApplyRecoversOrReleases 锁定的既有
	// 契约，本回滚不得触碰。
	// LB-REQ-05：快照集必须覆盖本 handler 在 apply 路径上改写并经
	// UpdatePeerShareRuntime 落库的全部字段——除上述五项外还有
	// protocol/strategy（下方改写，漏回滚时失败尝试的值会残留：
	// 重试不带 protocol 经 defaultString 继承残留值并下发）与
	// binding_id（上方生成，prevBindingID 已在生成前捕获）。
	prevRole, prevServiceName, prevChainName, prevTarget, prevApplied, prevProtocol, prevStrategy := runtime.Role, runtime.ServiceName, runtime.ChainName, runtime.Target, runtime.Applied, runtime.Protocol, runtime.Strategy
	wasPlainReservation := prevRole == "" && prevServiceName == "" && prevChainName == ""
	runtime.Role = req.Role
	runtime.ServiceName = fmt.Sprintf("fed_svc_%d", runtime.ID)
	runtime.ChainName = ""
	if req.Role == "middle" {
		runtime.ChainName = federationRuntimeChainName(runtime.BindingID)
	}
	runtime.Protocol = normalizeTunnelProtocol(defaultString(req.Protocol, runtime.Protocol))
	runtime.Strategy = defaultString(req.Strategy, "round")
	targetBytes, _ := json.Marshal(req.Targets)
	runtime.Target = string(targetBytes)
	if err := h.applyPeerShareRoleRuntime(runtime); err != nil {
		if wasPlainReservation {
			runtime.Role, runtime.ServiceName, runtime.ChainName, runtime.Target, runtime.Applied, runtime.Protocol, runtime.Strategy, runtime.BindingID = prevRole, prevServiceName, prevChainName, prevTarget, prevApplied, prevProtocol, prevStrategy, prevBindingID
			runtime.UpdatedTime = time.Now().UnixMilli()
			if rbErr := h.repo.UpdatePeerShareRuntime(runtime); rbErr != nil {
				log.Printf("federation: rollback failed apply for runtime %d failed: %v (apply error: %v)", runtime.ID, rbErr, err)
			}
		}
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	response.WriteJSON(w, response.OK(map[string]interface{}{
		"bindingId":     runtime.BindingID,
		"reservationId": runtime.ReservationID,
		"allocatedPort": runtime.Port,
	}))
}

func (h *Handler) federationRuntimeReleaseRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	var req federationRuntimeReleaseRoleRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}

	var runtime *repo.PeerShareRuntime
	if strings.TrimSpace(req.BindingID) != "" {
		runtime, err = h.repo.GetPeerShareRuntimeByBindingID(share.ID, strings.TrimSpace(req.BindingID))
	} else if strings.TrimSpace(req.ReservationID) != "" {
		runtime, err = h.repo.GetPeerShareRuntimeByReservationID(share.ID, strings.TrimSpace(req.ReservationID))
	} else if strings.TrimSpace(req.ResourceKey) != "" {
		runtime, err = h.repo.GetPeerShareRuntimeByResourceKey(share.ID, strings.TrimSpace(req.ResourceKey))
	} else {
		response.WriteJSON(w, response.ErrDefault("bindingId or reservationId or resourceKey is required"))
		return
	}
	if err != nil {
		writePeerInternalError(w, "release-role runtime lookup", err)
		return
	}
	if runtime == nil {
		response.WriteJSON(w, response.OKEmpty())
		return
	}

	if err := h.releasePeerShareRuntime(runtime); err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}

	response.WriteJSON(w, response.OKEmpty())
}

func (h *Handler) federationRuntimeDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	var req federationRuntimeDiagnoseRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}

	req.IP = strings.TrimSpace(req.IP)
	if req.IP == "" || req.Port <= 0 || req.Port > 65535 {
		response.WriteJSON(w, response.ErrDefault("Invalid target"))
		return
	}
	pinnedIP, targetErr := resolveFederationDiagnoseTarget(req.IP)
	if targetErr != nil {
		log.Printf("federation diagnose target rejected peer_share=%d name=%q target=%s reason=%v", share.ID, share.Name, req.IP, targetErr)
		response.WriteJSON(w, response.ErrDefault(targetErr.Error()))
		return
	}
	// D3-07: 下发钉死后的 IP（原始目标仅保留在响应语境/日志中）。
	req.IP = pinnedIP
	if req.Count <= 0 {
		req.Count = 4
	}
	if req.Count > maxFederationDiagnoseCount {
		req.Count = maxFederationDiagnoseCount
	}
	if req.Timeout <= 0 || req.Timeout > int(diagnosisCommandTimeout/time.Millisecond) {
		req.Timeout = int(diagnosisCommandTimeout / time.Millisecond)
	}
	commandTimeout := time.Duration(req.Timeout) * time.Millisecond
	if commandTimeout <= 0 || commandTimeout > diagnosisCommandTimeout {
		commandTimeout = diagnosisCommandTimeout
	}

	data := map[string]interface{}{
		"ip":      req.IP,
		"port":    req.Port,
		"count":   req.Count,
		"timeout": req.Timeout,
	}
	var res ws.CommandResult
	if isUDPBasedProtocol(req.Protocol) {
		res, err = h.sendNodeCommandWithTimeout(share.NodeID, "UdpPing", data, commandTimeout, false, false)
	} else {
		// DB-5：TCP 诊断走探测槽位，排队与执行共用 commandTimeout 预算。
		res, err = h.sendTCPPing(share.NodeID, data, commandTimeout)
	}
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	if res.Data == nil {
		response.WriteJSON(w, response.ErrDefault("Node did not return diagnosis data"))
		return
	}

	response.WriteJSON(w, response.OK(res.Data))
}

func (h *Handler) federationRuntimeCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("Invalid method"))
		return
	}

	token := extractBearerToken(r)
	share, err := h.repo.GetPeerShareByToken(token)
	if err != nil || share == nil {
		response.WriteJSON(w, response.Err(401, "Unauthorized"))
		return
	}

	var req federationRuntimeCommandRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		response.WriteJSON(w, response.ErrDefault("Invalid JSON"))
		return
	}
	cmd := strings.TrimSpace(req.CommandType)
	if cmd == "" {
		response.WriteJSON(w, response.ErrDefault("commandType is required"))
		return
	}
	if !isFederationRuntimeCommandAllowed(cmd) {
		response.WriteJSON(w, response.ErrDefault("command not allowed"))
		return
	}

	h.peerResourceMu.Lock()
	defer h.peerResourceMu.Unlock()
	// Recheck after acquiring the mutation lock: an earlier authentication
	// decision cannot authorize a recreation after quota/expiry cleanup.
	share, err = h.repo.GetPeerShare(share.ID)
	if err != nil || share == nil {
		response.WriteJSON(w, response.ErrDefault("share ownership unavailable"))
		return
	}
	if isFederationServiceCommand(cmd) {
		if err := validateFederationCommandPorts(share, req.Data); err != nil {
			response.WriteJSON(w, response.Err(403, err.Error()))
			return
		}
	}

	_, action := federationResourceCommandKind(cmd)
	if action != "delete" && (share.IsActive != 1 || (share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli()) || isPeerShareFlowExceeded(share)) {
		response.WriteJSON(w, response.Err(403, "share is inactive, expired, or over quota"))
		return
	}
	if strings.EqualFold(cmd, "tcpping") {
		// F-086 变体：此分支此前把 req.Data 直通节点，与 diagnose
		// 入口同口径校验目标地址并钳制 count 后再下发。
		dataMap, _ := req.Data.(map[string]interface{})
		targetIP := strings.TrimSpace(asString(dataMap["ip"]))
		if targetIP == "" {
			response.WriteJSON(w, response.ErrDefault("tcpping 需要目标 ip"))
			return
		}
		pinnedIP, targetErr := resolveFederationDiagnoseTarget(targetIP)
		if targetErr != nil {
			log.Printf("federation runtime command tcpping target rejected peer_share=%d name=%q target=%s reason=%v", share.ID, share.Name, targetIP, targetErr)
			response.WriteJSON(w, response.ErrDefault(targetErr.Error()))
			return
		}
		clampedCount := asInt(dataMap["count"], 0)
		if clampedCount <= 0 {
			clampedCount = 4
		}
		if clampedCount > maxFederationDiagnoseCount {
			clampedCount = maxFederationDiagnoseCount
		}
		// D3-07: 与 diagnose 入口同口径，下发钉死后的 IP。
		dataMap["ip"] = pinnedIP
		dataMap["count"] = clampedCount
		req.Data = dataMap
		res, err := h.sendTCPPing(share.NodeID, req.Data, defaultNodeCommandTimeout)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.OK(res))
		return
	}
	items, err := h.preparePeerResourceCommand(share, cmd, req.Data)
	if err != nil {
		response.WriteJSON(w, response.ErrDefault(err.Error()))
		return
	}
	var result interface{} = map[string]interface{}{"success": true}
	for _, item := range items {
		res, err := h.applyPeerShareResource(item)
		if err != nil {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		result = res
	}
	response.WriteJSON(w, response.OK(result))
}

type federationForwardServiceBinding struct {
	Name string
	Port int
}

func extractFederationServiceEntries(data interface{}) []map[string]interface{} {
	if data == nil {
		return nil
	}

	if entries := asMapSlice(data); len(entries) > 0 {
		return entries
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return nil
	}

	if entries := asMapSlice(dataMap["services"]); len(entries) > 0 {
		return entries
	}

	return nil
}

func parseFederationForwardServiceBindings(data interface{}) []federationForwardServiceBinding {
	serviceList := extractFederationServiceEntries(data)
	bindings := make([]federationForwardServiceBinding, 0, len(serviceList))
	for _, svcMap := range serviceList {
		name := normalizeForwardRuntimeServiceName(asString(svcMap["name"]))
		originalName := name
		if shareID, original, ok := parsePeerShareServiceName(asString(svcMap["name"])); ok {
			originalName = normalizeForwardRuntimeServiceName(original)
			name = peerShareResourceName(shareID, "service", originalName)
		}
		if name == "" {
			continue
		}
		if _, _, _, ok := parseFlowServiceIDs(originalName); !ok {
			continue
		}
		addr := strings.TrimSpace(asString(svcMap["addr"]))
		if addr == "" {
			continue
		}
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			continue
		}
		bindings = append(bindings, federationForwardServiceBinding{Name: name, Port: port})
	}
	return bindings
}

func parseFederationForwardServiceNamesForRelease(data interface{}) []string {
	names := make(map[string]struct{})
	appendName := func(raw string) {
		name := normalizeForwardRuntimeServiceName(raw)
		if name == "" {
			return
		}
		if _, _, _, ok := parseFlowServiceIDs(name); !ok {
			return
		}
		names[name] = struct{}{}
	}

	for _, svcMap := range extractFederationServiceEntries(data) {
		appendName(asString(svcMap["name"]))
	}

	if dataMap, ok := data.(map[string]interface{}); ok {
		for _, item := range asAnySlice(dataMap["services"]) {
			appendName(asString(item))
		}
	}

	for _, item := range asAnySlice(data) {
		appendName(asString(item))
	}

	if len(names) == 0 {
		return nil
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (h *Handler) bindPeerShareForwardRuntimeServices(share *repo.PeerShare, data interface{}) error {
	if h == nil || h.repo == nil || share == nil {
		return nil
	}
	bindings := parseFederationForwardServiceBindings(data)
	if len(bindings) == 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	for _, binding := range bindings {
		runtime, err := h.repo.GetActiveForwardPeerShareRuntimeByPort(share.ID, binding.Port)
		if err != nil {
			return err
		}
		if runtime == nil {
			runtime, err = h.repo.GetActiveForwardPeerShareRuntimeByServiceName(share.ID, binding.Name)
			if err != nil {
				return err
			}
		}
		if runtime == nil {
			reservationID, randErr := randomToken(24)
			if randErr != nil {
				return randErr
			}
			resourceSuffix, randErr := randomToken(8)
			if randErr != nil {
				return randErr
			}
			if err := h.repo.CreatePeerShareRuntime(&repo.PeerShareRuntime{
				ShareID:       share.ID,
				NodeID:        share.NodeID,
				ReservationID: reservationID,
				ResourceKey:   fmt.Sprintf("forward-runtime:%d:%s:%d:%s", share.ID, binding.Name, binding.Port, resourceSuffix),
				BindingID:     "",
				Role:          "forward",
				ChainName:     "",
				ServiceName:   binding.Name,
				Protocol:      "tcp",
				Strategy:      "fifo",
				Port:          binding.Port,
				Target:        "",
				Applied:       1,
				Status:        1,
				CreatedTime:   now,
				UpdatedTime:   now,
			}); err != nil {
				return err
			}
			continue
		}
		if runtime.ReleasePending != 0 {
			return fmt.Errorf("runtime release is pending")
		}
		if runtime.ServiceName == binding.Name && runtime.Applied == 1 && runtime.Port == binding.Port && runtime.Status == 1 {
			continue
		}
		runtime.ServiceName = binding.Name
		runtime.Port = binding.Port
		runtime.Applied = 1
		runtime.Status = 1
		runtime.UpdatedTime = now
		if strings.TrimSpace(runtime.Protocol) == "" {
			runtime.Protocol = "tcp"
		}
		if strings.TrimSpace(runtime.Strategy) == "" {
			runtime.Strategy = "fifo"
		}
		if err := h.repo.UpdatePeerShareRuntime(runtime); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) releasePeerShareForwardRuntimeServices(share *repo.PeerShare, data interface{}) {
	if h == nil || h.repo == nil || share == nil {
		return
	}
	names := parseFederationForwardServiceNamesForRelease(data)
	if len(names) == 0 {
		return
	}

	now := time.Now().UnixMilli()
	for _, name := range names {
		_ = h.repo.MarkForwardPeerShareRuntimeReleasedByServiceName(share.ID, name, now)
	}
}

func isFederationRuntimeCommandAllowed(commandType string) bool {
	switch strings.ToLower(strings.TrimSpace(commandType)) {
	case "addservice", "updateservice", "deleteservice", "pauseservice", "resumeservice", "addchains", "updatechains", "deletechains", "addlimiters", "updatelimiters", "deletelimiters", "addclimiters", "updateclimiters", "deleteclimiters", "tcpping":
		return true
	default:
		return false
	}
}

func isFederationServiceCommand(commandType string) bool {
	switch strings.ToLower(strings.TrimSpace(commandType)) {
	case "addservice", "updateservice":
		return true
	default:
		return false
	}
}

func validateFederationCommandPorts(share *repo.PeerShare, data interface{}) error {
	if share == nil {
		return fmt.Errorf("share not found")
	}

	serviceList := extractFederationServiceEntries(data)
	if len(serviceList) == 0 {
		return nil
	}

	// D3-06: 端口范围未设/无效时与 tunnel/create、reserve-port 统一
	// fail-closed。此前 0/0 直接 return nil，peer 可经 command 路径在
	// 节点任意端口下发服务（R5 真机坐实 :39999 被接受并落库）。
	if share.PortRangeStart <= 0 || share.PortRangeEnd <= 0 || share.PortRangeEnd < share.PortRangeStart {
		return fmt.Errorf("share port range is not configured; service ports are not authorized")
	}
	for _, svcMap := range serviceList {
		addr := asString(svcMap["addr"])
		if addr == "" {
			continue
		}
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return fmt.Errorf("invalid service address: %s", addr)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port <= 0 {
			return fmt.Errorf("invalid port in service address: %s", addr)
		}
		if port < share.PortRangeStart || port > share.PortRangeEnd {
			return fmt.Errorf("port %d out of allowed range %d-%d", port, share.PortRangeStart, share.PortRangeEnd)
		}
	}

	return nil
}

// peerShareReservationTTL 是纯预留（reserve-port 成功、尚未 apply 角色）
// 的存活上限（B13-003）：对端在 reserve 后崩溃/弃单时预留到期自动释放，
// 端口回到可用池，不再永久占用直至耗尽整个端口段。消费方 reserve→apply
// 是同一操作内的连续调用，30 分钟远超正常流程耗时。回收走惰性路径：
// reserve 分配前批量回收本 share 的过期预留、apply 时校验目标预留未
// 过期；已进入 apply 的 runtime（role 非空）是期望状态、对账对象，
// 不在本 TTL 范围内。
const peerShareReservationTTL = 30 * time.Minute

// peerShareReservationExpired 判断一条 runtime 是否为已过期的纯预留。
func peerShareReservationExpired(runtime *repo.PeerShareRuntime, now time.Time) bool {
	return runtime != nil && runtime.Role == "" && runtime.Applied == 0 &&
		runtime.UpdatedTime > 0 && runtime.UpdatedTime < now.Add(-peerShareReservationTTL).UnixMilli()
}

func (h *Handler) pickPeerSharePort(share *repo.PeerShare, requestedPort int) (int, error) {
	if share == nil {
		return 0, fmt.Errorf("share not found")
	}
	if share.PortRangeStart <= 0 || share.PortRangeEnd <= 0 || share.PortRangeEnd < share.PortRangeStart {
		return 0, fmt.Errorf("No available port")
	}

	used := make(map[int]struct{})

	nodePorts, err := h.repo.ListUsedPortsOnNode(share.NodeID)
	if err != nil {
		return 0, err
	}
	for _, p := range nodePorts {
		used[p] = struct{}{}
	}

	ports, err := h.repo.ListActivePeerShareRuntimePorts(share.ID, share.NodeID)
	if err != nil {
		return 0, err
	}
	for _, p := range ports {
		if p > 0 {
			used[p] = struct{}{}
		}
	}

	if requestedPort > 0 {
		if requestedPort < share.PortRangeStart || requestedPort > share.PortRangeEnd {
			return 0, fmt.Errorf("Port out of range")
		}
		if _, ok := used[requestedPort]; ok {
			return 0, fmt.Errorf("No available port")
		}
		return requestedPort, nil
	}

	for p := share.PortRangeStart; p <= share.PortRangeEnd; p++ {
		if _, ok := used[p]; ok {
			continue
		}
		return p, nil
	}

	return 0, fmt.Errorf("No available port")
}

func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	parts := strings.Split(authHeader, " ")
	if len(parts) == 2 && parts[0] == "Bearer" {
		return parts[1]
	}
	return ""
}

func isPeerShareFlowExceeded(share *repo.PeerShare) bool {
	if share == nil {
		return false
	}
	if share.MaxBandwidth <= 0 {
		return false
	}
	return share.CurrentFlow >= share.MaxBandwidth
}

// validatePeerShareAllowedDomains 校验分享域名白名单（复扫 R-08）：
// 逗号分隔的域名列表逐项做空标签判；空值放行（不限制）。消费面为等值
// 匹配，坏条目永不命中——写面即拒，避免管理员误以为放行了某域名。
func validatePeerShareAllowedDomains(raw string) error {
	for _, d := range strings.Split(raw, ",") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if kernel.HasEmptyLabel(d) {
			return fmt.Errorf("分享域名 %q 包含空标签", d)
		}
	}
	return nil
}

func normalizePeerShareAllowedIPs(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))

	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}

		if strings.Contains(item, "/") {
			_, network, err := net.ParseCIDR(item)
			if err != nil {
				return "", fmt.Errorf("Invalid allowed IP or CIDR: %s", item)
			}
			item = network.String()
		} else {
			ip := parseIPLiteral(item)
			if ip == nil {
				return "", fmt.Errorf("Invalid allowed IP or CIDR: %s", item)
			}
			item = ip.String()
		}

		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		normalized = append(normalized, item)
	}

	return strings.Join(normalized, ","), nil
}

// federationClientIP 联邦侧客户端 IP 解析（D5）。
//
// 与浏览器侧（loginClientIP）是两种信任模型，分开实现、不共用函数：
// 面板↔面板是机器对机器通信，默认只信任 r.RemoteAddr（直连对端 IP）。
// 只有当显式配置 FLMX_FEDERATION_TRUSTED_PROXIES（逗号分隔 CIDR）且
// RemoteAddr 落在其内时，才从右按 FLMX_TRUSTED_PROXY_HOPS 解析 XFF。
// 把私网 RemoteAddr 一律视为可信代理，会让任意私网对端伪造 XFF 绕过
// AllowedIPs 白名单——这正是旧 resolvePeerClientIP 的缺陷。
func federationClientIP(r *http.Request) net.IP {
	if r == nil {
		return nil
	}
	remoteIP := parseIPLiteral(r.RemoteAddr)
	if remoteIP == nil {
		return nil
	}
	cidrs := federationTrustedProxyCIDRs()
	for _, cidr := range cidrs {
		if !cidr.Contains(remoteIP) {
			continue
		}
		hops := TrustedProxyHops()
		if hops <= 0 {
			return remoteIP
		}
		entries := splitXFFEntries(r.Header.Get("X-Forwarded-For"))
		// R24-X-01：同 loginClientIP，信任边界不可由左侧预置移动。
		// 联邦链上的代理 IP 应落在已配置的可信 CIDR 内；hops 超过
		// 实际可信代理链长即超配，fail-closed 回 RemoteAddr。
		isFedProxy := func(ip net.IP) bool {
			for _, c := range cidrs {
				if c.Contains(ip) {
					return true
				}
			}
			return false
		}
		if chainLen := trustedXFFChainLen(entries, isFedProxy); hops > chainLen {
			log.Printf("warning: federation XFF has %d entries but trusted proxy chain is %d hops (FLMX_TRUSTED_PROXY_HOPS=%d); ignoring XFF (fail-closed), using RemoteAddr", len(entries), chainLen, hops)
			return remoteIP
		}
		if len(entries) >= hops {
			if ip := parseIPLiteral(entries[len(entries)-hops]); ip != nil {
				return ip
			}
		}
		// XFF 缺失或选中条目非法：回退到 RemoteAddr，不信任整条链
		return remoteIP
	}
	return remoteIP
}

// federationTrustedProxyCIDRs 解析 FLMX_FEDERATION_TRUSTED_PROXIES。
// 未配置或解析失败时返回空（= 默认只用 RemoteAddr）。
func federationTrustedProxyCIDRs() []*net.IPNet {
	raw := strings.TrimSpace(config.Getenv("FLMX_FEDERATION_TRUSTED_PROXIES"))
	if raw == "" {
		return nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			// 裸 IP 视为 /32（或 /128）
			if ip := net.ParseIP(part); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
				continue
			}
			log.Printf("warning: invalid FLMX_FEDERATION_TRUSTED_PROXIES entry %q, skipped", part)
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			log.Printf("warning: invalid FLMX_FEDERATION_TRUSTED_PROXIES entry %q, skipped", part)
			continue
		}
		out = append(out, cidr)
	}
	return out
}

func parseIPLiteral(raw string) net.IP {
	value := strings.Trim(strings.TrimSpace(raw), "\"")
	if value == "" {
		return nil
	}

	if ip := net.ParseIP(value); ip != nil {
		return normalizeIPAddress(ip)
	}

	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return nil
	}

	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil
	}
	return normalizeIPAddress(net.ParseIP(host))
}

func normalizeIPAddress(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip.To16()
}

func isTrustedProxyIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func isPeerIPAllowed(clientIP net.IP, whitelist string) bool {
	if clientIP == nil {
		return false
	}

	for _, part := range strings.Split(whitelist, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}

		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				continue
			}
			if network.Contains(clientIP) {
				return true
			}
			continue
		}

		allowedIP := parseIPLiteral(entry)
		if allowedIP != nil && allowedIP.Equal(clientIP) {
			return true
		}
	}

	return false
}

// federationSyncErrorCode 把提供方 Connect 错误（client 形态
// "remote error <status>: <body>"）映射为消费方节点状态码
// （R1-FED-01）：按 authPeer 的实际回执文案逐类区分——IP 拒绝、
// 域名拒绝、已过期、已超额、已停用各自成码，使前端专设文案可
// 达。此前全部 403 一律映射 provider_share_disabled，且映射表
// 里的 "Share is disabled"/"Share expired" 字面量在提供方根本
// 不存在、分支永不可达。无法分类时返回 ""（调用方回退原文）。
// 旧版提供方的合并文案（"Share is inactive, expired, or over
// quota"）三因不可分，命中 "Share is inactive" 前缀按停用计，
// 是该形态下的既定降级。
func federationSyncErrorCode(errMsg string) string {
	switch {
	case strings.Contains(errMsg, "401") || strings.Contains(errMsg, "Invalid token") || strings.Contains(errMsg, "Unauthorized"):
		return "provider_share_deleted"
	case strings.Contains(errMsg, "IP not allowed") || strings.Contains(errMsg, "Unable to determine client IP"):
		return "provider_share_ip_denied"
	case strings.Contains(errMsg, "Domain not allowed") || strings.Contains(errMsg, "Domain verification required"):
		return "provider_share_domain_denied"
	case strings.Contains(errMsg, "Share has expired"):
		return "provider_share_expired"
	case strings.Contains(errMsg, "Share traffic limit exceeded"):
		return "provider_share_over_quota"
	case strings.Contains(errMsg, "403") || strings.Contains(errMsg, "Share is inactive"):
		return "provider_share_disabled"
	}
	return ""
}

func (h *Handler) syncRemoteNodeStatuses(items []map[string]interface{}) {
	type remoteEntry struct {
		index        int
		nodeID       int64
		remoteURL    string
		remoteToken  string
		remoteConfig string
	}

	// R7-F2-01：列表 DTO 已不再回传 remoteToken（凭据最小暴露面），
	// 同步所需的 URL/令牌改经 repo.ListRemoteNodes 在服务端内部按
	// 节点 ID 取用，不再借道下发给前端的列表项。
	indexByID := make(map[int64]int, len(items))
	hasRemote := false
	for i, item := range items {
		if isRemote, _ := item["isRemote"].(int); isRemote == 1 {
			hasRemote = true
		}
		if id, _ := item["id"].(int64); id > 0 {
			indexByID[id] = i
		}
	}
	if !hasRemote {
		return
	}
	rows, err := h.repo.ListRemoteNodes()
	if err != nil {
		log.Printf("sync remote node statuses: list remote nodes: %v", err)
		return
	}
	var remotes []remoteEntry
	for _, row := range rows {
		i, ok := indexByID[row.ID]
		if !ok {
			continue
		}
		url := strings.TrimSpace(row.RemoteURL.String)
		token := strings.TrimSpace(row.RemoteToken.String)
		if url == "" || token == "" {
			continue
		}
		remotes = append(remotes, remoteEntry{index: i, nodeID: row.ID, remoteURL: url, remoteToken: token, remoteConfig: row.RemoteConfig.String})
	}
	if len(remotes) == 0 {
		return
	}

	localDomain := h.federationLocalDomain()
	fc := client.NewFederationClientWithTimeout(5 * time.Second)

	type syncResult struct {
		index     int
		status    int
		syncError string
		// FIX2-B（V5-3）：对端版本与提示随节点列表回执呈现。
		panelVersion   string
		versionWarning string
	}

	results := make([]syncResult, len(remotes))
	var wg sync.WaitGroup
	// O12-003：限制并发同步数，避免远端节点很多时同时发起大量连接耗尽资源。
	sem := make(chan struct{}, 10)
	for i, entry := range remotes {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, e remoteEntry) {
			defer wg.Done()
			defer func() { <-sem }()
			info, err := fc.Connect(e.remoteURL, e.remoteToken, localDomain)
			if err != nil {
				errMsg := err.Error()
				code := federationSyncErrorCode(errMsg)
				if code == "" {
					code = errMsg
				}
				results[idx] = syncResult{index: e.index, status: 0, syncError: code}
			} else {
				logFederationVersionWarning(info)
				// R2-F3-01：状态同步同样是凭据自愈面（relay.md「下次
				// 同步自动自愈」）——此前只 Connect 不落盘凭据，提供方
				// 重新补发凭据后消费方持旧凭据永不自愈。按与建隧道
				// 路径同一合并口径比对刷新 RemoteConfig；Connect 失败
				// 的节点经下方 syncError 在状态面点名披露。
				h.mergeRemoteConnectCredentials(e.nodeID, e.remoteConfig, info)
				results[idx] = syncResult{index: e.index, status: info.Status, syncError: "", panelVersion: info.PanelVersion, versionWarning: info.VersionWarning}
			}
		}(i, entry)
	}
	wg.Wait()

	for _, r := range results {
		items[r.index]["status"] = r.status
		if r.syncError != "" {
			items[r.index]["syncError"] = r.syncError
		}
		if r.panelVersion != "" {
			items[r.index]["panelVersion"] = r.panelVersion
		}
		if r.versionWarning != "" {
			items[r.index]["versionWarning"] = r.versionWarning
		}
	}
}

func (h *Handler) cleanupPeerShareRuntimes(shareID int64) error {
	if h == nil || h.repo == nil || shareID <= 0 {
		return nil
	}
	if err := h.releasePeerShareResources(shareID); err != nil {
		return err
	}
	runtimes, err := h.repo.ListActivePeerShareRuntimesByShareID(shareID)
	if err != nil {
		return err
	}
	var cleanupErr error
	for i := range runtimes {
		if err := h.releasePeerShareRuntime(&runtimes[i]); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

// cleanupFederationTunnels 删除一个共享名下的全部联邦隧道，返回未能
// 删除的隧道 ID 清单（R2-F3-02：旧实现返回 void——查列表失败静默
// return、逐条删失败以 _ = 丢弃、调用方无条件回成功，残留孤儿无日
// 志无回执无重试）。现：识别口径双路——合成名前缀清单之外，再按
// tunnel/create 运行时 ResourceKey 反查隧道 ID（与 R2-F3-03 回放收养
// 同族：提供方改名后的联邦隧道不在前缀清单内，旧实现静默漏删）；
// 查列表任一失败记日志并回错误（调用方中止删共享、保留重试路径）；
// 逐条删失败逐条记日志并汇入返回清单，由调用方在回执点名披露。
func (h *Handler) cleanupFederationTunnels(shareID int64) ([]int64, error) {
	if h == nil || h.repo == nil || shareID <= 0 {
		return nil, nil
	}
	idSet := make(map[int64]struct{})
	namePrefix := fmt.Sprintf("Share-%d-Port-", shareID)
	tunnelIDs, err := h.repo.ListTunnelIDsByNamePrefix(namePrefix)
	if err != nil {
		log.Printf("federation share delete: list tunnels by name prefix for share %d: %v", shareID, err)
		return nil, err
	}
	for _, id := range tunnelIDs {
		idSet[id] = struct{}{}
	}
	runtimes, err := h.repo.ListPeerShareRuntimesByShareID(shareID)
	if err != nil {
		log.Printf("federation share delete: list runtimes for share %d: %v", shareID, err)
		return nil, err
	}
	for i := range runtimes {
		if tunnelID, ok := parseFederationForwardResourceKey(runtimes[i].ResourceKey, shareID, runtimes[i].Port); ok {
			idSet[tunnelID] = struct{}{}
		}
	}
	ids := make([]int64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// R4X-024（E4-S1）：裸级联删隧道会连 NodeInboundRelay 一并删掉，
	// 此前删后无从反查受影响入站、更无人刷新——landing 的 allow 残留
	// 已删隧道入口且零 KernelApply。与 tunnelDelete 的 O3-001 同口径：
	// 删前收集受影响入站集，删后逐一刷新白名单。
	var affectedInboundIDs []int64
	for _, tid := range ids {
		affectedInboundIDs = append(affectedInboundIDs, h.inboundIDsForTunnel(tid)...)
	}

	var failed []int64
	for _, tid := range ids {
		if err := h.deleteTunnelByID(tid); err != nil {
			log.Printf("federation share delete: cleanup of tunnel %d for share %d failed: %v", tid, shareID, err)
			failed = append(failed, tid)
		}
	}

	h.refreshInboundWhitelists(affectedInboundIDs)
	return failed, nil
}
