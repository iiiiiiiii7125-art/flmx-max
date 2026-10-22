package handler

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	"go-backend/internal/kernel"
	runtimenft "go-backend/internal/runtime/nftables"
	"go-backend/internal/store/model"
	"gorm.io/gorm"
)

// Tunnels whose engine is realm run their forwards with realm on the entry
// nodes instead of the agent's forwarder. realm is installed and supervised
// like a kernel (one per node, config = every realm forward on the node), so
// a forward change re-renders its nodes' realm config.

const tunnelEngineRealm = kernel.TypeRealm

func tunnelIsRealm(t *tunnelRecord) bool { return t != nil && t.Engine == tunnelEngineRealm }

// normalizeTunnelEngine checks a requested engine: "" or "realm".
func normalizeTunnelEngine(raw string) (string, error) {
	switch e := strings.ToLower(strings.TrimSpace(raw)); e {
	case "", "gost":
		return "", nil
	case tunnelEngineRealm:
		return e, nil
	default:
		return "", fmt.Errorf("不支持的转发引擎 %s", raw)
	}
}

// validateRealmTunnelTx: realm runs port forwards on the panel's own agents.
func (h *Handler) validateRealmTunnelTx(tx *gorm.DB, typeVal int, entryNodeIDs []int64) error {
	if typeVal != 1 {
		return errors.New("realm 引擎仅支持端口转发隧道")
	}
	for _, nodeID := range entryNodeIDs {
		node, err := h.repo.GetNodeRecordTx(tx, nodeID)
		if err != nil {
			return err
		}
		if node == nil {
			return errors.New("节点不存在")
		}
		if node.IsRemote == 1 {
			return fmt.Errorf("节点 %s 是远程节点，不能使用 realm 引擎", node.Name)
		}
		if isNftablesForwardMode(node.ForwardMode) {
			return fmt.Errorf("节点 %s 是 nftables 模式，不能使用 realm 引擎", node.Name)
		}
	}
	return nil
}

// realmIPHash maps the forward strategy: realm only balances by round robin
// or IP hash.
func realmIPHash(strategy string) bool { return strings.TrimSpace(strategy) == "hash" }

// validateRealmForwardStrategy（A4-004 / BC-26）：realm 渲染多目标时非
// iphash 一律 roundrobin——fifo（主备）/rand（随机）曾被静默改写为轮询，
// 备目标在主目标健康时持续收到生产流量且用户无感知。改为明确拒绝：
// 仅当隧道为 realm、目标 ≥2 且策略 ∉ {round, hash} 时报错（单目标无
// 负载语义、策略不参与渲染，不拦，避免误伤存量默认 fifo 的单目标转发）。
func validateRealmForwardStrategy(tunnel *tunnelRecord, remoteAddr, strategy string) error {
	if !tunnelIsRealm(tunnel) {
		return nil
	}
	switch strings.TrimSpace(strategy) {
	case "", "round", "hash":
		return nil
	}
	targets := 0
	for _, part := range strings.Split(remoteAddr, ",") {
		if strings.TrimSpace(part) != "" {
			targets++
		}
	}
	if targets < 2 {
		return nil
	}
	return fmt.Errorf("realm 引擎仅支持轮询（round）与 IP 哈希（hash）负载策略，多目标转发不能使用 %s 策略", strings.TrimSpace(strategy))
}

func realmListenAddr(node *nodeRecord, port int, bindIP string) string {
	if bindIP = strings.TrimSpace(bindIP); bindIP != "" {
		if _, _, err := net.SplitHostPort(bindIP); err == nil {
			return processServerAddress(bindIP)
		}
		return processServerAddress(net.JoinHostPort(strings.Trim(bindIP, "[]"), strconv.Itoa(port)))
	}
	return processServerAddress(fmt.Sprintf("%s:%d", node.TCPListenAddr, port))
}

