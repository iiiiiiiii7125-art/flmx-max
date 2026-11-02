export const TUNNEL_QUALITY_ENABLED_CONFIG_KEY =
  "monitor_tunnel_quality_enabled";
export const TUNNEL_QUALITY_INTERVAL_CONFIG_KEY =
  "monitor_tunnel_quality_interval_sec";

// 隧道质量开关的唯一判读点，与后端逐字对齐：handler
// isTunnelQualityMonitoringEnabled 与 prober 均为「值 trim＋小写后
// !== "false" 即开启」，键缺失/读取失败同为开启。前端全部判读面
// （config 页开关显示、config 页保存事件广播、隧道监控页自读）必须
// 走此函数，不许再各写 === "true"——R8-018：保存广播曾以 === "true"
// 判读，键从未落库时得出 false，把已挂载的监控页质量区误关，与其余
// 三面相反。门禁：pnpm run check:tunnel-quality-switch。
export const resolveTunnelQualityEnabled = (value: unknown): boolean => {
  if (typeof value !== "string") {
    return true;
  }

  return value.trim().toLowerCase() !== "false";
};
export const DEFAULT_TUNNEL_QUALITY_INTERVAL_SEC = 1;
export const MIN_TUNNEL_QUALITY_INTERVAL_SEC = 1;
export const MAX_TUNNEL_QUALITY_INTERVAL_SEC = 3600;

export const parseTunnelQualityIntervalSeconds = (value: unknown): number => {
  const seconds = Number(value);

  return Number.isInteger(seconds) &&
    seconds >= MIN_TUNNEL_QUALITY_INTERVAL_SEC &&
    seconds <= MAX_TUNNEL_QUALITY_INTERVAL_SEC
    ? seconds
    : DEFAULT_TUNNEL_QUALITY_INTERVAL_SEC;
};

export const validateTunnelQualityInterval = (value: string): string | null => {
  const normalized = value.trim();

  if (!normalized) {
    return "请输入探测间隔";
  }
  const seconds = Number(normalized);

  if (!Number.isInteger(seconds)) {
    return "探测间隔必须是整数";
  }
  if (
    seconds < MIN_TUNNEL_QUALITY_INTERVAL_SEC ||
    seconds > MAX_TUNNEL_QUALITY_INTERVAL_SEC
  ) {
    return `探测间隔必须在 ${MIN_TUNNEL_QUALITY_INTERVAL_SEC} 到 ${MAX_TUNNEL_QUALITY_INTERVAL_SEC} 秒之间`;
  }

  return null;
};

export const tunnelQualityIntervalLabel = (seconds: number): string =>
  seconds === 1 ? "每秒" : `每 ${seconds} 秒`;
