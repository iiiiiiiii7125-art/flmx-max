import type {
  AxiosAdapter,
  AxiosResponse,
  InternalAxiosRequestConfig,
} from "axios";

import axios from "axios";
import toast from "react-hot-toast";

import { getRoleIdFromToken } from "@/utils/jwt";
import { getToken } from "@/utils/session";

/**
 * The online demo (VITE_DEMO=true builds, published with the docs): the
 * panel runs without a backend. Every API call is answered in the browser
 * from responses recorded on a panel seeded with sample data
 * (fixtures.json); changes report success but are not kept.
 */
export const IS_DEMO = import.meta.env.VITE_DEMO === "true";

type Entry = {
  key: string;
  res: { code: number; msg: string; data?: unknown };
};
type Fixtures = {
  recordedAt: number;
  admin: Record<string, Entry[]>;
  user: Record<string, Entry[]>;
};

export type DemoRole = "admin" | "user";

// The panel reads the role and expiry from the JWT payload; the demo's
// tokens carry them unsigned (nothing checks a signature here).
const demoToken = (sub: number, roleId: number, user: string) =>
  `demo.${btoa(JSON.stringify({ sub: String(sub), role_id: roleId, user, exp: 4102444800, iat: 1760000000 }))}.demo`;

// N-002 会话模型：localStorage 只存 token 与显示用 name，角色一律从 JWT
// 解析（getRoleIdFromToken），不再有上游的 role_id/admin 独立键。
const SESSIONS: Record<DemoRole, Record<string, string>> = {
  admin: {
    token: demoToken(1, 0, "admin@user"),
    name: "admin@user",
  },
  user: {
    token: demoToken(2, 1, "xiaoming"),
    name: "xiaoming",
  },
};

const writeSession = (role: DemoRole) => {
  // 清掉旧版本残留键，防止被篡改值在 demo 里继续生效（与 writeLoginSession 同口径）。
  localStorage.removeItem("role_id");
  localStorage.removeItem("admin");
  for (const [k, v] of Object.entries(SESSIONS[role]))
    localStorage.setItem(k, v);
};

export function demoRole(): DemoRole {
  const token = getToken();

  return token && getRoleIdFromToken(token) === 0 ? "admin" : "user";
}

/** Signs in as the demo admin or the demo user (and reloads). */
export function switchDemoRole(role: DemoRole) {
  writeSession(role);
  window.location.hash = "#/dashboard";
  window.location.reload();
}

/**
 * Writes report success without a stored result; the first word of the
 * last path segment tells them apart ("latest-results" is a read).
 * 词表按我方接口复核过：上游词表之外补 set（user-subscription/set、
 * user-inbound/set）、upgrade（node/upgrade、system/upgrade）、
 * tune（node/tune）。node/tune 例外（RR-C5-05）：其 status 读与各
 * 动作都回带状态对象、由 tuneAnswer 先于词表合成回答，词表仅兜底；
 * 同理 tunnel/delete-preview 与 tunnel/batch-delete-preview 是带
 * 数据的预览读、由 deletePreviewAnswer 先于词表合成（见下两函数）。
 * R4-C5-01/02/03 同族：node/install、subscription/template/refresh、
 * backup/import、backup/gist/restore、inbound/import、backup/export、
 * subscription/source/refresh、monitor/services/run、
 * federation/node/import 与 backup/gist/save（echo 形状不符）的回执
 * 均由 receiptAnswer 先于词表按真实形状合成（见该函数头注）。
 */
const WRITE_WORDS = new Set(
  "create save update delete remove assign reset toggle import install control regenerate pause resume redeploy batch force run refresh split rollback test dismiss restore export reconcile clear login register finish begin verify set upgrade tune".split(
    " ",
  ),
);
const isWrite = (segment: string) =>
  WRITE_WORDS.has(segment.split("-")[0]) ||
  segment === "check-status" ||
  // R4X-016: the only camelCase last segment in the API surface; the
  // first-word rule cannot see "update" inside it, so register the
  // full segment like check-status above.
  segment === "updatePassword";

/** Measurements: answered from the recorded lists, by id. */
const PROBES: Record<string, [list: string, field: string]> = {
  "inbound/probe": ["inbound/list", "probe"],
  "node/latency/probe": ["node/list", "latency"],
  "inbound/relay/probe": ["inbound/relay/list", "latency"],
};
const ALIASES: Record<string, string> = { "node/route/probe": "node/route" };

/**
 * Diagnosis (R3X-032 g/h): synthesized, not recorded. A seeded panel has
 * no agents, so a recorded forward/tunnel diagnose POST could only ever
 * be a failure payload — and the streaming variant bypasses axios
 * entirely (diagnosis-stream.ts already reports fallback in demo
 * builds, landing on this POST). Build one successful result from the
 * recorded lists instead, shaped like the real payloads
 * (ForwardDiagnosisApiData / TunnelDiagnosisApiData).
 */
function diagnoseAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  const forward = bare === "forward/diagnose";
  const tunnel = bare === "tunnel/diagnose";

  if (!forward && !tunnel) return null;
  let req: Record<string, unknown> = {};

  try {
    req = (typeof body === "string" ? JSON.parse(body) : body) ?? {};
  } catch {
    req = {};
  }
  const id = Number(forward ? req.forwardId : req.tunnelId);
  const listOf = (k: string) =>
    (book[k]?.[0]?.res.data ?? []) as Array<Record<string, unknown>>;
  const nodes = listOf("node/list");
  const tunnels = listOf("tunnel/list");
  const item =
    listOf(forward ? "forward/list" : "tunnel/list").find(
      (i) => Number(i.id) === id,
    ) ?? {};
  const name = String(item.name ?? (forward ? "演示转发" : "演示隧道"));
  const tunnelOf = forward
    ? tunnels.find((t) => Number(t.id) === Number(item.tunnelId))
    : item;
  const entryNodeId = Number(
    (tunnelOf?.inNodeId as Array<{ nodeId?: number }> | undefined)?.[0]
      ?.nodeId,
  );
  const nodeName =
    nodes.find((n) => Number(n.id) === entryNodeId)?.name ??
    name.split("→")[0]?.trim() ??
    name;
  const target = String(
    forward
      ? (item.remoteAddr ?? "")
      : tunnelOf?.probeTargetHost
        ? `${tunnelOf.probeTargetHost}:${tunnelOf.probeTargetPort || 443}`
        : "",
  );
  const [targetIp, targetPort] = target.split(":");
  // UDP-family chain segments (kcp/udp/quic) cannot be measured by the
  // panel's UDP probe: since R6-L4-01 the real payload marks such rows
  // unverified (success=false, averageTime/packetLoss null) instead of
  // fabricating numbers. Mirror that shape (the panel's
  // markDiagnosisItemUnverified) when the diagnosed tunnel rides one,
  // so the demo shows the neutral unverified presentation.
  const chainProtocols = [
    ...((tunnelOf?.inNodeId as Array<{ protocol?: string }> | undefined) ??
      []),
    ...((tunnelOf?.outNodeId as Array<{ protocol?: string }> | undefined) ??
      []),
  ].map((e) => String(e.protocol ?? "").toLowerCase());
  const udpProtocol = forward
    ? undefined
    : chainProtocols.find((p) => p === "kcp" || p === "udp" || p === "quic");
  const baseResult = {
    success: true,
    description: forward ? "转发目标连通性" : "隧道连通性",
    nodeName: String(nodeName),
    nodeId: String(entryNodeId || item.id || id || ""),
    targetIp: targetIp || "203.0.113.11",
    targetPort: Number(targetPort) || 443,
    message: "连接正常（演示合成数据）",
    averageTime: 24,
    packetLoss: 0,
    // The diagnosis modals group rows by chain position; without
    // fromChainType a row belongs to no group and never renders.
    fromChainType: 1,
    fromInx: 0,
  };
  const result = udpProtocol
    ? {
        ...baseResult,
        success: false,
        unverified: true,
        averageTime: null,
        packetLoss: null,
        message: "UDP 无法实测连通性（无通用回声协议），本项未验证",
        protocol: udpProtocol,
      }
    : baseResult;
  const data = forward
    ? { forwardName: name, timestamp: Date.now(), results: [result] }
    : {
        tunnelName: name,
        tunnelType: Number(item.type) === 1 ? "端口转发" : "隧道转发",
        timestamp: Date.now(),
        results: [result],
      };

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

/**
 * Tunnel delete previews (RR-C5-05): data-carrying POSTs whose last
 * segment starts with "delete", so the wordlist judges them writes
 * and answers data:null — but both pages fail closed on a null
 * preview, and the write answer's msg is "操作成功", so the demo
 * showed an error toast reading 操作成功 and the delete dialogs
 * never opened. Synthesize the previews from the recorded lists
 * instead, in the backend's exact shapes
 * (tunnel_delete_resolution.go): single {tunnelId, tunnelName,
 * forwardCount, sampleForwards}, batch {tunnelCount,
 * totalForwardCount, items, failures?}; the sample is capped at 5,
 * the backend's tunnelDeletePreviewSampleLimit.
 */
function deletePreviewAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  const single = bare === "tunnel/delete-preview";
  const batch = bare === "tunnel/batch-delete-preview";

  if (!single && !batch) return null;
  let req: Record<string, unknown> = {};

  try {
    req = (typeof body === "string" ? JSON.parse(body) : body) ?? {};
  } catch {
    req = {};
  }
  const listOf = (k: string) =>
    (book[k]?.[0]?.res.data ?? []) as Array<Record<string, unknown>>;
  const tunnels = listOf("tunnel/list");
  const forwards = listOf("forward/list");
  const previewOf = (t: Record<string, unknown>) => {
    const own = forwards.filter((f) => Number(f.tunnelId) === Number(t.id));

    return {
      tunnelId: Number(t.id),
      tunnelName: String(t.name ?? ""),
      forwardCount: own.length,
      sampleForwards: own.slice(0, 5).map((f) => ({
        id: Number(f.id),
        name: String(f.name ?? ""),
        userId: Number(f.userId ?? 0),
        userName: String(f.userName ?? ""),
        inPort: Number(f.inPort ?? 0),
      })),
    };
  };

  if (single) {
    const t = tunnels.find((x) => Number(x.id) === Number(req.id));

    if (!t) return null;

    return { code: 0, msg: "操作成功", data: previewOf(t), ts: Date.now() };
  }
  const ids = Array.isArray(req.ids) ? req.ids.map(Number) : [];
  const items: ReturnType<typeof previewOf>[] = [];
  const failures: Array<{ id: number; reason: string }> = [];

  for (const id of ids) {
    const t = tunnels.find((x) => Number(x.id) === id);

    if (t) items.push(previewOf(t));
    else failures.push({ id, reason: "隧道不存在" });
  }
  const data: Record<string, unknown> = {
    tunnelCount: items.length,
    totalForwardCount: items.reduce((n, i) => n + i.forwardCount, 0),
    items,
  };

  if (failures.length) data.failures = failures;

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

/**
 * Node tune (RR-C5-05): every call to node/tune — the "status" read
 * included — returns the agent's tune status object, but the
 * wordlist only sees the segment "tune" and answers data:null, so
 * the dialog waited on its spinner forever. A seeded panel has no
 * agents, so synthesize one status in the agent's exact shape
 * (go-gost/x/socket tuneStatus, plus the panel's derived
 * lineSupported flag): an agent that supports automatic tuning,
 * fully applied at the default line (1000 Mbps × 150 ms, the
 * agent's own defaultTuneParams), with no measurement or job yet —
 * the modal then renders its normal "not measured yet" state.
 */
