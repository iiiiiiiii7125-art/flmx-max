package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/repo"
)

// Serialize desired-state changes with reconnect reconciliation. In particular,
// a stale reconnect snapshot must never revive an acknowledged release.
var peerRoleRuntimeMu sync.Mutex

// applyPeerShareRoleRuntime requires peerRoleRuntimeMu. Persist the validated
// desired configuration before creating resources, so failed or interrupted
// commands remain recoverable rather than producing untracked listeners.
func (h *Handler) applyPeerShareRoleRuntime(runtime *repo.PeerShareRuntime) error {
	if runtime.ReleasePending != 0 || runtime.Status != 1 {
		return errors.New("runtime is being released")
	}
	if runtime.Role != "middle" && runtime.Role != "exit" {
		return errors.New("invalid runtime role")
	}
	// F-UDP-4: 重连对账重下发不经 reserve/apply-role HTTP 入口，
	// 在此对存量 protocol 做同一合法集校验，防修复前落库的非法值
	// （如 sshd）绕过入口校验直达生成。
	if err := validateTunnelProtocol(runtime.Protocol); err != nil {
		return err
	}
	// R1-03-L1：strategy 同口径补判（protocol 门的同族漏格）——
	// 存量非法 strategy（如 banana）在此点名失败，不再经联邦链
	// 渲染静默落 agent default 轮询。空值是缺省形态（消费面按
	// round 语义），不在此判。
	if strings.TrimSpace(runtime.Strategy) != "" && !kernel.PeerRuntimeStrategyLegal(runtime.Strategy) {
		return fmt.Errorf("unsupported peer runtime strategy %q", strings.TrimSpace(runtime.Strategy))
	}
	node, err := h.getNodeRecord(runtime.NodeID)
	if err != nil {
		return err
	}
	var targets []federationRuntimeTarget
	if strings.TrimSpace(runtime.Target) != "" {
		if err := json.Unmarshal([]byte(runtime.Target), &targets); err != nil {
			return err
		}
	}
	// D3-04: 角色服务必须带本 share 的 relay 凭据（handler auth）。
	// share 加载失败时不静默降级为无鉴权服务——直接失败，让 runtime
	// 保持未 applied 状态等重试，而不是留下一个开放中继。
	share, err := h.repo.GetPeerShare(runtime.ShareID)
	if err != nil {
		return err
	}
	if share == nil {
		return errors.New("share not found for role runtime")
	}
	h.ensurePeerShareRelayCredentials(share)
	if runtime.Role == "middle" {
		// 同 share 兜底：多跳链的下一跳若就是本 share 在同节点的另
		// 一个角色服务（端口命中本 share 的其他 active runtime），
		// 消费方未必知道那是同 share 的服务、不会附凭据——由提供方
		// 按端口归属补上本 share 凭据。
		ownPorts := map[int]bool{}
		if runtimes, listErr := h.repo.ListActivePeerShareRuntimesByShareID(share.ID); listErr == nil {
			for _, rt := range runtimes {
				if rt.Port > 0 && rt.ID != runtime.ID {
					ownPorts[rt.Port] = true
				}
			}
		}
		for i := range targets {
			if strings.TrimSpace(targets[i].RelayPassword) == "" && ownPorts[targets[i].Port] {
				targets[i].RelayUsername = share.RelayUsername
				targets[i].RelayPassword = share.RelayPassword
			}
		}
	}
	// K1-01：KCP 角色服务的监听钥按 share 派生（与消费方面板为
	// 同一中继链生成的拨号钥同源：同 jwtSecret＋同 share ID）；
	// 派生失败整条 runtime 拒绝下发，不产出无钥 KCP 服务。
	serviceKCPKey, err := federationKCPKey(h.jwtSecret, runtime.ShareID, runtime.Protocol)
	if err != nil {
		return err
	}
	var chain map[string]interface{}
	if runtime.Role == "middle" {
		// KCP 目标腿的拨号钥在 builder 内按同一 (secret, shareID)
		// 派生，派生失败由 builder 返回错误、整条 runtime 拒绝。
		chain, err = buildFederationMiddleChainConfig(runtime.ChainName, runtime.ID, runtime.Protocol, runtime.Strategy, targets, node.InterfaceName, h.jwtSecret, runtime.ShareID, kcpFecOff(h.kcpFecMode()))
		if err != nil {
			return err
		}
	}
	// R32-FIX-F01 / PC-01：第 4 处 TCPListenAddr 直拼——监听地址先剥离 :port
	// 再与服务端口拼接（枚举 A12–A14 之外的第四处同形直拼点；须用
	// JoinHostPort：裸 IPv6 直拼会被误读，见 realm.go 同注释）。
	// 注：不用裸 processServerAddress 包裹——它对 "host:port:port" 原样返回，
	// 包了也修不了本处。
	// F-UDP-6：监听地址按协议分取（nodeListenAddrForProtocol）——
	// udp/kcp 角色服务绑 UDPListenAddr，与转发面/本地链面同口径。
	service := buildFederationServiceConfig(runtime.ServiceName, net.JoinHostPort(strings.Trim(stripListenAddrPort(nodeListenAddrForProtocol(node, runtime.Protocol)), "[]"), strconv.Itoa(runtime.Port)), runtime.Protocol, runtime.Role, runtime.ChainName, len(targets), node.InterfaceName, serviceKCPKey)
	if auth := relayAuthConfig(share.RelayUsername, share.RelayPassword); auth != nil {
		service["handler"].(map[string]interface{})["auth"] = auth
	}
	// FIX2-C #8（D3-04 目标面补完）：exit 服务挂 agent 内置 bypass
	// 拒绝私网目标；节点 agent 版本不够时不下发（旧版对未知 bypass
	// 名 fail-open，下发只会制造已防护假象）。版本读取失败按不支持
	// 处理（不下发），与版本未知同口径。
	if runtime.Role == "exit" {
		agentVersion, versionErr := h.repo.GetNodeAgentVersion(runtime.NodeID)
		if versionErr != nil {
			agentVersion = ""
		}
		applyFederationExitPrivateGuard(service, runtime.Role, agentVersion)
	}
	runtime.Applied = 0
	runtime.UpdatedTime = time.Now().UnixMilli()
	if err := h.repo.UpdatePeerShareRuntime(runtime); err != nil {
		return err
	}
	if h.wsServer == nil {
		return errors.New("node command transport unavailable")
	}
	if chain != nil {
		if _, err := h.sendNodeCommand(runtime.NodeID, "UpdateChains", updateChainPayload(runtime.ChainName, chain), false, false); err != nil {
			return err
		}
	}
	// UpdateService and UpdateChains are upserts, including on an empty agent.
	if _, err := h.sendNodeCommand(runtime.NodeID, "UpdateService", []map[string]interface{}{service}, false, false); err != nil {
		return err
	}
	runtime.Applied = 1
	runtime.UpdatedTime = time.Now().UnixMilli()
	return h.repo.UpdatePeerShareRuntime(runtime)
}