// realmNodeEndpoints lists the realm forwards to run on a node. override
// forces a forward on or off: pause, resume and delete re-render before the
// forward's status changes in the database.
func (h *Handler) realmNodeEndpoints(nodeID int64, override map[int64]bool) ([]kernel.RealmEndpoint, []map[string]interface{}, error) {
	node, err := h.getNodeRecord(nodeID)
	if err != nil {
		return nil, nil, err
	}
	forwards, err := h.repo.ListRealmForwardsByNode(nodeID)
	if err != nil {
		return nil, nil, err
	}
	var endpoints []kernel.RealmEndpoint
	var ports []map[string]interface{}
	tunnels := map[int64]*tunnelRecord{}
	for i := range forwards {
		f := &forwards[i]
		tunnel, ok := tunnels[f.TunnelID]
		if !ok {
			tunnel, _ = h.repo.GetTunnelRecord(f.TunnelID)
			tunnels[f.TunnelID] = tunnel
		}
		run := f.Status == 1 && tunnel != nil && tunnel.Status == 1
		if v, ok := override[f.ID]; ok {
			run = v
		}
		if !run {
			continue
		}
		fps, err := h.listForwardPorts(f.ID)
		if err != nil {
			return nil, nil, err
		}
		receive, send := normalizeForwardProxyProtocol(f.ProxyProtocol, f.ProxyProtocolReceive, f.ProxyProtocolSend)
		for _, fp := range fps {
			if fp.NodeID != nodeID || fp.Port <= 0 {
				continue
			}
			e := kernel.RealmEndpoint{
				ForwardID:   f.ID,
				Listen:      realmListenAddr(node, fp.Port, fp.InIP),
				Remotes:     splitRemoteTargets(f.RemoteAddr),
				IPHash:      realmIPHash(f.Strategy),
				Interface:   strings.TrimSpace(node.InterfaceName),
				AcceptProxy: receive > 0,
				SendProxy:   send,
			}
			endpoints = append(endpoints, e)
			ports = append(ports, map[string]interface{}{
				"inboundId": kernel.RealmCounterID(f.ID), "port": fp.Port, "network": "tcp,udp",
			})
		}
	}
	return endpoints, ports, nil
}

// buildRealmApplyPayload renders a node's realm kernel.
func (h *Handler) buildRealmApplyPayload(k *model.NodeKernel, info kernel.Info, override map[int64]bool) (map[string]interface{}, string, error) {
	endpoints, ports, err := h.realmNodeEndpoints(k.NodeID, override)
	if err != nil {
		return nil, "", err
	}
	rendered, err := kernel.RenderRealm(endpoints)
	if err != nil {
		return nil, "", err
	}
	proxy := ""
	if enabled, proxyURL := h.getGithubProxyConfig(); enabled {
		proxy = proxyURL
	}
	download, err := kernel.ResolveDownload(info, k.Version, proxy)
	if err != nil {
		return nil, "", err
	}
	if ports == nil {
		ports = []map[string]interface{}{}
	}
	return map[string]interface{}{
		"type":     k.KernelType,
		"version":  k.Version,
		"download": download,
		"files":    rendered.Files,
		"args":     rendered.Args,
		"hash":     rendered.Hash,
		"enabled":  k.Enabled == 1 && !rendered.Idle,
		"ports":    ports,
	}, rendered.Hash, nil
}

// ensureRealmKernel returns the node's realm kernel, registering it when the
// node needs one (create false only looks it up).
func (h *Handler) ensureRealmKernel(nodeID int64, create bool) (*model.NodeKernel, error) {
	k, err := h.repo.GetNodeKernelByType(nodeID, kernel.TypeRealm)
	if err != nil || (k == nil && !create) {
		return k, err
	}
	now := time.Now().UnixMilli()
	if k == nil {
		k = &model.NodeKernel{
			NodeID: nodeID, KernelType: kernel.TypeRealm, Version: kernel.RealmVersion,
			Status: kernelStatusPending, Enabled: 1, CreatedTime: now, UpdatedTime: now,
		}
		return k, h.repo.CreateNodeKernel(k)
	}
	if k.Version != kernel.RealmVersion {
		// A panel upgrade moved to a newer realm.
		if err := h.repo.UpdateNodeKernelFields(k.ID, map[string]interface{}{"version": kernel.RealmVersion, "updated_time": now}); err != nil {
			return nil, err
		}
		k.Version = kernel.RealmVersion
	}
	return k, nil
}

// syncRealmNodes re-renders realm on the nodes. Offline nodes are skipped
// with a warning; they catch up when the agent reconnects.
func (h *Handler) syncRealmNodes(nodeIDs []int64, override map[int64]bool) ([]string, error) {
	var warnings []string
	for _, nodeID := range uniqueInt64s(nodeIDs) {
		if nodeID <= 0 {
			continue
		}
		endpoints, _, err := h.realmNodeEndpoints(nodeID, override)
		if err != nil {
			return warnings, err
		}
		k, err := h.ensureRealmKernel(nodeID, len(endpoints) > 0)
		if err != nil {
			return warnings, err
		}
		if k == nil {
			continue
		}
		install := k.InstalledVersion == ""
		if err := h.applyKernelWith(k.ID, install, override); err != nil {
			if isNodeOfflineOrTimeoutError(err) {
				warnings = append(warnings, fmt.Sprintf("节点 %s 不在线，realm 配置待节点上线后下发", h.nodeLabel(nodeID)))
				continue
			}
			if strings.Contains(err.Error(), "不支持的内核类型") {
				return warnings, fmt.Errorf("节点 %s 的 agent 版本过旧，不支持 realm，请先升级节点", h.nodeLabel(nodeID))
			}
			return warnings, fmt.Errorf("节点 %s realm 下发失败: %w", h.nodeLabel(nodeID), err)
		}
	}
	return warnings, nil
}

