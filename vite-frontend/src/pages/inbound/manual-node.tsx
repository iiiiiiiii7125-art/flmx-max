import { Segmented } from "@/pages/subscription/controls";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Input } from "@/shadcn-bridge/heroui/input";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";

// A node typed in field by field. It is turned into a standard share link
// and imported like a pasted one, so the backend parses and checks it the
// same way.

export type ManualProtocol =
  | "ss"
  | "vmess"
  | "vless"
  | "trojan"
  | "hysteria2"
  | "tuic"
  | "anytls"
  | "socks5";

type Security = "none" | "tls" | "reality";
type Network = "tcp" | "ws" | "grpc";

export interface ManualNode {
  protocol: ManualProtocol;
  name: string;
  host: string;
  port: string;
  method: string;
  uuid: string;
  password: string;
  username: string;
  alterId: string;
  flow: string;
  security: Security;
  sni: string;
  fingerprint: string;
  insecure: boolean;
  publicKey: string;
  shortId: string;
  network: Network;
  path: string;
  wsHost: string;
  serviceName: string;
  obfsPassword: string;
  ports: string;
  congestion: string;
}

export const MANUAL_PROTOCOLS: Array<[ManualProtocol, string]> = [
  ["ss", "Shadowsocks"],
  ["vless", "VLESS"],
  ["vmess", "VMess"],
  ["trojan", "Trojan"],
  ["hysteria2", "Hysteria2"],
  ["tuic", "TUIC v5"],
  ["anytls", "AnyTLS"],
  ["socks5", "SOCKS5"],
];

const FALLBACK_SS_METHODS = [
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
  "aes-128-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
];

const FINGERPRINTS = [
  "",
  "chrome",
  "firefox",
  "safari",
  "edge",
  "ios",
  "random",
];

export function emptyManualNode(): ManualNode {
  return {
    protocol: "ss",
    name: "",
    host: "",
    port: "",
    method: "2022-blake3-aes-128-gcm",
    uuid: "",
    password: "",
    username: "",
    alterId: "0",
    flow: "",
    security: "tls",
    sni: "",
    fingerprint: "",
    insecure: false,
    publicKey: "",
    shortId: "",
    network: "tcp",
    path: "",
    wsHost: "",
    serviceName: "",
    obfsPassword: "",
    ports: "",
    congestion: "bbr",
  };
}

function base64UTF8(s: string, url: boolean): string {
  const bytes = new TextEncoder().encode(s);
  let bin = "";

  for (const b of bytes) bin += String.fromCharCode(b);
  const b64 = btoa(bin);

  return url
    ? b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")
    : b64;
}

function randomUUID(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  // randomUUID needs a secure context; panels are often served over http.
  const b = crypto.getRandomValues(new Uint8Array(16));

  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");

  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

const usesUUID = (p: ManualProtocol) =>
  p === "vmess" || p === "vless" || p === "tuic";
const usesPassword = (p: ManualProtocol) => p !== "vmess" && p !== "vless";
const usesTransport = (p: ManualProtocol) =>
  p === "vmess" || p === "vless" || p === "trojan";
const alwaysTLS = (p: ManualProtocol) =>
  p === "trojan" || p === "hysteria2" || p === "tuic" || p === "anytls";

function tlsOn(n: ManualNode): boolean {
  if (alwaysTLS(n.protocol)) return true;
  if (n.protocol === "vless") return n.security !== "none";
  if (n.protocol === "vmess") return n.security === "tls";

  return false;
}

/** Vision 仅在 VLESS + TCP + 安全≠无时合法（presets.ts visionAllowed
 *  口径；手动表单无 ENC 字段，故安全=无时恒不合法）。 */
function visionAllowedManual(n: ManualNode): boolean {
  return n.protocol === "vless" && n.network === "tcp" && n.security !== "none";
}

// ---- 与后端同口径的前端校验（F-03/F-05/F-06）---------------------------

/** 严格 IPv4 字面量：4 段十进制、无前导 0（Go net.ParseIP 同口径）。 */
function parseIPv4Literal(s: string): number[] | null {
  const parts = s.split(".");

  if (parts.length !== 4) return null;
  const out: number[] = [];

  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null;
    if (p.length > 1 && p[0] === "0") return null;
    const v = Number(p);

    if (v > 255) return null;
    out.push(v);
  }

  return out;
}

