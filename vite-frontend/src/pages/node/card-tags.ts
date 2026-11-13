import type {
  ForwardApiItem,
  SpeedLimitApiItem,
  TunnelApiItem,
} from "@/api/types";

/**
 * 节点卡片标签集（X06 恢复，1.2.0 标签集语义、现行后端口径）。
 *
 * 1.2.0 的「用户/分组/限速三标签＋流量进度条」在后续卡片重构
 * （Round 5/6 拆分）中被移除、无替代面（AUDIT-HIST L5）。
 * 现行后端 node/list 不下发逐节点用户数与限速（Node 模型无此二
 * 字段），故三标签按现行后端已服务的数据口径还原，不编造数字：
 *
 * - 用户：经隧道使用本节点的去重用户数。转发（forward/list）是
 *   用户流量进入隧道的唯一归属面（userId＋tunnelId＋speedId），
 *   隧道（tunnel/list）的 inNodeId/outNodeId/chainNodes 给出链
 *   上全部节点。只计「转发状态正常（status 1）且隧道已启用
 *   （status 1）」的活路：暂停/异常/删除中的转发与已禁用隧道
 *   不载流，计入会虚增。订阅直连入站的用户不在此数内（入站行
 *   无 userId，后端未服务该归属），芯片 title 点名此口径。
 * - 分组：节点自带的 displayGroup（node/list 下发，纯展示标
 *   签；空＝未分组），不走推导。
 * - 限速：上述活路转发上绑定的限速规则名去重。speedId 按
 *   限速规则表（speed-limit/list）解名、解不出时以「规则#id」
 *   原样示出（不静默丢弃）；ipSpeedId 同口径解名，
 *   ipSpeedLimitName 是后端已服务的名字、直接取用。无绑定
 *   时芯片写「不限速」，不是缺数据。
 *
 * 本模块为纯函数（仅 import type），供节点页调用与
 * scripts/check-node-card-tags.mjs 转译断言。
 */
export interface NodeCardTags {
  /** 去重后的活路用户数。 */
  userCount: number;
  /** 去重后的限速规则名（按转发出现顺序）；空＝无绑定。 */
  speedLimitNames: string[];
}

function chainNodeIds(tunnel: TunnelApiItem): number[] {
  const ids: number[] = [];
  const push = (items?: Array<{ nodeId: number }>) => {
    for (const item of items ?? []) {
      if (typeof item?.nodeId === "number") ids.push(item.nodeId);
    }
  };
  push(tunnel.inNodeId);
  push(tunnel.outNodeId);
  for (const hop of tunnel.chainNodes ?? []) push(hop);
  return ids;
}

export function computeNodeCardTags(
  nodeIds: number[],
  forwards: ForwardApiItem[],
  tunnels: TunnelApiItem[],
  speedLimits: SpeedLimitApiItem[],
): Map<number, NodeCardTags> {
  const result = new Map<number, NodeCardTags>();
  for (const id of nodeIds) {
    result.set(id, { userCount: 0, speedLimitNames: [] });
  }
  const nameById = new Map<number, string>();
  for (const rule of speedLimits) {
    if (typeof rule?.id === "number" && typeof rule?.name === "string") {
      nameById.set(rule.id, rule.name);
    }
  }
  const tunnelById = new Map<number, TunnelApiItem>();
  for (const tunnel of tunnels) {
    if (typeof tunnel?.id === "number") tunnelById.set(tunnel.id, tunnel);
  }
  const usersByNode = new Map<number, Set<number>>();
  const namesByNode = new Map<number, string[]>();
  const seenName = new Map<number, Set<string>>();
  const addName = (nodeId: number, name: string) => {
    const trimmed = name.trim();
    if (!trimmed) return;
    let seen = seenName.get(nodeId);
    if (!seen) {
      seen = new Set<string>();
      seenName.set(nodeId, seen);
    }
    if (seen.has(trimmed)) return;
    seen.add(trimmed);
    let list = namesByNode.get(nodeId);
    if (!list) {
      list = [];
      namesByNode.set(nodeId, list);
    }
    list.push(trimmed);
  };
  for (const forward of forwards) {
    // 只计活路：转发正常且隧道启用（见模块头口径）。
    if (forward?.status !== 1) continue;
    if (typeof forward?.tunnelId !== "number") continue;
    const tunnel = tunnelById.get(forward.tunnelId);
    if (!tunnel || tunnel.status !== 1) continue;
    for (const nodeId of chainNodeIds(tunnel)) {
      if (!result.has(nodeId)) continue;
      if (typeof forward.userId === "number") {
        let users = usersByNode.get(nodeId);
        if (!users) {
          users = new Set<number>();
          usersByNode.set(nodeId, users);
        }
        users.add(forward.userId);
      }
      if (typeof forward.speedId === "number") {
        addName(nodeId, nameById.get(forward.speedId) ?? `规则#${forward.speedId}`);
      }
      if (typeof forward.ipSpeedId === "number") {
        addName(nodeId, nameById.get(forward.ipSpeedId) ?? `规则#${forward.ipSpeedId}`);
      }
      if (typeof forward.ipSpeedLimitName === "string") {
        addName(nodeId, forward.ipSpeedLimitName);
      }
    }
  }
  for (const [nodeId, entry] of result) {
    entry.userCount = usersByNode.get(nodeId)?.size ?? 0;
    entry.speedLimitNames = namesByNode.get(nodeId) ?? [];
  }
  return result;
}

/**
 * 本月网卡流量下行占比（流量进度条的刻度，X06 恢复）。
 * 节点无月度流量上限（后端只服务当月 [入, 出] 累计），进度条
 * 不冒充「用量/上限」，而是上下行分段：下行占比＝入/(入+出)。
 * 当月零流量返回 null，调用方不渲染进度条（PORT-022 口径：
 * 未上报用量不显示空白条）。
 */
export function trafficDownShare(month?: [number, number]): number | null {
  if (!month) return null;
  const total = month[0] + month[1];
  if (!(total > 0)) return null;
  return Math.round((month[0] / total) * 100);
}
