// gen-manual-links.mjs — 手动填写节点 golden 链接生成器（F-10/F-13）。
//
// 用仓库自带的 typescript 把 src/pages/inbound/manual-node.tsx 转译为
// CommonJS（UI import 用空对象 stub 顶替，只调纯函数），对全部 8 协议 +
// 关键变体运行真实的 manualNodeLink()，把产出的 exact 链接写成 golden
// JSON（go-backend/internal/kernel/testdata/manual-links.json），供 Go
// 回归测试读入——Go 测试不再手写链接字面量。
//
// 同时执行 JS 的 randomUUID()（crypto.randomUUID 路径）与 http fallback
//（把 crypto.randomUUID 置空、走 crypto.getRandomValues 手拼 v4），把
// 产出的真实 UUID 一并写进 golden，Go 测试改用这些 UUID，不再用 Go 的
// uuid.NewString()。
//
// 再生成命令（工作目录 vite-frontend）：
//   node scripts/gen-manual-links.mjs
// 前置条件：vite-frontend 已 pnpm install（需 node_modules/typescript，
// 仓库 package.json 钉死 typescript 5.6.3）。缺失时直接失败，不静默
// 用其他来源的 typescript。
//
// 确定性：输入固定、输出顺序固定（cases 数组顺序）；唯一非确定性是
// uuids 一节——randomUUID 本就是随机值，再生成会换新值，属预期行为，
// Go 测试只断言其形态（version/variant 位）与后端接受，不钉死具体值。
//
// 对前端改动敏感：
//  - 源文件改动会改变 golden 里的 sourceSha256，Go 测试会校验该哈希与
//    当前 manual-node.tsx 一致——改了前端不重跑本脚本，测试即红。
//  - 截断标记（// Select keys must not be empty）消失、或新增运行时
//    import、或 manualNodeLink 产物含 error，生成器直接失败退出，
//    不把前端改动消化掉。
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const repoDir = join(frontendDir, "..");

const srcPath = join(frontendDir, "src/pages/inbound/manual-node.tsx");
const goldenPath = join(
  repoDir,
  "go-backend/internal/kernel/testdata/manual-links.json",
);

// 仓库自带的 typescript（pnpm install 后在 vite-frontend/node_modules）。
let ts;
try {
  ts = require(join(frontendDir, "node_modules/typescript"));
} catch {
  console.error(
    "FAIL  找不到 vite-frontend/node_modules/typescript，" +
      "请先在 vite-frontend 下跑 pnpm install（仓库钉死 typescript 5.6.3）。",
  );
  process.exit(1);
}

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const source = readFileSync(srcPath, "utf8");
const sourceSha256 = createHash("sha256").update(source).digest("hex");

// 纯函数都在文件上半部；JSX 组件（Pick / ManualNodeFields）在截断标记
// 之后，截掉。标记消失 = 前端结构变了，生成器必须失败而不是猜。
const CUT_MARKER = "// Select keys must not be empty";
const cutAt = source.indexOf(CUT_MARKER);
if (cutAt === -1) {
  console.error(
    `FAIL  截断标记 ${JSON.stringify(CUT_MARKER)} 在 manual-node.tsx 中找不到了，` +
      "前端结构已变，请更新本生成器（不要静默跳过）。",
  );
  process.exit(1);
}
let pure = source.slice(0, cutAt);
if (pure.includes("ManualNodeFields")) {
  console.error("FAIL  截断后仍含 ManualNodeFields，JSX 组件未切干净。");
  process.exit(1);
}

// UI import 只服务于被截掉的 JSX 组件：用空对象 stub 顶替。转译后若仍
// 有运行时 require，说明 manualNodeLink 侧新增了真实依赖，直接失败。
pure = pure.replace(
  /^import\s+\{[^}]*\}\s+from\s+["'][^"']+["'];?\s*$/gm,
  (m) => {
    const names = m
      .slice(m.indexOf("{") + 1, m.indexOf("}"))
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
    return `const { ${names.join(", ")} } = {};`;
  },
);
if (/^import\s/m.test(pure)) {
  console.error("FAIL  仍有未被 stub 的 import 语句，请更新本生成器。");
  process.exit(1);
}

const { outputText, diagnostics } = ts.transpileModule(pure, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2022,
  },
  reportDiagnostics: true,
});
const errors = (diagnostics ?? []).filter(
  (d) => d.category === ts.DiagnosticCategory.Error,
);
check("转译无错误", errors.length === 0,
  errors.map((d) => ts.flattenDiagnosticMessageText(d.messageText, " ")).join("; "));
