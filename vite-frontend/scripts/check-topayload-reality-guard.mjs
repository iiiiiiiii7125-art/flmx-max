// check-topayload-reality-guard.mjs — toPayload Reality 字段设防回归校验（无测试框架版）。
//
// R34-L2-01 / FIX-PLAN F-02：toPayload（src/pages/inbound/presets.ts 安全段）
// 对 realityDest / realityPrivateKey / realityShortIds 无 securityType 设防，
// 手动把安全层 reality → none 后残留值仍发出（仅 realityMinClientVer 设防）。
// 本脚本把 presets.ts 用 typescript.transpileModule 转译后在 Node 里执行
// 并断言（与 check-pure-enc-preset.mjs 同型做法；presets.ts 只有 import type，
// 转译产物无运行时 import，可直接跑）。
//
// 断言：
//   ① reality 路径：安全段仍含四项 Reality 值（不得改动 reality 路径行为）；
//   ② 手动切 reality → none：安全段不含四项 Reality 值（修前含值 = 红，修后不含 = 绿）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-topayload-reality-guard.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");

let ts;
try {
  ts = require("typescript");
} catch {
  // worktree 尚未 pnpm install 时，退回主仓的 node_modules。
  ts = require(
    "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
  );
}

const srcPath = join(
  frontendDir,
  "src/pages/inbound/presets.ts",
);
const source = readFileSync(srcPath, "utf8");

const { outputText } = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2020 },
  reportDiagnostics: true,
});

// 纯函数不得有运行时依赖：转译产物里不应出现 require("...")。
const runtimeRequires = [...outputText.matchAll(/require\(["']([^"']+)["']\)/g)].map(
  (m) => m[1],
);
const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

check("转译产物无运行时 require", runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`);

const mod = { exports: {} };
new Function("module", "exports", "require", outputText)(
  mod,
  mod.exports,
  require,
);
const { toPayload } = mod.exports;
check("导出 toPayload", typeof toPayload === "function",
  `typeof = ${typeof toPayload}`);

if (typeof toPayload === "function") {
  // 一份带 Reality 值的完整表单（VLESS / TCP / Reality / Vision）。
  const baseForm = {
    id: 1,
    nodeId: "1",
    kernelType: "xray",
    name: "reality-test",
    protocol: "vless",
    listen: "0.0.0.0",
    port: "8443",
    uuid: "11111111-1111-1111-1111-111111111111",
    flowVision: true,
    encryptionEnabled: false,
    encryptionAuth: "x25519",
    encryptionMode: "native",
    encryptionSecret: "",
    password: "",
    method: "",
    snellVersion: "",
    snellMode: "",
    masquerade: "",
    backendPort: 0,
    portHopping: "",
    username: "",
    transportType: "tcp",
    path: "",
    host: "",
    serviceName: "",
    xhttpMode: "",
    securityType: "reality",
    sni: "www.example.com",
    alpn: "",
    fingerprint: "",
    certMode: "",
    certFile: "",
    keyFile: "",
    realityDest: "www.example.com:443",
    realityPrivateKey: "reality-priv-key",
    realityShortIds: "abc123,def456",
    realityMinClientVer: "1.0.0",
    shadowTlsPassword: "",
    acmeEmail: "",
    acmeDns: "",
    acmeDnsKey: "",
    acmeDnsSecret: "",
    acmeDnsSet: false,
    shareAddressMode: "",
    shareHost: "",
    remark: "",
    extra: "",
    relayOnly: false,
    acceptProxyProtocol: false,
    relayWhitelist: false,
    whitelistExtra: "",
    outboundId: "",
    outboundIpStrategy: "auto",
    splitRules: [],
    enabled: true,
    meteringEnabled: false,
    meteringActive: false,
    connIdleSeconds: "",
  };

  // ① reality 路径：四项 Reality 值必须仍在 payload 安全段。
  const realityPayload = toPayload(baseForm);
  check("reality 路径：security.type === reality",
    realityPayload.security.type === "reality",
    `实际 ${JSON.stringify(realityPayload.security.type)}`);
  check("reality 路径：realityDest 仍发出",
    realityPayload.security.realityDest === "www.example.com:443",
    `实际 ${JSON.stringify(realityPayload.security.realityDest)}`);
  check("reality 路径：realityPrivateKey 仍发出",
    realityPayload.security.realityPrivateKey === "reality-priv-key",
    `实际 ${JSON.stringify(realityPayload.security.realityPrivateKey)}`);
  check("reality 路径：realityShortIds 仍发出",
    JSON.stringify(realityPayload.security.realityShortIds) ===
      JSON.stringify(["abc123", "def456"]),
    `实际 ${JSON.stringify(realityPayload.security.realityShortIds)}`);
  check("reality 路径：realityMinClientVer 仍发出",
    realityPayload.security.realityMinClientVer === "1.0.0",
    `实际 ${JSON.stringify(realityPayload.security.realityMinClientVer)}`);

  // ② 手动切 reality → none（表单残留值不动）：安全段不得再含 Reality 值。
  const noneForm = { ...baseForm, securityType: "none" };
  const nonePayload = toPayload(noneForm);
  check("none 路径：security.type === none",
    nonePayload.security.type === "none",
    `实际 ${JSON.stringify(nonePayload.security.type)}`);
  for (const k of [
    "realityDest",
    "realityPrivateKey",
    "realityShortIds",
    "realityMinClientVer",
  ]) {
    check(`none 路径：${k} 不发出`,
      nonePayload.security[k] === undefined,
      `实际 ${JSON.stringify(nonePayload.security[k])}`);
  }
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