function tuneAnswer(bare: string) {
  if (bare !== "node/tune") return null;
  const setting = (key: string, current: string) => ({
    key,
    current,
    target: current,
    supported: true,
    ok: true,
  });
  const group = (
    id: string,
    name: string,
    desc: string,
    settings: ReturnType<typeof setting>[],
  ) => ({ id, name, desc, applied: true, settings });
  const data = {
    params: { bandwidthMbps: 1000, rttMs: 150 },
    sizes: {
      ramMb: 4096,
      bdp: 18750000,
      bufMax: 37500000,
      bufDefault: 131072,
      bufReason: "2×BDP",
      tcpMem: "187500 250000 375000",
    },
    lineSupported: true,
    kernel: "6.8.0-31-generic",
    congestion: "bbr",
    available: "reno cubic bbr",
    hasBackup: true,
    automatic: true,
    groups: [
      group(
        "bbr",
        "BBR 拥塞控制",
        "高延迟、有丢包的线路上明显提速（需要内核 4.9+）",
        [
          setting("net.core.default_qdisc", "fq"),
          setting("net.ipv4.tcp_congestion_control", "bbr"),
        ],
      ),
      group(
        "buffer",
        "按带宽和延迟设缓冲区",
        "单连接跑满线路；按内存限定上限，小内存机器不会被连接占满",
        [
          setting("net.core.rmem_max", "37500000"),
          setting("net.core.wmem_max", "37500000"),
          setting("net.core.rmem_default", "131072"),
          setting("net.core.wmem_default", "131072"),
          setting("net.ipv4.tcp_rmem", "4096 131072 37500000"),
          setting("net.ipv4.tcp_wmem", "4096 131072 37500000"),
          setting("net.ipv4.tcp_mem", "187500 250000 375000"),
          setting("net.ipv4.tcp_window_scaling", "1"),
          setting("net.ipv4.tcp_moderate_rcvbuf", "1"),
          setting("net.ipv4.tcp_adv_win_scale", "1"),
          setting("net.ipv4.udp_rmem_min", "16384"),
          setting("net.ipv4.udp_wmem_min", "16384"),
        ],
      ),
      group(
        "tcp",
        "TCP 连接优化",
        "Fast Open、空闲后不降速、自动探测 MTU、更快回收连接",
        [
          setting("net.ipv4.tcp_fastopen", "3"),
          setting("net.ipv4.tcp_slow_start_after_idle", "0"),
          setting("net.ipv4.tcp_mtu_probing", "1"),
          setting("net.ipv4.tcp_fin_timeout", "15"),
          setting("net.ipv4.tcp_tw_reuse", "1"),
          setting("net.ipv4.tcp_keepalive_time", "600"),
          setting("net.ipv4.tcp_syncookies", "1"),
          setting("net.ipv4.tcp_no_metrics_save", "0"),
          setting("net.ipv4.tcp_sack", "1"),
          setting("net.ipv4.tcp_dsack", "1"),
          setting("net.ipv4.tcp_timestamps", "1"),
        ],
      ),
      group(
        "backlog",
        "高并发",
        "连接多时不丢新连接，中转出站可用的端口更多",
        [
          setting("net.core.somaxconn", "32768"),
          setting("net.core.netdev_max_backlog", "16384"),
          setting("net.core.netdev_budget", "600"),
          setting("net.core.optmem_max", "65536"),
          setting("net.ipv4.tcp_max_syn_backlog", "16384"),
          setting("net.netfilter.nf_conntrack_max", "262144"),
          setting("net.ipv4.ip_local_port_range", "10000 65535"),
        ],
      ),
      group("forward", "IPv4 转发", "内核转发（nftables 端口转发）需要", [
        setting("net.ipv4.ip_forward", "1"),
      ]),
      group("files", "文件句柄上限", "系统允许打开更多连接和文件", [
        setting("fs.file-max", "1048576"),
        setting("fs.nr_open", "1048576"),
      ]),
    ],
  };

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

const parseBody = (body: unknown): Record<string, unknown> => {
  try {
    return (typeof body === "string" ? JSON.parse(body) : body) ?? {};
  } catch {
    return {};
  }
};

const listOf = (book: Record<string, Entry[]>, k: string) =>
  (book[k]?.[0]?.res.data ?? []) as Array<Record<string, unknown>>;

/**
 * Data-carrying action POSTs (R4-C5-01/02/03 + I01/I02): the wordlist
 * judges these writes and answers data:null (or, for save, an echo of
 * the request body), but their pages consume a real receipt — a null
 * receipt makes node/install and template/refresh error-toast the
 * write answer's own msg ("操作成功"), leaves the import receipt
 * panels permanently empty, and breaks the backup export blob path.
 * Synthesize each receipt in the backend's exact shape:
 *  - node/install: one sample command per mode (a real panel embeds
 *    the node's secret and the panel address; the demo shows the
 *    shape with placeholder values, labelled as synthesized);
 *  - subscription/template/refresh: the recorded template row, with
 *    the fetch error cleared and lastFetch re-stamped;
 *  - backup/import: counts derived from the submitted file itself
 *    (the visitor picks a real backup file; its segment arrays are
 *    in the request body), with the removed group segments counted
 *    as *Ignored and named in importWarnings in the backend's own
 *    wording — a zero-skip receipt for current-format files;
 *  - backup/gist/restore: the same receipt shape, counted from the
 *    recorded lists (the demo's own data set is what a restore of
 *    the demo backup would bring back);
 *  - inbound/import: one created id per non-empty pasted line;
 *  - subscription/source/refresh: a zero-change sync result;
 *  - monitor/services/run: one successful result row (the demo has
 *    no agents, but "ran and failed" would be a false claim — the
 *    recorded definitions/results make a success row the honest
 *    sample);
 *  - federation/node/import: a fresh import whose peer version is
 *    the recorded book's latest version, with no version warning;
 *  - backup/gist/save: a GistBackupStatus built from the recorded
 *    status plus the submitted token/password/interval semantics
 *    (the save echo previously returned the raw request body, which
 *    the page applied as an unconfigured status).
 * backup/export is not an envelope at all: it returns a download
 * marker the adapter turns into an attachment response (see
 * demoAdapter), carrying a backup file assembled from the recorded
 * lists in the real BackupData envelope (version "1.0"). The
 * configs segment is deliberately not synthesized — the recorded
 * config rows are real panel configuration and a demo download
 * must not carry them out.
 */
function installAnswer(bare: string, body: unknown) {
  if (bare !== "node/install") return null;
  const req = parseBody(body);
  const mode = String(req.mode ?? "script");
  const head =
    "# 演示站合成的示例命令：真实面板会生成带该节点密钥与面板地址的一次性命令";
  const data =
    mode === "docker"
      ? `${head}\ndocker run -d --name flmx-agent --network host --cap-add NET_ADMIN${req.privileged === true ? " --privileged" : ""} --restart unless-stopped ghcr.io/example/flmx-agent:latest`
      : mode === "docker-compose"
        ? `${head}\n# 面板生成 docker-compose.yaml（服务 flmx-agent，network_mode: host）后执行\ndocker compose up -d`
        : `${head}\ncurl -fsSL https://example.com/flmx/install.sh | bash -s -- --addr https://panel.example.com --secret <节点密钥>`;

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

function templateRefreshAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  if (bare !== "subscription/template/refresh") return null;
  const req = parseBody(body);
  const items = listOf(book, "subscription/template/list");
  const item = items.find((i) => Number(i.id) === Number(req.id)) ?? items[0];

  if (!item) return null;

  return {
    code: 0,
    msg: "操作成功",
    data: { ...structuredClone(item), lastError: "", lastFetch: Date.now() },
    ts: Date.now(),
  };
}

const IMPORT_COUNT_KEYS = [
  "usersImported",
  "nodesImported",
  "tunnelsImported",
  "forwardsImported",
  "userTunnelsImported",
  "speedLimitsImported",
  "tunnelGroupsIgnored",
  "userGroupsIgnored",
  "permissionsIgnored",
  "configsImported",
  "inboundsImported",
  "userInboundsImported",
  "inboundKernelsImported",
  "inboundRelaysImported",
  "inboundCountersImported",
  "inboundSubscriptionsImported",
  "inboundSubscriptionCountersImported",
  "inboundSourcesImported",
  "inboundShieldsImported",
  "inboundNodeGroupsIgnored",
  "inboundTemplatesImported",
  "inboundLegacyUsersImported",
  "announcementsImported",
  "monitorPermissionsImported",
  "userSubscriptionsImported",
  "serviceMonitorsImported",
  "nodeSSHConfigsImported",
  "peerSharesImported",
];

function importReceipt(
  counts: Record<string, number>,
  warnings: string[],
) {
  const data: Record<string, unknown> = {};

  for (const k of IMPORT_COUNT_KEYS) data[k] = counts[k] ?? 0;
  data.userSubscriptionsSkipped = 0;
  if (warnings.length) data.importWarnings = warnings;

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

function backupImportAnswer(bare: string, body: unknown) {
  if (bare !== "backup/import") return null;
  const req = parseBody(body);
  const types = Array.isArray(req.types) ? req.types.map(String) : [];
  const on = (t: string) => types.length === 0 || types.includes(t);
  const len = (v: unknown) => (Array.isArray(v) ? v.length : 0);
  const counts: Record<string, number> = {};

  if (on("users")) counts.usersImported = len(req.users);
  if (on("nodes")) {
    counts.nodesImported = len(req.nodes);
    counts.nodeSSHConfigsImported = len(req.nodeSSHConfigs);
    counts.peerSharesImported = len(req.peerShares);
  }
  if (on("tunnels")) counts.tunnelsImported = len(req.tunnels);
  if (on("forwards")) counts.forwardsImported = len(req.forwards);
  if (on("userTunnels")) counts.userTunnelsImported = len(req.userTunnels);
  if (on("speedLimits")) counts.speedLimitsImported = len(req.speedLimits);
  if (on("configs"))
    counts.configsImported =
      req.configs && typeof req.configs === "object"
        ? Object.keys(req.configs).length
        : 0;
  if (on("userInbounds")) counts.userInboundsImported = len(req.userInbounds);
  if (on("announcements"))
    counts.announcementsImported = len(req.announcements);
  if (on("monitorPermissions"))
    counts.monitorPermissionsImported = len(req.monitorPermissions);
  if (on("userSubscriptions"))
    counts.userSubscriptionsImported = len(req.userSubscriptions);
  if (on("serviceMonitors"))
    counts.serviceMonitorsImported = len(req.serviceMonitors);
  if (on("inbounds")) {
    const inb = (req.inbounds ?? {}) as Record<string, unknown>;

    counts.inboundKernelsImported = len(inb.kernels);
    counts.inboundsImported = len(inb.inbounds);
    counts.inboundRelaysImported = len(inb.relays);
    counts.inboundCountersImported = len(inb.counters);
    counts.inboundSubscriptionsImported = len(inb.subscriptions);
    counts.inboundSubscriptionCountersImported = len(
      inb.subscriptionCounters,
    );
    counts.inboundSourcesImported = len(inb.sources);
    counts.inboundShieldsImported = len(inb.shields);
    counts.inboundTemplatesImported = len(inb.templates);
    counts.inboundLegacyUsersImported = len(inb.users);
    counts.inboundNodeGroupsIgnored = len(inb.nodeGroups);
  }
  // The removed group segments are noted on every import that
  // carries them, whatever segment set was selected (the backend's
  // noteRemovedGroupSegments口径), in its own warning wording.
  counts.tunnelGroupsIgnored = len(req.tunnelGroups);
  counts.userGroupsIgnored = len(req.userGroups);
  counts.permissionsIgnored = len(req.permissions);
  const warnings: string[] = [];

  if (counts.tunnelGroupsIgnored)
    warnings.push(
      `备份恢复：隧道分组功能已移除，备份中的 tunnelGroups 段（${counts.tunnelGroupsIgnored} 条）已忽略未恢复`,
    );
  if (counts.userGroupsIgnored)
    warnings.push(
      `备份恢复：用户分组功能已移除，备份中的 userGroups 段（${counts.userGroupsIgnored} 条）已忽略未恢复`,
    );
  if (counts.permissionsIgnored)
    warnings.push(
      `备份恢复：分组权限功能已移除，备份中的 permissions 段（${counts.permissionsIgnored} 条）已忽略未恢复`,
    );
  if (counts.inboundNodeGroupsIgnored)
    warnings.push(
      `备份恢复：节点组功能已移除，备份中的 nodeGroups 段（${counts.inboundNodeGroupsIgnored} 条）已忽略未恢复`,
    );

  return importReceipt(counts, warnings);
}

function gistRestoreAnswer(bare: string, book: Record<string, Entry[]>) {
  if (bare !== "backup/gist/restore") return null;
  const n = (k: string) => listOf(book, k).length;

  return importReceipt(
    {
      usersImported: n("user/list"),
      nodesImported: n("node/list"),
      tunnelsImported: n("tunnel/list"),
      forwardsImported: n("forward/list"),
      speedLimitsImported: n("speed-limit/list"),
      userSubscriptionsImported: n("user-subscription/list"),
      serviceMonitorsImported: n("monitor/services"),
      inboundKernelsImported: n("kernel/list"),
      inboundsImported: n("inbound/list"),
      inboundRelaysImported: n("inbound/relay/list"),
      inboundSourcesImported: n("subscription/source/list"),
      inboundTemplatesImported: n("subscription/template/list"),
    },
    [],
  );
}

function inboundImportAnswer(bare: string, body: unknown) {
  if (bare !== "inbound/import") return null;
  const req = parseBody(body);
  const lines = String(req.links ?? "")
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean);

  return {
    code: 0,
    msg: "操作成功",
    data: { created: lines.map((_, i) => 9001 + i), errors: [] },
    ts: Date.now(),
  };
}

function sourceRefreshAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  if (bare !== "subscription/source/refresh") return null;
  const req = parseBody(body);
  const source =
    listOf(book, "subscription/source/list").find(
      (s) => Number(s.id) === Number(req.id),
    ) ?? { id: Number(req.id) || 0 };
  const result = {
    added: 0,
    updated: 0,
    removed: 0,
    kept: 0,
    keptUnknown: 0,
    keptStale: 0,
    shielded: 0,
    status: "ok",
    truncated: 0,
    errors: [],
  };

  return { code: 0, msg: "操作成功", data: { source, result }, ts: Date.now() };
}

function serviceRunAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  if (bare !== "monitor/services/run") return null;
  const req = parseBody(body);
  const id = Number(req.id);
  const def = listOf(book, "monitor/services").find(
    (m) => Number(m.id) === id,
  );

  return {
    code: 0,
    msg: "操作成功",
    data: {
      id: Date.now() % 100000,
      monitorId: id,
      nodeId: Number(def?.nodeId ?? 0),
      timestamp: Date.now(),
      success: 1,
      latencyMs: 26,
      statusCode: 0,
      errorMessage: "",
    },
    ts: Date.now(),
  };
}

function federationImportAnswer(bare: string, book: Record<string, Entry[]>) {
  if (bare !== "federation/node/import") return null;
  const version = book["system/version"]?.[0]?.res.data as
    | { latestVersion?: string; currentVersion?: string }
    | undefined;
  const panelVersion = version?.latestVersion || version?.currentVersion || "";
  const data: Record<string, unknown> = { refreshed: false, versionWarning: "" };

  if (panelVersion) data.panelVersion = panelVersion;

  return { code: 0, msg: "操作成功", data, ts: Date.now() };
}

function gistSaveAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  if (bare !== "backup/gist/save") return null;
  const req = parseBody(body);
  const prev = (book["backup/gist/status"]?.[0]?.res.data ?? {}) as Record<
    string,
    unknown
  >;
  const uploaded = book["backup/gist/upload"]?.[0]?.res.data as
    | { status?: Record<string, unknown> }
    | undefined;
  const status: Record<string, unknown> = {
    configured: false,
    tokenHint: "",
    encrypted: false,
    intervalHours: 0,
    gistUrl: "",
    lastAt: 0,
    lastUpload: 0,
    lastError: "",
    ...prev,
  };

  if (req.clearToken === true) {
    status.configured = false;
    status.tokenHint = "";
    status.gistUrl = "";
  } else if (typeof req.token === "string" && req.token) {
    const t = req.token;

    status.configured = true;
    status.tokenHint =
      t.length >= 8 ? `${t.slice(0, 4)}••••••••${t.slice(-4)}` : "••••••••";
    if (!status.gistUrl && uploaded?.status?.gistUrl)
      status.gistUrl = uploaded.status.gistUrl;
  }
  if (req.clearPassword === true) status.encrypted = false;
  else if (typeof req.password === "string" && req.password)
    status.encrypted = true;
  if (typeof req.intervalHours === "number")
    status.intervalHours = req.intervalHours;

  return { code: 0, msg: "操作成功", data: status, ts: Date.now() };
}

function exportAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  if (bare !== "backup/export") return null;
  const req = parseBody(body);
  const types = Array.isArray(req.types) ? req.types.map(String) : [];
  const on = (t: string) => types.length === 0 || types.includes(t);
  const data: Record<string, unknown> = {
    version: "1.0",
    exportedAt: Date.now(),
  };
  const put = (type: string, key: string, fixture: string) => {
    if (!on(type)) return;
    const rows = book[fixture]?.[0]?.res.data;

    if (Array.isArray(rows) && rows.length) data[key] = structuredClone(rows);
  };

  put("users", "users", "user/list");
  put("nodes", "nodes", "node/list");
  put("tunnels", "tunnels", "tunnel/list");
  put("forwards", "forwards", "forward/list");
  put("userTunnels", "userTunnels", "tunnel/user/list");
  put("speedLimits", "speedLimits", "speed-limit/list");
  put("userSubscriptions", "userSubscriptions", "user-subscription/list");
  put("serviceMonitors", "serviceMonitors", "monitor/services");
  if (on("inbounds")) {
    data.inbounds = {
      kernels: structuredClone(listOf(book, "kernel/list")),
      inbounds: structuredClone(listOf(book, "inbound/list")),
      relays: structuredClone(listOf(book, "inbound/relay/list")),
      sources: structuredClone(listOf(book, "subscription/source/list")),
      templates: structuredClone(listOf(book, "subscription/template/list")),
    };
  }

  return {
    __demoDownload: {
      filename: "backup.json",
      content: JSON.stringify(data, null, 2),
    },
  };
}

