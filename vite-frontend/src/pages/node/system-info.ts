export interface NodeSystemInfo {
  // Optional: a node that has not reported a figure leaves it undefined,
  // and the UI hides the meter instead of showing a fabricated 0%.
  cpuUsage?: number;
  memoryUsage?: number;
  uploadTraffic: number;
  downloadTraffic: number;
  uploadSpeed: number;
  downloadSpeed: number;
  uptime: number;
  diskUsage?: number;
  load1?: number;
  load5?: number;
  load15?: number;
  tcpConns?: number;
  udpConns?: number;
  netInSpeed?: number;
  netOutSpeed?: number;
}

type RawSystemInfo = Record<string, string | number | undefined>;

const toInteger = (value: string | number | undefined): number => {
  return Number.parseInt(String(value ?? 0), 10) || 0;
};

const toFloat = (value: string | number | undefined): number => {
  return Number.parseFloat(String(value ?? 0)) || 0;
};

const parseRawSystemInfo = (messageData: unknown): RawSystemInfo | null => {
  if (typeof messageData === "string") {
    try {
      const parsed = JSON.parse(messageData);

      if (parsed && typeof parsed === "object") {
        return parsed as RawSystemInfo;
      }

      return null;
    } catch {
      return null;
    }
  }

  if (messageData && typeof messageData === "object") {
    return messageData as RawSystemInfo;
  }

  return null;
};

/**
 * Heuristic to verify the parsed object actually contains system-info fields.
 * Prevents non-metric messages (command responses, etc.) from being mistakenly
 * treated as system info, which would reset all values to 0 and cause UI flicker.
 */
const SYSTEM_INFO_KEYS = [
  "uptime",
  "cpu_usage",
  "memory_usage",
  "disk_usage",
  "bytes_received",
  "bytes_transmitted",
  "net_in_speed",
  "net_out_speed",
  "tcp_conns",
  "udp_conns",
  "load1",
  "load5",
  "load15",
] as const;

const looksLikeSystemInfo = (raw: RawSystemInfo): boolean => {
  let matched = 0;

  for (const key of SYSTEM_INFO_KEYS) {
    if (key in raw && raw[key] !== undefined) {
      matched++;
      if (matched >= 3) {
        return true;
      }
    }
  }

  return false;
};

export const buildNodeSystemInfo = (
  messageData: unknown,
  previous: NodeSystemInfo | null | undefined,
): NodeSystemInfo | null => {
  const raw = parseRawSystemInfo(messageData);

  if (!raw || !looksLikeSystemInfo(raw)) {
    return null;
  }

  const uploadTraffic = toInteger(raw.bytes_transmitted);
  const downloadTraffic = toInteger(raw.bytes_received);
  const uptime = toInteger(raw.uptime);

  let uploadSpeed = 0;
  let downloadSpeed = 0;

  if (previous && previous.uptime) {
    const timeDiff = uptime - previous.uptime;

    if (timeDiff > 0 && timeDiff <= 10) {
      const uploadDiff = uploadTraffic - previous.uploadTraffic;
      const downloadDiff = downloadTraffic - previous.downloadTraffic;

      if (uploadTraffic >= previous.uploadTraffic && uploadDiff >= 0) {
        uploadSpeed = uploadDiff / timeDiff;
      }

      if (downloadTraffic >= previous.downloadTraffic && downloadDiff >= 0) {
        downloadSpeed = downloadDiff / timeDiff;
      }
    }
  }

  return {
    cpuUsage: raw.cpu_usage === undefined ? undefined : toFloat(raw.cpu_usage),
    memoryUsage:
      raw.memory_usage === undefined ? undefined : toFloat(raw.memory_usage),
    uploadTraffic,
    downloadTraffic,
    uploadSpeed,
    downloadSpeed,
    uptime,
    diskUsage: raw.disk_usage === undefined ? undefined : toFloat(raw.disk_usage),
    // Same undefined-preserving rule as the usage figures above
    // (A-F-02 family): a snapshot that omits a key leaves it undefined
    // instead of fabricating 0 — consumers read these through ?? chains
    // (e.g. netOutSpeed ?? derived uploadSpeed), so a fabricated 0
    // would also shadow the derived fallback.
    load1: raw.load1 === undefined ? undefined : toFloat(raw.load1),
    load5: raw.load5 === undefined ? undefined : toFloat(raw.load5),
    load15: raw.load15 === undefined ? undefined : toFloat(raw.load15),
    tcpConns: raw.tcp_conns === undefined ? undefined : toInteger(raw.tcp_conns),
    udpConns: raw.udp_conns === undefined ? undefined : toInteger(raw.udp_conns),
    netInSpeed:
      raw.net_in_speed === undefined ? undefined : toInteger(raw.net_in_speed),
    netOutSpeed:
      raw.net_out_speed === undefined
        ? undefined
        : toInteger(raw.net_out_speed),
  };
};
