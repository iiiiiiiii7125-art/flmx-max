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
 * tune（node/tune），均为纯写操作。
 */
const WRITE_WORDS = new Set(
  "create save update delete remove assign reset toggle import install control regenerate pause resume redeploy batch force run refresh split rollback test dismiss restore export reconcile clear login register finish begin verify set upgrade tune".split(
    " ",
  ),
);
const isWrite = (segment: string) =>
  WRITE_WORDS.has(segment.split("-")[0]) || segment === "check-status";

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
  const result = {
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