func (h *Handler) releasePeerShareRuntime(runtime *repo.PeerShareRuntime) error {
	peerRoleRuntimeMu.Lock()
	defer peerRoleRuntimeMu.Unlock()
	if runtime == nil {
		return nil
	}
	current, err := h.repo.GetPeerShareRuntimeByID(runtime.ID)
	if err != nil {
		return err
	}
	if current != nil && (current.ReservationID != runtime.ReservationID || current.BindingID != runtime.BindingID) {
		// A completed reservation may have been reused while this release
		// waited for the mutation lock. Never release the new generation.
		return nil
	}
	return h.releasePeerShareRuntimeLocked(current)
}

func (h *Handler) releasePeerShareRuntimeLocked(runtime *repo.PeerShareRuntime) error {
	if runtime == nil || runtime.Status == 0 {
		return nil
	}
	if err := h.repo.SetPeerShareRuntimeReleasePending(runtime.ID); err != nil {
		return err
	}
	runtime.ReleasePending = 1
	if runtime.Role == "forward" {
		if _, _, scoped := parsePeerShareServiceName(runtime.ServiceName); scoped {
			if err := h.releasePeerShareForwardRuntimeResources(runtime); err != nil {
				return err
			}
			return h.repo.CompletePeerShareRuntimeRelease(runtime.ID)
		}
		// Older agents used unscoped names. Do not delete a name shared by
		// another reservation or by a local forward during migration.
		owners, err := h.repo.ListActiveForwardPeerShareRuntimesByNodeAndServiceName(runtime.NodeID, runtime.ServiceName)
		if err != nil {
			return err
		}
		for _, owner := range owners {
			if owner.ShareID != runtime.ShareID {
				return fmt.Errorf("legacy runtime %q has ambiguous ownership", runtime.ServiceName)
			}
		}
		if forwardID, _, _, ok := parseFlowServiceIDs(normalizeForwardRuntimeServiceName(runtime.ServiceName)); ok {
			local, err := h.repo.GetForwardRecord(forwardID)
			if err != nil {
				return err
			}
			if local != nil {
				return fmt.Errorf("legacy runtime %q collides with a local forward", runtime.ServiceName)
			}
		}
	}
	// ServiceName is saved before apply; Applied=0 can therefore mean an
	// unacknowledged command, and must not bypass deletion.
	if strings.TrimSpace(runtime.ServiceName) != "" || strings.TrimSpace(runtime.ChainName) != "" {
		if h.wsServer == nil {
			return errors.New("node command transport unavailable")
		}
		if strings.TrimSpace(runtime.ServiceName) != "" {
			names := []string{runtime.ServiceName}
			if runtime.Role == "forward" {
				names = append(names, runtime.ServiceName+"_tcp", runtime.ServiceName+"_udp")
			}
			if _, err := h.sendNodeCommand(runtime.NodeID, "DeleteService", map[string]interface{}{"services": names}, false, true); err != nil {
				return err
			}
		}
		if strings.TrimSpace(runtime.ChainName) != "" {
			if _, err := h.sendNodeCommand(runtime.NodeID, "DeleteChains", map[string]interface{}{"chain": runtime.ChainName}, false, true); err != nil {
				return err
			}
		}
	}
	return h.repo.CompletePeerShareRuntimeRelease(runtime.ID)
}

