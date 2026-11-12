package handler

import (
	"go-backend/internal/store/repo"
)

// 入站连接日志（设计 feat-inbound-connlog §2/§6-U7）的入口标记：
// 面板在既有配置下发通道里显式标明「入口」，agent 只对带标记的
// 面采集，禁止靠服务名/地址猜测归属。
//
// 两条下发面：
//   - gost 转发服务：托管中转（Managed=1 的 NodeInboundRelay 行）
//     名下 forward 的服务是整条线路的入口，其 `<base>_tcp/_udp`
//     服务在 service metadata 里带 connlogEntryKind=forward 与
//     connlogEntryID=<服务名>（即 §5.1 的 entry_id）。普通用户
//     转发没有 relay 行，天然零标记。
//   - 内核入站：KernelApply 载荷的 connlogEntries 列出「自身即
//     入口」的入站（直连，或链式源入站）：被中转落地（有 relay
//     行指向）、链式目标（被其他入站的 OutboundID 或分流规则的
//     SplitProxy 目标指向，其连接对端是源入站所在机器）、计量伴生（CompanionOf>0）、导入
//     来源（SourceID>0）以及 RelayOnly/RelayWhitelist 的入站都
//     不在列——它们名下的连接对端是上一跳而非客户端，采下来
//     是错数据。
//
// 两组集合都由当前 DB 拓扑在每次生成时现算，不落任何缓存：
// 中转增删、链式改道后随下一次配置重推自动收敛，不留陈旧标记。
const (
	connlogEntryKindMetadataKey = "connlogEntryKind"
	connlogEntryIDMetadataKey   = "connlogEntryID"
	connlogEntryKindForward     = "forward"
)

// connlogMasterEnabled 读功能总开关（connlog_enabled，设计
// §4.5，读面口径单点在 repo.ConnLogEnabledFromConfigMap）。一切
// 标记/名单的下发都先过这道门：关时三面皆空——gost 入口标记
// （connlogEntryForwardIDs 直接返回空集）、KernelApply 的
// connlogEntries（恒下发空集、清 agent 旧标记）与
// connLogInbounds（不写键）——agent 侧采集随下一次配置推送全
// 链停下，不许只停一半。读失败返回错误：两个下发面本就以
// 「查询失败整单中止」为口径（半知状态下发标记可能把上一跳
// 标成客户端），不静默按开处理。
func (h *Handler) connlogMasterEnabled() (bool, error) {
	cfg, err := h.repo.GetConfigsByNames([]string{repo.ConnLogEnabledConfigKey})
	if err != nil {
		return false, err
	}
	return repo.ConnLogEnabledFromConfigMap(cfg), nil
}

// connlogEnabledForNode 是单节点的采集门控：总开关 AND 该节点
// 的节点级开关（node.conn_log_enabled，默认关）。节点开关是
// 唯一的采集门控：关则该节点直连＋中转一律不采，开则全采；
// 开则该节点全部入站的内核日志形态随之调整（sing-box 升
// debug、Xray 开 access），直连入站连接被采集。任一查询失败
// 返回错误（fail-closed，与总开关同口径）。
func (h *Handler) connlogEnabledForNode(nodeID int64) (bool, error) {
	masterOn, err := h.connlogMasterEnabled()
	if err != nil {
		return false, err
	}
	if !masterOn {
		return false, nil
	}
	node, err := h.repo.GetNodeByID(nodeID)
	if err != nil {
		return false, err
	}
	if node == nil {
		return false, nil
	}
	return node.ConnLogEnabled == 1, nil
}

// connlogEntryForwardIDs 返回被标为连接日志入口的 forward ID 集
// （存在 Managed=1 的 NodeInboundRelay 行指向它）；总开关关时
// 返回空集（见 connlogMasterEnabled）。
func (h *Handler) connlogEntryForwardIDs() (map[int64]bool, error) {
	enabled, err := h.connlogMasterEnabled()
	if err != nil {
		return nil, err
	}
	if !enabled {
		return map[int64]bool{}, nil
	}
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool, len(rels))
	for _, rel := range rels {
		if rel.Managed == 1 {
			ids[rel.ForwardID] = true
		}
	}
	return ids, nil
}

// connlogRelayTargetInboundIDs 返回「有托管中转指向」的入站 ID
// 集：这些入站是落地，其入口在中转入口机的 gost 服务上，自身
// 不作直连入口标记。
func (h *Handler) connlogRelayTargetInboundIDs() (map[int64]bool, error) {
	rels, err := h.repo.ListNodeInboundRelays()
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool, len(rels))
	for _, rel := range rels {
		if rel.Managed == 1 {
			ids[rel.InboundID] = true
		}
	}
	return ids, nil
}

// connlogChainTargetInboundIDs 返回被任一入站以 OutboundID 指
// 向的入站 ID 集：链式目标是落地，客户端连入的是源入站，目标
// 名下的连接对端是源机器，不得标直连入口（验收格一⑥「链式腿
// 无记录混入」）。分流目标同形同理：分流腿的对端同样是源机
// 器——「分流目标在运行时才定」只对「某条连接命中哪条规则」
// 成立，静态目标集合面板完全推导得出，故与 OutboundID 一并
// 枚举（复用 kernel_chain.go 的 outboundTargets）。
func (h *Handler) connlogChainTargetInboundIDs() (map[int64]bool, error) {
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool)
	for _, row := range rows {
		for _, id := range outboundTargets(row) {
			ids[id] = true
		}
	}
	return ids, nil
}

// connlogEntryForService 是 buildForwardServiceConfigs 写标记的
// 谓词出口（测试与下发共用）：给定 forward 是否入口、以及生成
// 出的服务名，返回应写入该服务 metadata 的标记键值；非入口返回
// nil（零标记）。
func connlogEntryMetadata(entry bool, serviceName string) map[string]interface{} {
	if !entry {
		return nil
	}
	return map[string]interface{}{
		connlogEntryKindMetadataKey: connlogEntryKindForward,
		connlogEntryIDMetadataKey:   serviceName,
	}
}