/** IPv6 字面量解析（Go net.ParseIP 同口径）：至多一个 ::，末段可为内嵌
 *  IPv4；返回 16 字节，非法即 null。 */
function parseIPv6Literal(s: string): number[] | null {
  if (s.includes("%")) return null; // zone id 不接受
  const dbl = s.indexOf("::");

  if (dbl !== -1 && s.indexOf("::", dbl + 2) !== -1) return null;
  const parseGroup = (g: string, last: boolean): number[] | null => {
    if (g.includes(".")) {
      if (!last) return null;

      return parseIPv4Literal(g);
    }
    if (!/^[0-9a-fA-F]{1,4}$/.test(g)) return null;
    const v = parseInt(g, 16);

    return [v >> 8, v & 0xff];
  };
  const parseSide = (
    side: string,
  ): { bytes: number[]; groups: number } | null => {
    if (side === "") return { bytes: [], groups: 0 };
    const parts = side.split(":");
    const bytes: number[] = [];
    let groups = 0;

    for (let i = 0; i < parts.length; i++) {
      const b = parseGroup(parts[i], i === parts.length - 1);

      if (b === null) return null;
      bytes.push(...b);
      groups += b.length / 2;
    }

    return { bytes, groups };
  };

  if (dbl === -1) {
    const r = parseSide(s);

    if (!r || r.groups !== 8) return null;

    return r.bytes;
  }
  const head = parseSide(s.slice(0, dbl));
  const tail = parseSide(s.slice(dbl + 2));

  if (!head || !tail) return null;
  const total = head.groups + tail.groups;

  if (total >= 8) return null; // :: 至少压缩一组
  const zeros = new Array<number>((8 - total) * 2).fill(0);

  return [...head.bytes, ...zeros, ...tail.bytes];
}

const IMPORT_HOST_LOOPBACK = "地址不能是本机或 0.0.0.0";
const IMPORT_HOST_DENIED = "地址不能是云元数据、链路本地或保留/组播地址";

/** IPv4 按 netguard PolicyHardDenyOrLoopback 判定：RFC1918 与 CGNAT
 *  私网是合法导入对象，不拦。 */
function checkIPv4Import(o: number[]): string | null {
  if (o[0] === 0 && o[1] === 0 && o[2] === 0 && o[3] === 0)
    return IMPORT_HOST_LOOPBACK;
  if (o[0] === 127) return IMPORT_HOST_LOOPBACK;
  if (
    (o[0] === 169 && o[1] === 254 && o[2] === 169 && o[3] === 254) || // 云元数据
    (o[0] === 100 && o[1] === 100 && o[2] === 100 && o[3] === 200) || // 云元数据
    (o[0] === 169 && o[1] === 254) || // 169.254/16 链路本地
    o[0] === 0 || // 0.0.0.0/8 保留
    (o[0] === 192 && o[1] === 0 && o[2] === 0) || // 192.0.0.0/24 保留
    (o[0] === 198 && (o[1] & 0xfe) === 18) || // 198.18.0.0/15 保留
    o[0] >= 240 || // 240.0.0.0/4 保留
    (o[0] >= 224 && o[0] < 240) // 224.0.0.0/4 组播
  )
    return IMPORT_HOST_DENIED;

  return null;
}