/**
 * Gist versions: the recording holds an empty list (a seeded panel
 * has no real Gist to list), which leaves the restore entry point
 * unrendered and the whole restore surface unreachable in the demo.
 * Same precedent as the synthesized upload fixture: present the one
 * version the demo's own upload produced, stamped an hour ago.
 */
function gistVersionsAnswer(bare: string) {
  if (bare !== "backup/gist/versions") return null;

  return {
    code: 0,
    msg: "操作成功",
    data: [{ version: "demo-v1", committedAt: Date.now() - 3600_000 }],
    ts: Date.now(),
  };
}

function receiptAnswer(
  bare: string,
  body: unknown,
  book: Record<string, Entry[]>,
) {
  return (
    gistVersionsAnswer(bare) ??
    installAnswer(bare, body) ??
    templateRefreshAnswer(bare, body, book) ??
    backupImportAnswer(bare, body) ??
    gistRestoreAnswer(bare, book) ??
    inboundImportAnswer(bare, body) ??
    sourceRefreshAnswer(bare, body, book) ??
    serviceRunAnswer(bare, body, book) ??
    federationImportAnswer(bare, book) ??
    gistSaveAnswer(bare, body, book) ??
    exportAnswer(bare, body, book)
  );
}

const canon = (body: unknown) => {
  if (body === undefined || body === null || body === "") return "";
  try {
    const value = typeof body === "string" ? JSON.parse(body) : body;

    return JSON.stringify(sortKeys(value));
  } catch {
    return String(body);
  }
};

function sortKeys(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sortKeys);
  if (v && typeof v === "object") {
    return Object.fromEntries(
      Object.keys(v as object)
        .sort()
        .map((k) => [k, sortKeys((v as Record<string, unknown>)[k])]),
    );
  }

  return v;
}

/**
 * Moves recorded times (last seen, expiry, chart windows) forward by the
 * time since the recording, so the demo always looks current.
 */
function shiftTimes(v: unknown, recordedAt: number, delta: number): unknown {
  if (typeof v === "number") {
    return v > 1.6e12 && Math.abs(v - recordedAt) < 400 * 86400000
      ? v + delta
      : v;
  }
  if (Array.isArray(v)) return v.map((x) => shiftTimes(x, recordedAt, delta));
  if (v && typeof v === "object") {
    return Object.fromEntries(
      Object.entries(v as object).map(([k, x]) => [
        k,
        shiftTimes(x, recordedAt, delta),
      ]),
    );
  }

  return v;
}

let fixtures: Fixtures | null = null;

// The check is inline so other builds drop the recording entirely.
const loadFixtures = async (): Promise<Fixtures> =>
  import.meta.env.VITE_DEMO === "true"
    ? ((await import("./fixtures.json")).default as Fixtures)
    : { recordedAt: 0, admin: {}, user: {} };
let lastNotice = 0;

