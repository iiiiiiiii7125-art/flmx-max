// check-pure-enc-preset.mjs — 纯 ENC 预设回归校验（无测试框架版）。
//
// 把 src/pages/inbound/pure-enc-preset.ts 用 typescript.transpileModule
// 转译后在 Node 里执行并断言。pure-enc-preset.ts 只能 import type，
// 转译产物无运行时 import，可直接跑。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-pure-enc-preset.mjs
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
  "src/pages/inbound/pure-enc-preset.ts",
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
const { applyPureEncPreset } = mod.exports;
check("导出 applyPureEncPreset", typeof applyPureEncPreset === "function",
  `typeof = ${typeof applyPureEncPreset}`);

if (typeof applyPureEncPreset === "function") {
  const eq = (a, b) => JSON.stringify(a) === JSON.stringify(b);

  // ① 从非 VLESS + Reality + 带 SNI 的表单出发，点标签后 8 字段 exactly。
  const before = {
    nodeId: "3",
    name: "my-inbound",
    port: "8443",
    protocol: "vmess",
    transportType: "ws",
    securityType: "reality",
    flowVision: false,
    encryptionEnabled: false,
    encryptionAuth: "x25519",
    encryptionMode: "native",
    sni: "www.example.com",
    realityDest: "www.example.com:443",
    realityPrivateKey: "reality-priv",
    realityShortIds: "abc123",
    realityMinClientVer: "1.0.0",
  };
  const snapshot = JSON.parse(JSON.stringify(before));
  const after = applyPureEncPreset(before);

  const expected = {
    protocol: "vless",
    transportType: "tcp",
    securityType: "none",
    flowVision: true,
    encryptionEnabled: true,
    encryptionAuth: "mlkem768",
    encryptionMode: "xorpub",
    sni: "",
  };
  for (const [k, v] of Object.entries(expected)) {
    check(
      `字段 ${k} === ${JSON.stringify(v)}`,
      after[k] === v,
      `实际 ${JSON.stringify(after[k])}`,
    );
  }
  // Reality 残留字段不得带入（toPayload 在 securityType=none 时仍会原样发出）。
  for (const k of [
    "realityDest",
    "realityPrivateKey",
    "realityShortIds",
    "realityMinClientVer",
  ]) {
    check(`残留字段 ${k} 已清空`, after[k] === "",
      `实际 ${JSON.stringify(after[k])}`);
  }

  // ② nodeId / port / name 原值保留。
  for (const k of ["nodeId", "port", "name"]) {
    check(`保留字段 ${k} === ${JSON.stringify(snapshot[k])}`,
      after[k] === snapshot[k], `实际 ${JSON.stringify(after[k])}`);
  }

  // ③ 不 mutate 输入。
  check("不修改原表单对象", eq(before, snapshot),
    `调用后输入被改动: ${JSON.stringify(before)}`);

  // ④ 幂等：已是纯 ENC 再点一次，结果不变。
  const twice = applyPureEncPreset(after);
  check("幂等：二次点击结果不变", eq(twice, after),
    `一次 ${JSON.stringify(after)} / 二次 ${JSON.stringify(twice)}`);
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
