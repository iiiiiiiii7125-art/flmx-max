// check-pure-enc-preset.mjs — IEPL 专用（纯 ENC）预设与标签回归校验
// （无测试框架版）。
//
// 把 src/pages/inbound/pure-enc-preset.ts 用 typescript.transpileModule
// 转译后在 Node 里执行并断言。pure-enc-preset.ts 只能 import type，
// 转译产物无运行时 import，可直接跑。另对 CHG-01（S-21）的标签面做
// 源码断言：三处文件「IEPL专用」在位且为红色（danger）、旧文案
// 「IX 中转专用协议」与独立「纯ENC」快捷标签全仓差集 ∅。
//
// 提取面（修复轮 6，同族机制处置）：标签面源码断言的提取对象先经
// 词法剥注释（字符串保留）——被 JSX 注释整块包住的 Chip 不再被
// extractChipBlocks 计入，注释里的旧文案提及也不再误报残留；转译
// 执行的核心断言（预设/载荷）不变。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-pure-enc-preset.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readFileSync, readdirSync } from "node:fs";
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
  // R4-C6-04：typescript 只许自当前工作树解析。未装依赖时不再静默
  // 回退到写死的其它检出路径——门结果会随那份检出的 TS 版本漂移
  // 且无点名。确需外部副本时以 FLMX_CHECK_TYPESCRIPT 显式指向，
  // 并在此点名（输出 NOTE 行）。
  const envTs = process.env.FLMX_CHECK_TYPESCRIPT;

  if (envTs) {
    ts = require(envTs);
    console.log(
      `NOTE  typescript 由 FLMX_CHECK_TYPESCRIPT 显式指向工作树外副本：${envTs}（v${ts.version}）`,
    );
  } else {
    console.log(
      "FAIL  typescript 无法自当前工作树解析（vite-frontend/node_modules 缺失）——先 pnpm install，或以 FLMX_CHECK_TYPESCRIPT=<typescript 路径> 显式指向外部副本",
    );
    process.exit(1);
  }
}

const srcPath = join(
  frontendDir,
  "src/pages/inbound/pure-enc-preset.ts",
);
const source = readFileSync(srcPath, "utf8");

