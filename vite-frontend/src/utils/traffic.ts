export type TrafficUnit = "MB" | "GB" | "TB" | "PB";

export const MIB = 1024 * 1024;
export const GIB = 1024 * MIB;

export const TRAFFIC_UNIT_MIB: Record<TrafficUnit, number> = {
  MB: 1,
  GB: 1024,
  TB: 1024 ** 2,
  PB: 1024 ** 3,
};

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

export function formatTraffic(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";

  let value = bytes;
  let unit = 0;

  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit++;
  }

  return `${unit === 0 ? Math.floor(value) : value.toFixed(2)} ${BYTE_UNITS[unit]}`;
}

export function flowLimitMiB(flowGB: number, flowMiB?: number): number {
  return flowMiB && flowMiB > 0 ? flowMiB : flowGB * 1024;
}

export function flowLimitBytes(flowGB: number, flowMiB?: number): number {
  return flowLimitMiB(flowGB, flowMiB) * MIB;
}

export function formatFlowLimit(flowGB: number, flowMiB?: number): string {
  if (flowGB === 99999 && !flowMiB) return "无限制";
  // R10X-006（K-001）：后端 R9X-001 后 0 读作不限额；精确限额（经
  // FlowMiB）<=0 才是不限，(0,512) 这类亚 GB 限额仍须显示数值。
  if (flowLimitMiB(flowGB, flowMiB) <= 0) return "无限制";

  return formatTraffic(flowLimitBytes(flowGB, flowMiB));
}

export function preferredTrafficUnit(mib: number): TrafficUnit {
  if (mib <= 0) return "GB";
  if (mib > 0 && mib % TRAFFIC_UNIT_MIB.PB === 0) return "PB";
  if (mib > 0 && mib % TRAFFIC_UNIT_MIB.TB === 0) return "TB";
  if (mib > 0 && mib % TRAFFIC_UNIT_MIB.GB === 0) return "GB";

  return "MB";
}

export function parseTrafficInput(
  value: string,
  unit: TrafficUnit,
): number | null {
  const amount = Number(value);
  const mib = amount * TRAFFIC_UNIT_MIB[unit];

  if (
    !value.trim() ||
    !Number.isFinite(amount) ||
    // R10X-006（K-001）：0 是后端显式接纳的合法值（不限额），编辑面须
    // 放行 0 值原样回存；仅负值无效。
    amount < 0 ||
    !Number.isSafeInteger(mib)
  ) {
    return null;
  }

  // Backend stores bytes as int64. Keep the converted value within that range.
  if (mib > 8_796_093_022_207) return null;

  return mib;
}