const runtimeRequires = [...outputText.matchAll(/require\(["']([^"']+)["']\)/g)].map(
  (m) => m[1],
);
check("转译产物无运行时 require", runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`);

// randomUUID 未导出：只在转译产物副本上追加一行测试钩子导出它，
// 不动 manual-node.tsx 源码。
const hooked = `${outputText}\n;module.exports.__gen_randomUUID = randomUUID;\n`;
const mod = { exports: {} };
new Function("module", "exports", "require", hooked)(mod, mod.exports, require);
const { manualNodeLink, emptyManualNode, __gen_randomUUID: randomUUID } =
  mod.exports;
check("导出 manualNodeLink", typeof manualNodeLink === "function");
check("导出 emptyManualNode", typeof emptyManualNode === "function");
check("导出 randomUUID（测试钩子）", typeof randomUUID === "function");
if (failures.length > 0) process.exit(1);

// 固定输入：与原手写 manualNodeLinkCases 覆盖一致（8 协议 + 关键变体）。
// SS2022 密钥：16 个零字节的 base64（与旧用例同值）。
const MANUAL_UUID = "11111111-2222-4333-8444-555555555555";
const REALITY_PBK = "A".repeat(43); // 32 字节 RawURL base64
const SS_KEY = Buffer.from(new Uint8Array(16)).toString("base64");
const HOST = "203.0.113.10";

const base = (patch) => ({ ...emptyManualNode(), host: HOST, ...patch });

const cases = [
  {
    name: "ss",
    protocol: "ss",
    input: base({
      protocol: "ss",
      name: "manual-ss",
      port: "8388",
      method: "2022-blake3-aes-128-gcm",
      password: SS_KEY,
    }),
  },
  {
    name: "vmess",
    protocol: "vmess",
    input: base({
      protocol: "vmess",
      name: "manual-vmess",
      port: "443",
      uuid: MANUAL_UUID,
      alterId: "0",
      security: "none",
      network: "tcp",
    }),
  },
  {
    name: "vmess-ws-tls",
    protocol: "vmess",
    input: base({
      protocol: "vmess",
      name: "manual-vmess-ws",
      port: "443",
      uuid: MANUAL_UUID,
      alterId: "0",
      security: "tls",
      sni: "example.com",
      fingerprint: "chrome",
      network: "ws",
      path: "/ws",
      wsHost: "example.com",
    }),
  },
  {
    name: "vless-tls",
    protocol: "vless",
    input: base({
      protocol: "vless",
      name: "manual-vless",
      port: "443",
      uuid: MANUAL_UUID,
      security: "tls",
      sni: "example.com",
      fingerprint: "chrome",
      network: "tcp",
    }),
  },
  {
    name: "vless-reality",
    protocol: "vless",
    input: base({
      protocol: "vless",
      name: "manual-vless-reality",
      port: "443",
      uuid: MANUAL_UUID,
      security: "reality",
      sni: "www.microsoft.com",
      fingerprint: "chrome",
      publicKey: REALITY_PBK,
      shortId: "abcd1234",
      network: "tcp",
    }),
  },
  {
    name: "vless-ws",
    protocol: "vless",
    input: base({
      protocol: "vless",
      name: "manual-vless-ws",
      port: "443",
      uuid: MANUAL_UUID,
      security: "tls",
      sni: "example.com",
      fingerprint: "chrome",
      network: "ws",
      path: "/ws",
      wsHost: "example.com",
    }),
  },
  {
    name: "trojan",
    protocol: "trojan",
    input: base({
      protocol: "trojan",
      name: "manual-trojan",
      port: "443",
      password: "secretpass",
      sni: "example.com",
      fingerprint: "chrome",
      network: "tcp",
    }),
  },
  {
    name: "hysteria2",
    protocol: "hysteria2",
    input: base({
      protocol: "hysteria2",
      name: "manual-hy2",
      port: "443",
      password: "secretpass",
      obfsPassword: "obfspass",
      ports: "20000-30000",
    }),
  },
  {
    name: "tuic",
    protocol: "tuic",
    input: base({
      protocol: "tuic",
      name: "manual-tuic",
      port: "443",
      uuid: MANUAL_UUID,
      password: "secretpass",
      congestion: "bbr",
    }),
  },
  {
    name: "anytls",
    protocol: "anytls",
    input: base({
      protocol: "anytls",
      name: "manual-anytls",
      port: "443",
      password: "secretpass",
      sni: "example.com",
      fingerprint: "chrome",
    }),
  },
  {
    name: "socks5-auth",
    protocol: "socks5",
    input: base({
      protocol: "socks5",
      name: "manual-socks5",
      port: "1080",
      username: "user1",
      password: "pass1",
    }),
  },
  {
    name: "socks5-noauth",
    protocol: "socks5",
    input: base({
      protocol: "socks5",
      name: "manual-socks5-noauth",
      port: "1080",
    }),
  },
];

const outCases = [];
for (const c of cases) {
  const { link, error } = manualNodeLink(c.input);
  check(`manualNodeLink(${c.name}) 无 error`, !error, error ?? "");
  check(`manualNodeLink(${c.name}) 产出链接`, typeof link === "string" && link.length > 0);
  if (!link) continue;
  console.log(`  ${c.name}: ${link}`);
  outCases.push({ name: c.name, protocol: c.protocol, input: c.input, link });
}

// T-01：空标签主机名（连续点号）必须被拒收、不出链，文案与后端同口径；
// 对照 a.b 仍出链（防过修）。
const t01bad = manualNodeLink(base({
  protocol: "vless",
  name: "manual-t01",
  port: "443",
  uuid: MANUAL_UUID,
  security: "tls",
  sni: "example.com",
  fingerprint: "chrome",
  network: "tcp",
  host: "a..b",
}));
check(
  "manualNodeLink(host=a..b) 拒收（T-01）",
  !!t01bad.error && !t01bad.link && t01bad.error.includes('地址 "a..b" 不正确'),
  t01bad.error ?? "未拒收",
);
const t01ok = manualNodeLink(base({
  protocol: "vless",
  name: "manual-t01-ok",
  port: "443",
  uuid: MANUAL_UUID,
  security: "tls",
  sni: "example.com",
  fingerprint: "chrome",
  network: "tcp",
  host: "a.b",
}));
check(
  "manualNodeLink(host=a.b) 出链（T-01 对照）",
  !t01ok.error && typeof t01ok.link === "string" && t01ok.link.length > 0,
  t01ok.error ?? "",
);
if (failures.length > 0) process.exit(1);

// F-10 显式验证：anytls 分支从不写 security 参数，golden 里的 anytls
// 链接不得含 security 参数（旧手写用例含 ?security=tls，是失真）。
const anytls = outCases.find((c) => c.name === "anytls");
check(
  "anytls golden 链接不含 security 参数（F-10）",
  anytls && !new URL(anytls.link).searchParams.has("security"),
  anytls?.link ?? "缺 anytls 用例",
);

// UUID：两条真实 JS 路径各取 5 个。fallback 靠临时置空
// crypto.randomUUID 触发（Node 里该属性可写，用完立即恢复）。
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const genUUIDs = (n) => {
  const out = [];
  for (let i = 0; i < n; i++) {
    const id = randomUUID();
    check(`randomUUID 产出 v4 形态 ${id}`, UUID_V4.test(id));
    out.push(id);
  }
  return out;
};
const randomUUIDs = genUUIDs(5);
const origRandomUUID = globalThis.crypto.randomUUID;
let fallbackUUIDs;
try {
  globalThis.crypto.randomUUID = undefined;
  check(
    "fallback 已触发（crypto.randomUUID 置空后不可用）",
    typeof globalThis.crypto.randomUUID !== "function",
  );
  fallbackUUIDs = genUUIDs(5);
} finally {
  globalThis.crypto.randomUUID = origRandomUUID;
}
const allUUIDs = [...randomUUIDs, ...fallbackUUIDs];
check("10 个 UUID 互不相同", new Set(allUUIDs).size === 10);
if (failures.length > 0) process.exit(1);

const golden = {
  generatedBy: "vite-frontend/scripts/gen-manual-links.mjs",
  source: "vite-frontend/src/pages/inbound/manual-node.tsx",
  sourceSha256,
  cases: outCases,
  uuids: { randomUUID: randomUUIDs, fallback: fallbackUUIDs },
};

mkdirSync(dirname(goldenPath), { recursive: true });
writeFileSync(goldenPath, `${JSON.stringify(golden, null, 2)}\n`);
console.log(`\nWROTE ${goldenPath} (${outCases.length} cases, 10 uuids)`);
console.log(`sourceSha256: ${sourceSha256}`);