// syncRealmForward re-renders the realm nodes of one forward.
func (h *Handler) syncRealmForward(forward *forwardRecord, override map[int64]bool) ([]string, error) {
	ports, err := h.listForwardPorts(forward.ID)
	if err != nil {
		return nil, err
	}
	warnings, err := h.syncRealmNodes(forwardPortNodeIDs(ports), override)
	if err != nil {
		return warnings, err
	}
	if forward.SpeedID.Valid || forward.IPSpeedID.Valid || forward.MaxConn > 0 || forward.IPMaxConn > 0 {
		warnings = append(warnings, "realm 引擎不支持限速和连接数限制，这些设置不生效")
	}
	return warnings, nil
}

// controlRealmForward pauses, resumes or removes a realm forward.
func (h *Handler) controlRealmForward(forward *forwardRecord, commandType string) error {
	run := strings.EqualFold(strings.TrimSpace(commandType), "ResumeService")
	_, err := h.syncRealmForward(forward, map[int64]bool{forward.ID: run})
	return err
}

// switchTunnelEngine moves a tunnel's forwards between realm and the agent's
// forwarder on its old and new entry nodes.
//
// A4-003：原实现全程吞错恒报成功——realm 下发硬失败（agent 版本过旧等）
// 或旧引擎服务删除硬失败时 DB 已切引擎、数据面未切，用户无感知且长期
// 分叉。现在聚合硬失败返回给调用方呈现；节点离线/超时属既有自愈语义
// （重连后对账收敛），仍只记日志不算失败，与 syncRealmNodes 的
// warnings 口径一致。
func (h *Handler) switchTunnelEngine(tunnelID int64, fromRealm bool, oldEntryNodeIDs, newEntryNodeIDs []int64) error {
	forwards, err := h.listForwardsByTunnel(tunnelID)
	if err != nil {
		return err
	}
	nodes := append(append([]int64{}, oldEntryNodeIDs...), newEntryNodeIDs...)
	var hardErrs []error
	collect := func(op string, err error) {
		if err == nil {
			return
		}
		if isNodeOfflineOrTimeoutError(err) {
			log.Printf("realm engine switch tunnel_id=%d op=%s deferred (node offline, converges on reconnect): %v", tunnelID, op, err)
			return
		}
		hardErrs = append(hardErrs, fmt.Errorf("%s: %w", op, err))
	}
	if fromRealm {
		// R3-01：拆除 realm 前先结算最终计数——拆除后 agent nft 表
		// 重建、realm 端口计数消失，此刻不结算的增量永久漏计
		// （A4-010 的 realm 侧窗口）。轮询失败（离线）不阻塞切换，
		// 与离线自愈口径一致。
		h.settleRealmCountersAtSwitch(nodes)
		// FIX2-A（V2-04）：settle 对离线节点是静默跳过——DB 引擎已
		// 切 gost、切换照常报成功，但自上次成功轮询以来的 realm 增
		// 量还只存在于 agent 侧。给离线节点打待结算标记，节点重连
		// 时 onNodeOnline 先结算再放行对账/重下发（见 upgrade.go），
		// 否则重连后 redeploy 先让 agent 弃 realm 计数、增量永久
		// 漏计且无任何日志。
		for _, nodeID := range uniqueInt64s(nodes) {
			if nodeID > 0 && (h.wsServer == nil || !h.wsServer.IsNodeOnline(nodeID)) {
				h.markPendingRealmSettle(nodeID)
				log.Printf("realm engine switch tunnel_id=%d node_id=%d offline: settle deferred to reconnect (pending marker set)", tunnelID, nodeID)
			}
		}
		// Free the ports first, then start the agent's services.
		if _, err := h.syncRealmNodes(nodes, nil); err != nil {
			collect("realm 收敛", err)
		}
		for i := range forwards {
			collect("gost 服务同步", h.syncForwardServices(&forwards[i], "UpdateService", true))
		}
		return errors.Join(hardErrs...)
	}
	for i := range forwards {
		for _, nodeID := range uniqueInt64s(nodes) {
			collect("旧引擎服务删除", h.deleteForwardServicesOnNode(&forwards[i], nodeID))
		}
	}
	if _, err := h.syncRealmNodes(nodes, nil); err != nil {
		collect("realm 下发", err)
	}
	// R3-01：realm 起来后立即拉一次状态：新引擎计数基线钉在切换点，
	// 切换后立即产生的流量从下一轮询起即可差分入账；不再等 60s 轮询
	// 首采样才建基线（真机 T10 观察到切换后计数 ≥8 分钟不动、仅迟到
	// 部分入账）。
	h.settleRealmCountersAtSwitch(nodes)
	return errors.Join(hardErrs...)
}