func (h *Handler) reconcilePeerShareRoleRuntimesOnNode(nodeID int64) error {
	return h.reconcilePeerShareRoleRuntimes(nodeID, false)
}

// Maintenance retries only unfinished desired state. Replaying acknowledged
// services while an unrelated operation is failing can interrupt live traffic.
func (h *Handler) retryPendingPeerShareRoleRuntimesOnNode(nodeID int64) error {
	return h.reconcilePeerShareRoleRuntimes(nodeID, true)
}

func (h *Handler) reconcilePeerShareRoleRuntimes(nodeID int64, pendingOnly bool) error {
	peerRoleRuntimeMu.Lock()
	defer peerRoleRuntimeMu.Unlock()
	runtimes, err := h.repo.ListActivePeerShareRuntimesByNode(nodeID)
	if err != nil {
		return err
	}
	var reconcileErr error
	for i := range runtimes {
		runtime := &runtimes[i]
		if pendingOnly && runtime.Applied == 1 && runtime.ReleasePending == 0 {
			continue
		}
		if runtime.ReleasePending != 0 {
			reconcileErr = errors.Join(reconcileErr, h.releasePeerShareRuntimeLocked(runtime))
			continue
		}
		if runtime.Role != "middle" && runtime.Role != "exit" {
			continue
		}
		share, err := h.repo.GetPeerShare(runtime.ShareID)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		if share == nil || share.IsActive != 1 || (share.ExpiryTime > 0 && share.ExpiryTime <= time.Now().UnixMilli()) || isPeerShareFlowExceeded(share) {
			reconcileErr = errors.Join(reconcileErr, h.releasePeerShareRuntimeLocked(runtime))
			continue
		}
		if err := h.applyPeerShareRoleRuntime(runtime); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("shared runtime %d: %w", runtime.ID, err))
		}
	}
	return reconcileErr
}