/** IPv6 按同一政策判定：映射/翻译地址按内嵌 IPv4 同判。 */
function checkIPv6Import(b: number[]): string | null {
  if (b.every((x) => x === 0)) return IMPORT_HOST_LOOPBACK;
  if (b.slice(0, 15).every((x) => x === 0) && b[15] === 1)
    return IMPORT_HOST_LOOPBACK; // ::1
  // IPv4 映射 ::ffff:a.b.c.d
  if (b.slice(0, 10).every((x) => x === 0) && b[10] === 0xff && b[11] === 0xff)
    return checkIPv4Import(b.slice(12));
  // 本地 NAT64 64:ff9b:1::/48 整段保留
  if (
    b[0] === 0x00 &&
    b[1] === 0x64 &&
    b[2] === 0xff &&
    b[3] === 0x9b &&
    b[4] === 0x00 &&
    b[5] === 0x01
  )
    return IMPORT_HOST_DENIED;
  // NAT64 64:ff9b::/96：按内嵌 IPv4 判定
  if (
    b[0] === 0x00 &&
    b[1] === 0x64 &&
    b[2] === 0xff &&
    b[3] === 0x9b &&
    b.slice(4, 12).every((x) => x === 0)
  )
    return checkIPv4Import(b.slice(12));
  // 6to4 2002::/16：按内嵌 IPv4 判定
  if (b[0] === 0x20 && b[1] === 0x02) return checkIPv4Import(b.slice(2, 6));
  if (b[0] === 0xfe && (b[1] & 0xc0) === 0x80) return IMPORT_HOST_DENIED; // fe80::/10
  if (b[0] === 0xff) return IMPORT_HOST_DENIED; // ff00::/8 组播
  if (b[0] === 0x01 && b[1] === 0x00 && b.slice(2, 8).every((x) => x === 0))
    return IMPORT_HOST_DENIED; // 100::/64 保留
  // 云元数据 fd00:ec2::254
  if (
    b[0] === 0xfd &&
    b[1] === 0x00 &&
    b[2] === 0x0e &&
    b[3] === 0xc2 &&
    b.slice(4, 14).every((x) => x === 0) &&
    b[14] === 0x02 &&
    b[15] === 0x54
  )
    return IMPORT_HOST_DENIED;

  return null;
}

/** 与后端 ValidateImportHost（go-backend/internal/kernel/import.go）
 *  同口径：IP 字面量走 netguard 严格政策，主机名走 localhost 与非规范
 *  编码拦截；错误文案复用后端原文。 */
function validateImportHost(host: string): string | null {
  const v4 = parseIPv4Literal(host);

  if (v4) return checkIPv4Import(v4);
  const v6 = parseIPv6Literal(host);

  if (v6) return checkIPv6Import(v6);
  const lower = host.toLowerCase();

  if (lower === "localhost" || lower.endsWith(".localhost"))
    return "地址不能是 localhost";
  if (
    lower === "ip6-localhost" ||
    lower === "ip6-loopback" ||
    lower === "localhost.localdomain"
  )
    return "地址不能是 localhost";
  for (const seg of lower.split(".")) {
    if (seg.startsWith("0x")) return `地址 "${host}" 不正确`;
  }
  if (host !== "" && /^[0-9.]+$/.test(host)) return `地址 "${host}" 不正确`;
  if (
    host === "" ||
    host.length > 253 ||
    !/^[A-Za-z0-9.-]+$/.test(host) ||
    host.startsWith(".") ||
    host.endsWith(".")
  )
    return `地址 "${host}" 不正确`;

  return null;
}

/** 与后端 ValidHy2Ports（credentials.go）同口径：逗号分隔的端口或端口段。 */
function validHy2Ports(ports: string): boolean {
  if (ports.trim() === "") return true;
  for (const raw of ports.split(",")) {
    const item = raw.trim();
    const dash = item.indexOf("-");
    const lo = dash === -1 ? item : item.slice(0, dash);
    const hi = dash === -1 ? item : item.slice(dash + 1);

    for (const s of [lo, hi]) {
      const t = s.trim();

      if (!/^[+-]?\d+$/.test(t)) return false;
      const v = Number(t);

      if (!Number.isInteger(v) || v < 1 || v > 65535) return false;
    }
  }

  return true;
}

/** RawURL base64 解出的字节数；非法字符/填充/长度即 null（后端
 *  validateRealityOpts 只认 RawURLEncoding 解出恰 32 字节）。 */
