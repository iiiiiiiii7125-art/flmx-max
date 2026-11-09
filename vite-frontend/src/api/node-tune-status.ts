import type { NodeTuneStatus } from "./index";

/**
 * 把 agent 的 TuneStatus / Tune 回包归一成前端契约 NodeTuneStatus。
 *
 * 线上存在两种新版回包形状：
 * - 规范形状（agent 已对齐契约）：{ faces: {ipv4, bbr, kernel, nic},
 *   congestion, qdisc, warnings? }——原样返回；
 * - 2.1.0 agent 的扁平形状：{ ipv4Priority, bbr, kernel, nic,
 *   congestionControl, defaultQdisc, rpsSockFlowEntries, nofile,
 *   warnings? }——按字段对位归一（faces.ipv4=ipv4Priority、
 *   faces.bbr=bbr、faces.kernel=kernel、faces.nic=nic、
 *   congestion=congestionControl、qdisc=defaultQdisc）。
 *
 * 判旧口径（收紧后）：仅当 faces 与扁平关键字段（ipv4Priority /
 * congestionControl 任一）都缺失时才返回 null，调用方按「agent
 * 版本过旧」呈现。已退役的分组形态回包（2.0.0 及更早：
 * { params, sizes, groups, congestion, kernel:"<字符串>", ... }）
 * 必然落此判——它没有 faces、没有 ipv4Priority、也没有
 * congestionControl（它的拥塞字段叫 congestion，与新规范名撞键，
 * 故判别绝不能只看 congestion）。
 */
export function normalizeNodeTuneStatus(raw: unknown): NodeTuneStatus | null {
  if (!raw || typeof raw !== "object") return null;
  const r = raw as Record<string, unknown>;
  const faces = r.faces;
  if (faces && typeof faces === "object") {
    return raw as NodeTuneStatus;
  }
  if (
    typeof r.ipv4Priority === "boolean" ||
    typeof r.congestionControl === "string"
  ) {
    return {
      faces: {
        ipv4: r.ipv4Priority === true,
        bbr: r.bbr === true,
        kernel: r.kernel === true,
        nic: r.nic === true,
      },
      congestion:
        typeof r.congestionControl === "string" ? r.congestionControl : "",
      qdisc: typeof r.defaultQdisc === "string" ? r.defaultQdisc : "",
      ...(Array.isArray(r.warnings)
        ? { warnings: r.warnings as string[] }
        : {}),
    };
  }
  return null;
}
