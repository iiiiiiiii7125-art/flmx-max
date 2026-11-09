package handler

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
//     行指向）、链式目标（被其他入站的 OutboundID 指向，其连接
//     对端是源入站所在机器）、计量伴生（CompanionOf>0）、导入
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

// connlogEntryForwardIDs 返回被标为连接日志入口的 forward ID 集
// （存在 Managed=1 的 NodeInboundRelay 行指向它）。
func (h *Handler) connlogEntryForwardIDs() (map[int64]bool, error) {
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
// 无记录混入」）。分流（SplitRules）目标在运行时才定、面板推
// 导不出，按设计 §2 不在此列。
func (h *Handler) connlogChainTargetInboundIDs() (map[int64]bool, error) {
	rows, err := h.repo.ListNodeInbounds()
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool)
	for _, row := range rows {
		if row.OutboundID > 0 {
			ids[row.OutboundID] = true
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
