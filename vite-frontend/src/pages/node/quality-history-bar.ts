import type { TunnelQualityApiItem } from "../../api/types";

export type QualityHistorySegment = "entryToExit" | "exitToBing";

export interface QualityHistoryBarStyle {
  colorClass: string;
  statusText: string;
}

const QUALITY_BAR_NO_DATA: QualityHistoryBarStyle = {
  colorClass: "bg-default-200",
  statusText: "无数据",
};

const QUALITY_BAR_ERROR: QualityHistoryBarStyle = {
  colorClass: "bg-danger",
  statusText: "错误",
};

/**
 * 质量历史条的段级着色判定（R3-B07-003）。
 *
 * 历史行的 errorMessage 是快照级单值：任一段失败后端都会写入。
 * 旧实现以它首判，把同一行两段同时涂红——仅出口→目标段失败时，
 * 入口→出口段明明实测健康（latency>=0、loss=0）也被误报为错误。
 * 此后着色只按本段实测归因：
 *
 *   - 本段 latency >= 0（有实测值）：只按本段延迟/丢包着色，整行
 *     errorMessage 不得影响本段（失败的是另一段，与本段无关）；
 *   - 本段 latency < 0（-1 哨兵，本段未测到）：行内有 errorMessage
 *     时本段即该行的失败段（或整轮前置失败、两段皆未测），按
 *     「错误」着色；无 errorMessage 时是未测段，按中性「无数据」
 *     着色（Round 2 已定口径：如类型1 不存在的入口→出口段，不
 *     得涂绿冒充优秀、也不得按哨兵 100% 丢包涂红）。
 *
 * errorMessage 本身只由调用方拼进 tooltip 作整行补充，不参与着色。
 *
 * 部分未测（R8-024）：入口→出口段带 entryToExitPartialUntested 时，
 * 本段总值只含实测跳（至少一跳未测）——着色仍按实测值走梯度，但
 * 状态文案必须附「部分未测」，不得与全实测行同形（旧实现无此标记，
 * 部分未测行被读作完整路径值）。出口→目标段是单跳实测、无此形态。
 *
 * 本模块只允许 import type，供 scripts/check-quality-history-bar.mjs
 * 在 Node 里转译执行做回归断言。
 */
export function classifyQualityHistoryBar(
  q: TunnelQualityApiItem,
  segment: QualityHistorySegment,
): QualityHistoryBarStyle {
  const isEntry = segment === "entryToExit";
  const latency = isEntry ? q.entryToExitLatency : q.exitToBingLatency;
  const loss = isEntry ? q.entryToExitLoss : q.exitToBingLoss;

  if (latency < 0) {
    return q.errorMessage ? QUALITY_BAR_ERROR : QUALITY_BAR_NO_DATA;
  }
  const partialNote =
    isEntry && q.entryToExitPartialUntested ? "（部分未测）" : "";
  if (loss !== undefined && loss > 0) {
    return {
      colorClass: "bg-danger",
      statusText: `丢包 ${loss.toFixed(1)}%${partialNote}`,
    };
  }
  if (latency > 200) {
    return { colorClass: "bg-danger", statusText: `极差${partialNote}` };
  }
  if (latency > 100) {
    return { colorClass: "bg-warning", statusText: `较差${partialNote}` };
  }

  return { colorClass: "bg-success", statusText: `优秀${partialNote}` };
}