async function answer(config: InternalAxiosRequestConfig) {
  fixtures ??= await loadFixtures();
  const raw = (config.url ?? "").replace(/^https?:\/\/[^/]+/, "");
  const path = raw.replace(/^\/?(api\/v1\/)?/, "");
  let bare = path.split("?")[0];

  bare = ALIASES[bare] ?? bare;
  const key = canon(config.data);
  const book = fixtures[demoRole()];
  const entries =
    book[path] ??
    book[bare] ??
    Object.entries(book).find(([p]) => p.split("?")[0] === bare)?.[1];
  const last = bare.split("/").pop() ?? "";
  const write = isWrite(last);
  const delta = Math.max(0, Date.now() - fixtures.recordedAt);

  const diagnosis = diagnoseAnswer(bare, config.data, book);

  if (diagnosis) return diagnosis;
  const delPreview = deletePreviewAnswer(bare, config.data, book);

  if (delPreview) return delPreview;
  const tune = tuneAnswer(bare);

  if (tune) return tune;
  const receipt = receiptAnswer(bare, config.data, book);

  if (receipt) return receipt;
  if (PROBES[bare]) {
    const [list, field] = PROBES[bare];
    const ids: number[] = JSON.parse(String(config.data || "{}")).ids ?? [];
    const items = (book[list]?.[0]?.res.data ?? []) as Array<
      Record<string, unknown>
    >;
    const data = Object.fromEntries(
      items
        .filter((i) => ids.length === 0 || ids.includes(Number(i.id)))
        .filter((i) => i[field])
        .map((i) => [String(i.id), i[field]]),
    );

    await new Promise((r) => setTimeout(r, 600));

    return shiftTimes(
      { code: 0, msg: "操作成功", data },
      fixtures.recordedAt,
      delta,
    );
  }
  if (entries?.length && !write) {
    const hit = entries.find((e) => e.key === key) ?? entries[0];

    return shiftTimes(structuredClone(hit.res), fixtures.recordedAt, delta);
  }
  if (write) {
    if (Date.now() - lastNotice > 4000) {
      lastNotice = Date.now();
      toast("演示站不会保存修改，刷新后还原", { icon: "ℹ️", id: "demo-write" });
    }
    let echo: unknown = null;

    if (/^(save|create|update)$/.test(last)) {
      try {
        const body =
          typeof config.data === "string"
            ? JSON.parse(config.data)
            : config.data;

        echo = { id: Date.now() % 100000, ...body };
      } catch {
        echo = null;
      }
    }

    return { code: 0, msg: "操作成功", data: echo, ts: Date.now() };
  }

  // Not recorded: an empty answer most pages render as "nothing yet". The
  // CI check (scripts/demo/check.mjs) fails on this warning, so new pages
  // get recorded (scripts/demo/README.md).
  // eslint-disable-next-line no-console
  console.warn(`[demo] not recorded: ${bare}`);

  return { code: 0, msg: "操作成功", data: [], ts: Date.now() };
}

const demoAdapter: AxiosAdapter = async (config) => {
  const data = await answer(config);
  // backup/export answers with a download marker, not an envelope:
  // the export path keys on Content-Disposition: attachment and
  // saves the blob (api/index.ts exportBackup, D5-01口径) — mirror
  // the real response's status, headers and filename exactly.
  const download = (
    data as { __demoDownload?: { filename: string; content: string } }
  )?.__demoDownload;

  if (download) {
    return {
      data: new Blob([download.content], { type: "application/json" }),
      status: 200,
      statusText: "OK",
      headers: {
        "content-disposition": `attachment; filename=${download.filename}`,
        "content-type": "application/json",
      },
      config,
      request: {},
    } as AxiosResponse;
  }

  return {
    data,
    status: 200,
    statusText: "OK",
    headers: {},
    config,
    request: {},
  } as AxiosResponse;
};

/** Answers the API in the browser and signs in as the demo admin. */
export function installDemo() {
  axios.defaults.adapter = demoAdapter;
  if (!localStorage.getItem("token")?.startsWith("demo")) {
    writeSession("admin");
  }
}

/**
 * The node page and monitoring get live metrics over a WebSocket; the demo
 * plays plausible ones for the demo nodes instead.
 */
export function startDemoRealtime(
  onMessage: (m: Record<string, unknown>) => void,
) {
  let stopped = false;
  let timer = 0;
  const totals: Record<number, [number, number]> = {};

  void (async () => {
    fixtures ??= await loadFixtures();
    const nodes = (fixtures.admin["node/list"]?.[0]?.res.data ?? []) as Array<{
      id: number;
    }>;
    const tick = () => {
      if (stopped) return;
      const t = Date.now() / 1000;

      for (const n of nodes) {
        const wave = 0.5 + 0.5 * Math.sin(t / 40 + n.id);
        const inSpeed = Math.round(
          (8 + 40 * wave + 6 * Math.random()) * 131072,
        );
        const outSpeed = Math.round(inSpeed * (0.9 + 0.2 * Math.random()));
        const [inTotal, outTotal] = totals[n.id] ?? [
          n.id * 7.3e11,
          n.id * 6.9e11,
        ];

        totals[n.id] = [inTotal + inSpeed * 3, outTotal + outSpeed * 3];
        onMessage({ id: n.id, type: "status", data: 1 });
        onMessage({
          id: n.id,
          type: "metric",
          data: {
            cpuUsage: Math.round((6 + 30 * wave + 5 * Math.random()) * 10) / 10,
            memoryUsage: Math.round((32 + 12 * wave) * 10) / 10,
            diskUsage: 23.5,
            netInBytes: totals[n.id][0],
            netOutBytes: totals[n.id][1],
            netInSpeed: inSpeed,
            netOutSpeed: outSpeed,
            load1: Math.round((0.2 + 1.2 * wave) * 100) / 100,
            load5: Math.round((0.3 + wave) * 100) / 100,
            load15: Math.round((0.4 + 0.8 * wave) * 100) / 100,
            tcpConns: Math.round(120 + 500 * wave),
            udpConns: Math.round(12 + 50 * wave),
            uptime: 86400 * 21 + Math.round(t % 86400),
          },
        });
      }
      timer = window.setTimeout(tick, 3000);
    };

    tick();
  })();

  return () => {
    stopped = true;
    window.clearTimeout(timer);
  };
}