// markPendingRealmSettle / consumePendingRealmSettle /
// hasPendingRealmSettle 是 V2-04 的待结算标记（upgradeMu 保护，与
// pendingUpgradeRedeploy 同族）：realm→gost 切换时节点离线、settle
// 轮询静默跳过，则给节点打标记；节点重连（onNodeOnline）在放行
// 异步对账与 redeploy 之前先消费标记做一次同步结算，保证「先结算
// 后下发」的顺序，增量不随 agent 弃 realm 计数丢失。
func (h *Handler) markPendingRealmSettle(nodeID int64) {
	if h == nil || nodeID <= 0 {
		return
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if h.pendingRealmSettle == nil {
		h.pendingRealmSettle = make(map[int64]struct{})
	}
	h.pendingRealmSettle[nodeID] = struct{}{}
}

func (h *Handler) consumePendingRealmSettle(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	if _, ok := h.pendingRealmSettle[nodeID]; !ok {
		return false
	}
	delete(h.pendingRealmSettle, nodeID)
	return true
}

func (h *Handler) hasPendingRealmSettle(nodeID int64) bool {
	if h == nil || nodeID <= 0 {
		return false
	}
	h.upgradeMu.Lock()
	defer h.upgradeMu.Unlock()
	_, ok := h.pendingRealmSettle[nodeID]
	return ok
}

// settleRealmCountersAtSwitch 主动拉一次各节点的内核状态，把 realm
// 计数结算/建基线到当前时刻。refreshNodeKernelStatus 内部对离线节点
// 静默跳过，失败不影响切换本身。
func (h *Handler) settleRealmCountersAtSwitch(nodeIDs []int64) {
	for _, nodeID := range uniqueInt64s(nodeIDs) {
		if nodeID > 0 {
			h.refreshNodeKernelStatus(nodeID)
		}
	}
}

// applyRealmCounters credits realm forwards with the traffic the agent
// counted on their ports, the same way nftables forwards are accounted.
func (h *Handler) applyRealmCounters(nodeID int64, epoch string, counters []agentKernelCounter, now time.Time) {
	samples := make([]runtimenft.CounterSample, 0, len(counters))
	hashes := map[int64]string{}
	for _, c := range counters {
		fid, ok := kernel.RealmForwardID(c.InboundID)
		if !ok || c.Bytes < 0 {
			continue
		}
		dir := runtimenft.CounterDirectionToTarget
		if c.Dir == "out" {
			dir = runtimenft.CounterDirectionFromTarget
		} else if c.Dir != "in" {
			continue
		}
		samples = append(samples, runtimenft.CounterSample{ForwardID: fid, Direction: dir, Protocol: c.Proto, Bytes: uint64(c.Bytes)})
		// The agent's counters restart with its epoch.
		hashes[fid] = "realm:" + epoch
	}
	if len(samples) == 0 {
		return
	}
	batch, metas, nowMs, quotaViews, err := h.accountForwardCounterSamples(nodeID, samples, hashes, now)
	if err != nil || batch == nil {
		return
	}
	// 与采集路径（collectNftablesNodeTraffic）相同的执法尾：指标、配额
	// 强制与流量策略在入账落库后同批执行。
	h.recordTunnelMetricsFromForwardBatch(nodeID, batch.forwardTraffic, metas, nowMs)
	for userID, quota := range quotaViews {
		h.enforceUserQuotaIfNeeded(userID, quota)
	}
	for _, target := range batch.policyTargets {
		if target.UserID <= 0 || target.UserTunnelID <= 0 {
			continue
		}
		h.enforceFlowPolicies(target.UserID, target.UserTunnelID)
	}
}