// 词法剥注释：// 与 /* */ 逐字替换为空格（换行保留），字符串/模板
// 字面量内容原样保留（模板 ${} 内的代码照常扫描；JSX 注释即块注
// 释、同样剥除）。标签面断言的读取一律经此处理。
const stripComments = (src) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let mode = "code";
  const tplDepths = [];
  let braceDepth = 0;
  while (i < n) {
    const c = src[i];
    const d = src[i + 1] ?? "";
    if (mode === "code") {
      if (c === "/" && d === "/") {
        out.push("  ");
        i += 2;
        mode = "line";
        continue;
      }
      if (c === "/" && d === "*") {
        out.push("  ");
        i += 2;
        mode = "block";
        continue;
      }
      if (c === "'") mode = "sq";
      else if (c === '"') mode = "dq";
      else if (c === "`") mode = "tpl";
      else if (c === "{") braceDepth++;
      else if (c === "}") {
        if (tplDepths.length && braceDepth === tplDepths[tplDepths.length - 1]) {
          tplDepths.pop();
          braceDepth--;
          mode = "tpl";
          out.push(c);
          i++;
          continue;
        }
        braceDepth--;
      }
      out.push(c);
      i++;
      continue;
    }
    if (mode === "line") {
      if (c === "\n") {
        out.push(c);
        i++;
        mode = "code";
        continue;
      }
      out.push(" ");
      i++;
      continue;
    }
    if (mode === "block") {
      if (c === "*" && d === "/") {
        out.push("  ");
        i += 2;
        mode = "code";
        continue;
      }
      out.push(c === "\n" ? "\n" : " ");
      i++;
      continue;
    }
    if (mode === "sq" || mode === "dq") {
      const q = mode === "sq" ? "'" : '"';
      out.push(c);
      if (c === "\\") {
        out.push(d);
        i += 2;
        continue;
      }
      if (c === q) mode = "code";
      i++;
      continue;
    }
    out.push(c);
    if (c === "\\") {
      out.push(d);
      i += 2;
      continue;
    }
    if (c === "`") {
      mode = "code";
      i++;
      continue;
    }
    if (c === "$" && d === "{") {
      out.push(d);
      i += 2;
      braceDepth++;
      tplDepths.push(braceDepth);
      mode = "code";
      continue;
    }
    i++;
  }
  return out.join("");
};

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
    connIdleSeconds: "300",
  };
  const snapshot = JSON.parse(JSON.stringify(before));
  const after = applyPureEncPreset(before);

  const expected = {
    protocol: "vless",
    transportType: "tcp",
    securityType: "none",
    // S-21 已定口径（单一判据）：纯 ENC 形态不启用 Vision 流控。
    flowVision: false,
    encryptionEnabled: true,
    encryptionAuth: "mlkem768",
    encryptionMode: "xorpub",
    sni: "",
    // CHG-01（S-21）联动：套用时勾选空闲超时「不限」（-1 哨兵值）。
    connIdleSeconds: "-1",
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

// ⑤ CHG-01（S-21）标签面源码断言：三文件「IEPL专用」在位且红色、
//    旧文案与独立快捷标签差集 ∅。
//
// 截块必须结构化（A-F-01）：旧实现以「<Chip 起 900 字符内出现标签
// 文案」的定长窗口计数，基线块长 896（余量 4），D-03 给 onClick 增
// 一行 setPresetNote 后块长 951、窗口溢出误判标签缺失——门禁脆弱而
// 非功能缺陷。此处按 JSX 结构截块：开标签的结束「>」须跳过引号与
// 表达式大括号（onClick={() => ...} 内的「>」不是标签结束），块以
// 真实 </Chip> 收尾，块长不再设上限。
const extractChipBlocks = (text) => {
  const blocks = [];
  const openRe = /<Chip\b/g;
  let m;

  while ((m = openRe.exec(text)) !== null) {
    const start = m.index;
    let i = start + "<Chip".length;
    let depth = 0;
    let quote = null;
    let openEnd = -1;

    for (; i < text.length; i++) {
      const c = text[i];

      if (quote) {
        if (c === quote && text[i - 1] !== "\\") quote = null;
      } else if (c === '"' || c === "'" || c === "`") {
        quote = c;
      } else if (c === "{") {
        depth++;
      } else if (c === "}") {
        depth--;
      } else if (c === ">" && depth === 0) {
        openEnd = i;
        break;
      }
    }
    if (openEnd < 0) break;
    const openTag = text.slice(start, openEnd + 1);

    if (openTag.endsWith("/>")) {
      blocks.push({ openTag, inner: "" });
      continue;
    }
    const closeStart = text.indexOf("</Chip>", openEnd);

    if (closeStart < 0) break;
    blocks.push({ openTag, inner: text.slice(openEnd + 1, closeStart) });
    openRe.lastIndex = closeStart + "</Chip>".length;
  }

  return blocks;
};

// 截取包含 pos 的最近一层 {...} 对象字面量（大括号配平；该区域的
// 字符串与注释内不出现裸大括号）。
const enclosingObject = (text, pos) => {
  let depth = 0;
  let start = -1;

  for (let i = pos; i >= 0; i--) {
    if (text[i] === "}") depth++;
    else if (text[i] === "{") {
      if (depth === 0) {
        start = i;
        break;
      }
      depth--;
    }
  }
  if (start < 0) return null;
  depth = 0;
  for (let i = start; i < text.length; i++) {
    if (text[i] === "{") depth++;
    else if (text[i] === "}") {
      depth--;
      if (depth === 0) return text.slice(start, i + 1);
    }
  }

  return null;
};

const labelFiles = [
  { rel: "src/pages/inbound/inbound-form-modal.tsx", count: 1 },
  { rel: "src/pages/inbound.tsx", count: 3 },
  { rel: "src/pages/relay.tsx", count: 2 },
];
for (const { rel, count } of labelFiles) {
  const text = stripComments(readFileSync(join(frontendDir, rel), "utf8"));
  const hits = extractChipBlocks(text).filter((b) =>
    b.inner.includes("IEPL专用"),
  );

  check(
    `${rel}：「IEPL专用」标签 ${count} 处`,
    hits.length === count,
    `实际 ${hits.length} 处`,
  );
  check(
    `${rel}：「IEPL专用」标签均为红色（danger）`,
    hits.every((b) => b.openTag.includes('color="danger"')),
    "存在非 danger 的 IEPL专用 标签",
  );
}
const formText = stripComments(
  readFileSync(
    join(frontendDir, "src/pages/inbound/inbound-form-modal.tsx"),
    "utf8",
  ),
);
check(
  "表单已无独立「纯ENC」快捷标签",
  !/>\s*纯ENC\s*</.test(formText),
  "仍存在文本为纯ENC 的 Chip",
);
// S-21 已定口径（单一判据）：ENC 开关自动填充产生的同样是纯 ENC
// 形态（ENC 开 + 安全无），不得写入 Vision——native 外观的那组自动
// 填充里 flowVision 必须为 false、且不得出现 flowVision: true。
// 判定对象是包含该字段的整个对象字面量（结构化截取，同 A-F-01：
// 旧实现以 200 字符定长窗口衔接两字段，实距 166、余量 34，同族脆弱）。
const nativePos = formText.indexOf('encryptionMode: "native",');
const nativeBlock = nativePos >= 0 ? enclosingObject(formText, nativePos) : null;

check(
  "ENC 开关自动填充不写入 Vision 流控",
  nativeBlock !== null &&
    nativeBlock.includes("flowVision: false") &&
    !nativeBlock.includes("flowVision: true"),
  "开关自动填充块（native 组）未以 flowVision: false 落定",
);
// ⑥ payload 级断言（presets.ts 的 visionAllowed/toPayload，import 均为
//    type-only，转译后可直接执行）：
//    - 纯 ENC 预设产物下发时 flow 必须缺省（无 Vision）；
//    - Reality 形态下 Vision 仍可选且会被下发（功能未被 ENC 改版波及）。
const presetsPath = join(frontendDir, "src/pages/inbound/presets.ts");
const presetsOut = ts.transpileModule(
  readFileSync(presetsPath, "utf8"),
  {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2020,
    },
    reportDiagnostics: true,
  },
).outputText;
const presetsRequires = [
  ...presetsOut.matchAll(/require\(["']([^"']+)["']\)/g),
].map((m) => m[1]);
check("presets.ts 转译产物无运行时 require", presetsRequires.length === 0,
  `发现 require: ${presetsRequires.join(", ")}`);