function rawURLBase64ByteLength(s: string): number | null {
  if (!/^[A-Za-z0-9_-]+$/.test(s)) return null;
  if (s.length % 4 === 1) return null;
  const padded =
    s.replace(/-/g, "+").replace(/_/g, "/") +
    "=".repeat((4 - (s.length % 4)) % 4);

  return atob(padded).length;
}

/** The node as a share link, or an error message for a missing field. */
export function manualNodeLink(n: ManualNode): {
  link?: string;
  error?: string;
} {
  // F-06：地址栏不得带端口。方括号只接受严格 [IPv6] 形；[IPv6]:端口形
  // 按“地址不要带端口”拒；其余 ] 残留 / 未闭合括号按地址不正确拒。
  const rawHost = n.host.trim();

  if (!rawHost) return { error: "请填写地址" };
  let host = rawHost;

  if (rawHost.includes("]")) {
    if (!rawHost.startsWith("[")) return { error: `地址 "${rawHost}" 不正确` };
    const bracketed = /^\[([^\[\]]+)\]$/.exec(rawHost);

    if (!bracketed) {
      if (/^\[[^\[\]]+\]:/.test(rawHost))
        return { error: "地址不要带端口，端口请填在端口栏" };

      return { error: `地址 "${rawHost}" 不正确` };
    }
    host = bracketed[1];
  } else if (rawHost.startsWith("[")) {
    return { error: `地址 "${rawHost}" 不正确` };
  }
  if (/[\s/?#@]/.test(host)) return { error: "地址只填域名或 IP" };
  // 含冒号的 host 须为合法 IPv6 字面量，否则是 host:port / 多冒号形态。
  if (host.includes(":") && !parseIPv6Literal(host))
    return { error: "地址不要带端口，端口请填在端口栏" };
  const port = Number(n.port);

  if (!Number.isInteger(port) || port < 1 || port > 65535)
    return { error: "端口应为 1–65535" };
  // F-03：地址类别校验（与后端 ValidateImportHost 同口径）。
  const hostErr = validateImportHost(host);

  if (hostErr) return { error: hostErr };
  // F-08：密码统一 trim，全空格即按空处理。
  const pw = n.password.trim();

  if (usesUUID(n.protocol) && !n.uuid.trim()) return { error: "请填写 UUID" };
  // F-05：UUID 格式（与后端 uuidPattern 同形，大小写不敏感）。
  if (
    usesUUID(n.protocol) &&
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      n.uuid.trim(),
    )
  )
    return { error: "UUID 格式错误" };
  if (usesPassword(n.protocol) && n.protocol !== "socks5" && !pw)
    return { error: "请填写密码" };
  // F-07：SOCKS5 用户名/密码成对填写，或都留空。
  if (n.protocol === "socks5" && Boolean(n.username.trim()) !== Boolean(pw))
    return { error: "用户名和密码请成对填写（或都留空）" };
  // F-09：VMess alterId 非空时须为非负整数，不再静默归零。
  if (n.protocol === "vmess") {
    const aid = n.alterId.trim();

    if (aid !== "" && !/^\d+$/.test(aid)) return { error: "alterId 应为整数" };
  }
  if (n.protocol === "vless" && n.security === "reality" && !n.publicKey.trim())
    return { error: "Reality 需要填写公钥" };
  if (n.protocol === "vless" && n.security === "reality") {
    // F-05：pbk 须为 RawURL base64 解出恰 32 字节；sid 偶数位小写十六
    // 进制且 ≤16 位（与后端 validateRealityOpts 同口径）。
    if (rawURLBase64ByteLength(n.publicKey.trim()) !== 32)
      return {
        error: "Reality 公钥 pbk 格式错误（须为 32 字节的 RawURL base64）",
      };
    const sid = n.shortId.trim();

    if (
      sid !== "" &&
      (!/^[0-9a-f]+$/.test(sid) || sid.length % 2 !== 0 || sid.length > 16)
    )
      return {
        error:
          "Reality short-id 格式错误（须为偶数位小写十六进制，最多 16 位）",
      };
    // F-02：Reality 必须填 SNI（伪装站域名）。
    if (!n.sni.trim()) return { error: "Reality 需要填写 SNI（伪装站域名）" };
  }
  // F-01：Vision 仅在 VLESS + TCP + 安全≠无时合法（手动表单无 ENC
  // 字段，故安全=无时恒不合法；纯 ENC 形态表单表达不出来，不放行）。
  if (
    n.protocol === "vless" &&
    n.flow.trim() !== "" &&
    (n.network !== "tcp" || n.security === "none")
  )
    return { error: "Vision 流控仅支持 TCP 传输且需要 TLS 或 Reality" };
  // F-05：Hysteria2 端口跳跃（与后端 ValidHy2Ports 同口径）。
  if (n.protocol === "hysteria2" && !validHy2Ports(n.ports))
    return {
      error:
        "Hysteria2 ports 格式错误（须为逗号分隔的端口或端口段，端口在 1–65535 内）",
    };

  const addr = `${host.includes(":") ? `[${host}]` : host}:${port}`;
  const name = n.name.trim() || addr;
  const tag = `#${encodeURIComponent(name)}`;
  const enc = encodeURIComponent;
  const q = new URLSearchParams();
  const set = (k: string, v: string) => v.trim() && q.set(k, v.trim());
  const tls = tlsOn(n);

  if (tls) {
    set("sni", n.sni);
    set("fp", n.fingerprint);
    if (n.insecure && n.security !== "reality") q.set("insecure", "1");
  }
  if (usesTransport(n.protocol)) {
    q.set("type", n.network);
    if (n.network === "ws") {
      set("path", n.path);
      set("host", n.wsHost);
    }
    if (n.network === "grpc") set("serviceName", n.serviceName);
  }
  const query = () => (q.toString() ? `?${q.toString()}` : "");

  switch (n.protocol) {
    case "ss":
      return {
        link: `ss://${base64UTF8(`${n.method}:${pw}`, true)}@${addr}${tag}`,
      };
    case "vmess": {
      const v: Record<string, string | boolean> = {
        v: "2",
        ps: name,
        add: host,
        port: String(port),
        id: n.uuid.trim(),
        aid: String(Number(n.alterId.trim()) || 0),
        scy: "auto",
        net: n.network,
        type: "none",
        host: n.network === "ws" ? n.wsHost.trim() : "",
        path:
          n.network === "ws"
            ? n.path.trim()
            : n.network === "grpc"
              ? n.serviceName.trim()
              : "",
        tls: tls ? "tls" : "",
        sni: tls ? n.sni.trim() : "",
        fp: tls ? n.fingerprint : "",
      };

      if (tls && n.insecure) v.allowInsecure = true;

      return { link: `vmess://${base64UTF8(JSON.stringify(v), false)}` };
    }
    case "vless":
      q.set("encryption", "none");
      q.set("security", n.security);
      if (n.security === "reality") {
        set("pbk", n.publicKey);
        set("sid", n.shortId);
      }
      set("flow", n.flow);

      return { link: `vless://${enc(n.uuid.trim())}@${addr}${query()}${tag}` };
    case "trojan":
      q.set("security", "tls");

      return { link: `trojan://${enc(pw)}@${addr}${query()}${tag}` };
    case "hysteria2":
      if (n.obfsPassword) {
        q.set("obfs", "salamander");
        q.set("obfs-password", n.obfsPassword);
      }
      set("mport", n.ports);

      return {
        link: `hysteria2://${enc(pw)}@${addr}/${query()}${tag}`,
      };
    case "tuic":
      q.set("alpn", "h3");
      set("congestion_control", n.congestion);
      q.set("udp_relay_mode", "native");

      return {
        link: `tuic://${enc(n.uuid.trim())}:${enc(pw)}@${addr}${query()}${tag}`,
      };
    case "anytls":
      return { link: `anytls://${enc(pw)}@${addr}/${query()}${tag}` };
    case "socks5": {
      const user = n.username.trim();
      const auth = user || pw ? `${enc(user)}:${enc(pw)}@` : "";

      return { link: `socks5://${auth}${addr}${tag}` };
    }
  }
}

// Select keys must not be empty; "" options travel under this one.
const NONE_KEY = "__none";

function Pick({
  label,
  value,
  options,
  onChange,
  disabled,
}: {
  label: string;
  value: string;
  options: Array<[string, string]>;
  onChange: (v: string) => void;
  disabled?: boolean;
}) {
  return (
    <Select
      isDisabled={disabled}
      label={label}
      selectedKeys={[value || NONE_KEY]}
      onSelectionChange={(keys) => {
        const k = Array.from(keys)[0];

        if (k !== undefined) onChange(String(k) === NONE_KEY ? "" : String(k));
      }}
    >
      {options.map(([k, text]) => (
        <SelectItem key={k || NONE_KEY} textValue={text}>
          {text}
        </SelectItem>
      ))}
    </Select>
  );
}

export function ManualNodeFields({
  value: n,
  onChange,
  ssMethods,
}: {
  value: ManualNode;
  onChange: (n: ManualNode) => void;
  ssMethods: string[];
}) {
  const set = (patch: Partial<ManualNode>) => onChange({ ...n, ...patch });
  const methods = ssMethods.length > 0 ? ssMethods : FALLBACK_SS_METHODS;
  const tls = tlsOn(n);
  const field = (
    key: keyof ManualNode,
    label: string,
    extra?: { placeholder?: string; description?: string; type?: string },
  ) => (
    <Input
      description={extra?.description}
      label={label}
      placeholder={extra?.placeholder}
      type={extra?.type}
      value={String(n[key])}
      variant="bordered"
      onChange={(e) => set({ [key]: e.target.value })}
    />
  );

  return (
    <div className="space-y-3">
      <Pick
        label="协议"
        options={MANUAL_PROTOCOLS}
        value={n.protocol}
        onChange={(v) => {
          const protocol = v as ManualProtocol;
          // VMess has no Reality; the other TLS protocols always use TLS.
          const security: Security =
            alwaysTLS(protocol) ||
            (protocol === "vmess" && n.security === "reality")
              ? "tls"
              : n.security;

          set({
            protocol,
            security,
            // F-01：切到 VLESS 时若当前组合不允许 Vision，顺手清掉 flow。
            flow:
              protocol === "vless" &&
              (security === "none" || n.network !== "tcp")
                ? ""
                : n.flow,
          });
        }}
      />
      {(n.protocol === "hysteria2" ||
        n.protocol === "tuic" ||
        n.protocol === "anytls") && (
        <p className="text-xs text-warning-600 dark:text-warning-400">
          作链式中转目标时只能走 sing-box 内核，Xray 会拒绝该协议
        </p>
      )}
      {field("name", "名称", { placeholder: "留空用 地址:端口" })}
      <div className="grid grid-cols-3 gap-3">
        <div className="col-span-2">
          {field("host", "地址", { placeholder: "域名或 IP" })}
        </div>
        {field("port", "端口", { type: "number", placeholder: "443" })}
      </div>

      {n.protocol === "ss" && (
        <Pick
          label="加密方式"
          options={methods.map((m) => [m, m])}
          value={n.method}
          onChange={(method) => set({ method })}
        />
      )}
      {usesUUID(n.protocol) && (
        <Input
          endContent={
            <Button
              size="sm"
              variant="light"
              onPress={() => set({ uuid: randomUUID() })}
            >
              生成
            </Button>
          }
          label="UUID"
          value={n.uuid}
          variant="bordered"
          onChange={(e) => set({ uuid: e.target.value })}
        />
      )}
      {n.protocol === "socks5" &&
        field("username", "用户名（可选）", {
          description: "与密码成对填写，或都留空",
        })}
      {usesPassword(n.protocol) &&
        field("password", n.protocol === "socks5" ? "密码（可选）" : "密码", {
          description:
            n.protocol === "socks5"
              ? "与用户名成对填写，或都留空"
              : n.protocol === "ss" && n.method.startsWith("2022-")
                ? "SS2022 的密码是 Base64 密钥，长度要和加密方式匹配"
                : undefined,
        })}
      {n.protocol === "vmess" &&
        field("alterId", "alterId", { type: "number", placeholder: "0" })}
      {n.protocol === "vless" && (
        <div className="space-y-1.5">
          <Pick
            disabled={!visionAllowedManual(n)}
            label="流控 (flow)"
            options={[
              ["", "无"],
              ["xtls-rprx-vision", "xtls-rprx-vision"],
            ]}
            value={n.flow}
            onChange={(flow) => set({ flow })}
          />
          {!visionAllowedManual(n) && (
            <p className="text-xs text-warning-600 dark:text-warning-400">
              Vision 仅支持 TCP 且需要 TLS/Reality，当前组合不可用
            </p>
          )}
        </div>
      )}
      {n.protocol === "tuic" && (
        <Pick
          label="拥塞控制"
          options={[
            ["bbr", "bbr"],
            ["cubic", "cubic"],
            ["new_reno", "new_reno"],
          ]}
          value={n.congestion}
          onChange={(congestion) => set({ congestion })}
        />
      )}

      {(n.protocol === "vless" || n.protocol === "vmess") && (
        <div className="space-y-1.5">
          <p className="text-sm">安全</p>
          <Segmented
            options={
              n.protocol === "vless"
                ? [
                    ["none", "无"],
                    ["tls", "TLS"],
                    ["reality", "Reality"],
                  ]
                : [
                    ["none", "无"],
                    ["tls", "TLS"],
                  ]
            }
            value={n.security}
            onChange={(security) =>
              set({
                security: security as Security,
                // F-01：切到“无”时 Vision 即非法，顺手清掉 flow。
                flow: security === "none" ? "" : n.flow,
              })
            }
          />
        </div>
      )}
      {tls && (
        <div className="space-y-3 rounded-xl border border-divider p-3">
          {field("sni", "SNI", {
            placeholder:
              n.protocol === "vless" && n.security === "reality"
                ? "必填，如 www.microsoft.com"
                : "留空用地址",
            description:
              n.security === "reality" && n.protocol === "vless"
                ? "Reality 伪装的网站域名，比如 www.microsoft.com"
                : undefined,
          })}
          {n.protocol === "vless" && n.security === "reality" && (
            <>
              {field("publicKey", "公钥 (pbk)")}
              {field("shortId", "shortId (sid)", { placeholder: "可留空" })}
            </>
          )}
          {n.protocol !== "hysteria2" && n.protocol !== "tuic" && (
            <Pick
              label="指纹 (fp)"
              options={FINGERPRINTS.map((f) => [f, f || "默认"])}
              value={n.fingerprint}
              onChange={(fingerprint) => set({ fingerprint })}
            />
          )}
          {!(n.protocol === "vless" && n.security === "reality") && (
            <Checkbox
              isSelected={n.insecure}
              onValueChange={(insecure) => set({ insecure })}
            >
              <span className="text-sm">跳过证书验证（自签证书时打开）</span>
            </Checkbox>
          )}
        </div>
      )}

      {usesTransport(n.protocol) && (
        <div className="space-y-1.5">
          <p className="text-sm">传输</p>
          <Segmented
            options={[
              ["tcp", "TCP"],
              ["ws", "WebSocket"],
              ["grpc", "gRPC"],
            ]}
            value={n.network}
            onChange={(network) =>
              set({
                network: network as Network,
                // F-01：切离 TCP 时 Vision 即非法，顺手清掉 flow。
                flow: network !== "tcp" ? "" : n.flow,
              })
            }
          />
        </div>
      )}
      {usesTransport(n.protocol) && n.network === "ws" && (
        <div className="grid grid-cols-2 gap-3">
          {field("path", "路径", { placeholder: "/" })}
          {field("wsHost", "Host", { placeholder: "可留空" })}
        </div>
      )}
      {usesTransport(n.protocol) &&
        n.network === "grpc" &&
        field("serviceName", "serviceName")}

      {n.protocol === "hysteria2" && (
        <div className="grid grid-cols-2 gap-3">
          {field("obfsPassword", "混淆密码", { placeholder: "没有就留空" })}
          {field("ports", "端口跳跃", { placeholder: "如 20000-30000" })}
        </div>
      )}
    </div>
  );
}