if (presetsRequires.length === 0) {
  const pmod = { exports: {} };

  new Function("module", "exports", "require", presetsOut)(
    pmod,
    pmod.exports,
    require,
  );
  const { emptyForm, toPayload, visionAllowed } = pmod.exports;

  if (typeof applyPureEncPreset === "function") {
    const encForm = applyPureEncPreset(emptyForm(null, "xray", "3"));
    const encPayload = toPayload(encForm);

    check(
      "纯 ENC 预设产物 payload 无 Vision flow",
      encPayload.settings.flow === undefined,
      `实际 ${JSON.stringify(encPayload.settings.flow)}`,
    );
    check(
      "纯 ENC 预设产物 payload 携带 ENC 认证与空闲超时不限",
      encPayload.settings.encryptionAuth === "mlkem768" &&
        encPayload.connIdleSeconds === -1,
      `encryptionAuth=${encPayload.settings.encryptionAuth} connIdleSeconds=${encPayload.connIdleSeconds}`,
    );
  }
  const realityForm = {
    ...emptyForm(null, "xray", "3"),
    protocol: "vless",
    transportType: "tcp",
    securityType: "reality",
    flowVision: true,
    encryptionEnabled: false,
  };

  check(
    "Reality 形态 visionAllowed 仍为 true",
    visionAllowed(realityForm) === true,
    `实际 ${visionAllowed(realityForm)}`,
  );
  check(
    "Reality 形态 payload 仍下发 xtls-rprx-vision",
    toPayload(realityForm).settings.flow === "xtls-rprx-vision",
    `实际 ${JSON.stringify(toPayload(realityForm).settings.flow)}`,
  );
}

const srcFiles = readdirSync(join(frontendDir, "src"), {
  recursive: true,
}).filter((p) => /\.(ts|tsx)$/.test(p));
const oldCopyFiles = srcFiles.filter((p) =>
  stripComments(readFileSync(join(frontendDir, "src", p), "utf8")).includes(
    "IX 中转专用协议",
  ),
);
check(
  "src 全仓无旧文案「IX 中转专用协议」",
  oldCopyFiles.length === 0,
  `残留于: ${oldCopyFiles.join(", ")}`,
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
