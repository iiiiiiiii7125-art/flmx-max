#!/usr/bin/env node
// check-theme-light-required.mjs — 主题包 light 集必填校验（无测试框架版）。
//
// 背景（R2-027 范围③，用户 2026-11-03 拍板）：深色模式已全站删除、
// 全站统一亮色。原先主题包 tokens 允许只有 dark 集，注册表在
// activateTheme / reapplyActiveTheme、主题设置页预览三处以
// `tokens?.light ?? tokens?.dark` 回退取色。拍板定案：light 集必填——
// 回退表达式清除、类型上 dark 成员移除、内置主题包 dark 数据集删除，
// 并在唯一摄入点 registerTheme 加格式校验：提供 tokens 却缺 light 集
// 的主题包一律拒绝注册（控制台报错点名），不得静默回退、不得注入
// 空 token 后装作激活成功。
//
// 本脚本把 src/themes/registry.ts 用 typescript.transpileModule 转译后
// 在 Node 里执行（与 check-quality-history-bar.mjs 同型做法；该模块
// 只有 import type，转译产物无运行时 import），对真实 registerTheme
// 行为断言，并对相关源码做文本钉。
//
// 断言覆盖：
//   ① 合法包（tokens.light 在位）可注册、可取回；
//   ② 变异形态：只有 dark 集的主题包 → 被拒（不在注册表内）、
//      console.error 点名该主题 id 且点名缺 light 集；
//   ③ tokens 在位但 light 缺失（tokens: {}）→ 同样被拒并点名；
//   ④ 被拒包无法激活：activateTheme 后 active 仍为 null（不装作成功）；
//   ⑤ 同 id 先注册合法包、再喂非法包 → 合法包不被顶掉；
//   ⑥ 无 tokens 的包（只覆盖组件/布局/页面）仍可注册——tokens 本身
//      在类型契约上可选，必填的是「提供 tokens 时」的 light 集；
//   ⑦ validateThemePackage 导出存在且对上述形态判定一致；
//   ⑧ 文本钉：registry.ts 与 theme-settings.tsx 无 tokens?.dark 回退；
//      types.ts 的 tokens 类型无 dark 成员且 light 为必填成员；
//      两内置主题包无 dark token 数据集；themes/README.md 无
//      dark token 集示例。
//   ⑨ 默认主题包 light 集与 globals.css :root 的共享 token 集相异
//      集＝∅（R4-02 / R3-RES-01）：该包头注声称 token 与 globals.css
//      一致、激活后与未改装一致；R2-049 换色按「页面渲染面」推导
//      全集时整套蓝色板在此漏网，修复轮 4 按裁定 A 逐值对齐后在
//      此钉死——包 light 集声明的每个 token 必须在 globals.css 的
//      :root 中存在且逐值相同，任一漂移（改包不改 globals、或反之
//      只改 globals 一面）即红。
//   ⑩ 「在场但为空」形态（R6-10 / F-C5-01）：light 键在场但集为空
//      （light: {}），或 light 全值为 undefined（injectTokens 跳过
//      undefined、实际注入 0）——两种形态的有效值计数均为 0，必须
//      与「缺失」形态同拒并点名；被拒包同样不得激活。
//   ⑪ 同 id 顶替保护对空形态同样成立：先注册合法包，再以同 id 喂
//      空集包 / 全 undefined 包 → 合法包不被顶掉。附带正对照：
//      light 集部分键为 undefined、其余为实值的包属合法部分集
//      （未定义键由 injectTokens 跳过、其余照常注入，未给出的
//      token 走回退），不得被新校验误伤。
//   ⑫ 「有效值＝非空字符串」收口（R7-12 / G-C5-01＋G-C5-02 登记
//      组）：谓词由「不为 undefined」收严为「字符串且 trim 后非
//      空」——空串（setProperty(name,"") 等价移除属性）与纯空白
//      （剥空后 var() 替换计算值无效）此前计数为 1、可注册可激
//      活、可同 id 顶掉合法包而实际注入 0；类型契约之外的运行时
//      形态（值 null／数字／布尔／对象、light 为数组——注入键
//      "0" 被浏览器忽略）同结局。此次一并钉死：全部同拒并点名、
//      被拒包不得激活、同 id 不顶替；空串与实值混合的部分集计数
//      ≥1 仍属合法，不得误伤。
//
// 提取面（修复轮 6，R6-C4-10 机制处置）：断言⑨的 globals 侧旧实现
// 以正则合并 :root 块、无注释剥离——在 :root 块内给改过的 token
// 同行加注释存档旧值声明，注释声明后胜出、相异集报 ∅。现 globals
// 侧先词法剥除 CSS 注释再合并；断言⑧的源码文本钉同样先剥 TS 注
// 释（注释里的回退形态提及不算数、也不能靠注释拷贝满足/干扰判
// 定），行为断言 ①–⑦ 的转译执行面不变。
//
// 级联条件口径（修复轮 7，R7-10 / G-C4-04 机制处置）：⑨的 globals
// 侧合并与 check-contrast 同机制同处置——改按级联求值：仅无条件
// :root（顶层或仅被 @layer 包裹）的声明入集，@media/@supports
// 等条件块整棵子树排除，胜出顺序按 !important/层级/源码序的级
// 联规则；旧正则合并另有首个 } 截断块体的缺陷（无括号配平），
// 本轮一并随求值器退役。本文件仅动⑨的 globals 提取机制，谓词
// 断言不在本轮改动面。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-theme-light-required.mjs
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

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

// ─── 文本钉（源码形态，剥注释后判定） ────────────────────────────────────

const readSrc = (rel) => readFileSync(join(frontendDir, rel), "utf8");

// 词法剥注释（TS/TSX）：// 与 /* */ 逐字替换为空格（换行保留），
// 字符串/模板字面量内容原样保留。
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

// 词法剥除 CSS 注释（/* */，字符串内容保留）。
const stripCssComments = (src) => {
  const out = [];
  const n = src.length;
  let i = 0;
  let quote = null;
  while (i < n) {
    const c = src[i];
    if (quote) {
      out.push(c);
      if (c === "\\") {
        out.push(src[i + 1] ?? "");
        i += 2;
        continue;
      }
      if (c === quote) quote = null;
      i++;
      continue;
    }
    if (c === '"' || c === "'") {
      quote = c;
      out.push(c);
      i++;
      continue;
    }
    if (c === "/" && src[i + 1] === "*") {
      out.push("  ");
      i += 2;
      while (i < n && !(src[i] === "*" && src[i + 1] === "/")) {
        out.push(src[i] === "\n" ? "\n" : " ");
        i++;
      }
      if (i < n) {
        out.push("  ");
        i += 2;
      }
      continue;
    }
    out.push(c);
    i++;
  }
  return out.join("");
};

const registrySrc = readSrc("src/themes/registry.ts");
const settingsSrc = readSrc("src/components/theme-settings.tsx");
const typesSrc = readSrc("src/themes/types.ts");
const defaultSrc = readSrc("src/themes/default/index.ts");
const cyberpunkSrc = readSrc("src/themes/example-cyberpunk/index.ts");
const readmeSrc = readSrc("src/themes/README.md");

check(
  "registry.ts 无 tokens?.dark 回退",
  !/tokens\?\.dark/.test(stripComments(registrySrc)),
);
check(
  "theme-settings.tsx 无 tokens?.dark 回退",
  !/tokens\?\.dark/.test(stripComments(settingsSrc)),
);
check(
  "types.ts tokens 类型无 dark 成员",
  !/dark\??:\s*ThemeTokens/.test(stripComments(typesSrc)),
);
check(
  "types.ts tokens.light 为必填成员",
  /tokens\?:\s*\{\s*light:\s*ThemeTokens/.test(stripComments(typesSrc)),
);
check(
  "默认主题包无 dark token 数据集",
  !/^\s*dark:\s*\{/m.test(stripComments(defaultSrc)),
);
check(
  "赛博朋克主题包无 dark token 数据集",
  !/^\s*dark:\s*\{/m.test(stripComments(cyberpunkSrc)),
);
check("themes/README.md 无 dark token 集示例", !/dark:\s*\{/.test(readmeSrc));

// ─── 行为断言（转译后执行真实 registry） ──────────────────────────────────

const { outputText } = ts.transpileModule(registrySrc, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
  reportDiagnostics: true,
});

const runtimeRequires = [
  ...outputText.matchAll(/require\(["']([^"']+)["']\)/g),
].map((m) => m[1]);

check(
  "registry 转译产物无运行时 require",
  runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`,
);

// registry 的部分函数触碰 localStorage / document；注册路径本身不触碰，
// 但仍预置最小桩，避免断言调用链上的任何环境访问炸掉执行。
globalThis.localStorage = {
  getItem: () => null,
  setItem: () => {},
  removeItem: () => {},
};
globalThis.document = {
  documentElement: {
    classList: { add: () => {}, remove: () => {} },
    style: { setProperty: () => {}, removeProperty: () => {} },
  },
  head: { appendChild: () => {} },
  createElement: () => ({ setAttribute: () => {}, remove: () => {} }),
};

const mod = { exports: {} };

new Function("module", "exports", "require", outputText)(
  mod,
  mod.exports,
  require,
);
const {
  registerTheme,
  getTheme,
  getRegisteredThemes,
  getActiveThemeId,
  activateTheme,
  validateThemePackage,
} = mod.exports;

check(
  "registry 导出 validateThemePackage",
  typeof validateThemePackage === "function",
  `typeof = ${typeof validateThemePackage}`,
);

// 捕获 console.error（拒绝时的点名提示）。
const errorLog = [];
const origError = console.error;

console.error = (...args) => errorLog.push(args.map(String).join(" "));

const base = { name: "T", author: "check", version: "1.0.0" };
const lightPkg = {
  ...base,
  id: "ok-light",
  tokens: { light: { "--primary": "#123456" } },
};
const darkOnlyPkg = {
  ...base,
  id: "bad-dark-only",
  tokens: { dark: { "--primary": "#654321" } },
};
const emptyTokensPkg = { ...base, id: "bad-empty-tokens", tokens: {} };
const noTokensPkg = { ...base, id: "ok-no-tokens" };
// R6-10：在场但为空的两形态（有效值计数 0），与部分 undefined 的合法形态。
const emptyLightPkg = { ...base, id: "bad-empty-light", tokens: { light: {} } };
const undefLightPkg = {
  ...base,
  id: "bad-undef-light",
  tokens: { light: { "--primary": undefined } },
};
const partialUndefPkg = {
  ...base,
  id: "ok-partial-undef",
  tokens: { light: { "--primary": undefined, "--secondary": "#abcdef" } },
};

if (typeof validateThemePackage === "function") {
  check(
    "validateThemePackage：light 在位 → 通过",
    validateThemePackage(lightPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(lightPkg))}`,
  );
  check(
    "validateThemePackage：只有 dark 集 → 点名缺 light",
    /light/.test(validateThemePackage(darkOnlyPkg) ?? ""),
    `实得 ${JSON.stringify(validateThemePackage(darkOnlyPkg))}`,
  );
  check(
    "validateThemePackage：tokens 为空 → 点名缺 light",
    /light/.test(validateThemePackage(emptyTokensPkg) ?? ""),
    `实得 ${JSON.stringify(validateThemePackage(emptyTokensPkg))}`,
  );
  check(
    "validateThemePackage：无 tokens → 通过（tokens 本身可选）",
    validateThemePackage(noTokensPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(noTokensPkg))}`,
  );
  check(
    "validateThemePackage：light 为空集 {} → 点名缺 light（R6-10）",
    /light/.test(validateThemePackage(emptyLightPkg) ?? ""),
    `实得 ${JSON.stringify(validateThemePackage(emptyLightPkg))}`,
  );
  check(
    "validateThemePackage：light 全值为 undefined → 点名缺 light（R6-10）",
    /light/.test(validateThemePackage(undefLightPkg) ?? ""),
    `实得 ${JSON.stringify(validateThemePackage(undefLightPkg))}`,
  );
  check(
    "validateThemePackage：light 部分 undefined＋部分实值 → 通过（合法部分集）",
    validateThemePackage(partialUndefPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(partialUndefPkg))}`,
  );
}

registerTheme(lightPkg);
check("合法包（light 在位）注册成功", getTheme("ok-light") === lightPkg);

registerTheme(darkOnlyPkg);
check(
  "只有 dark 集的主题包被拒注册",
  getTheme("bad-dark-only") === undefined &&
    !getRegisteredThemes().some((t) => t.id === "bad-dark-only"),
);
check(
  "拒绝时 console.error 点名主题 id 与缺失的 light 集",
  errorLog.some((m) => m.includes("bad-dark-only") && /light/.test(m)),
  `捕获到: ${JSON.stringify(errorLog)}`,
);

registerTheme(emptyTokensPkg);
check(
  "tokens 在位但缺 light 的主题包被拒注册",
  getTheme("bad-empty-tokens") === undefined &&
    errorLog.some((m) => m.includes("bad-empty-tokens") && /light/.test(m)),
  `捕获到: ${JSON.stringify(errorLog)}`,
);

activateTheme("bad-dark-only");
check(
  "被拒包无法激活（active 仍为 null，不装作成功）",
  getActiveThemeId() === null,
  `实得 ${JSON.stringify(getActiveThemeId())}`,
);

registerTheme({ ...darkOnlyPkg, id: "ok-light" });
check(
  "同 id 非法包不能顶掉已注册的合法包",
  getTheme("ok-light") === lightPkg,
);

// ─── ⑩⑪ R6-10：在场但为空的两形态 ────────────────────────────────────────

registerTheme(emptyLightPkg);
check(
  "light 为空集 {} 的主题包被拒注册（R6-10）",
  getTheme("bad-empty-light") === undefined &&
    errorLog.some((m) => m.includes("bad-empty-light") && /light/.test(m)),
  `捕获到: ${JSON.stringify(errorLog)}`,
);

registerTheme(undefLightPkg);
check(
  "light 全值为 undefined 的主题包被拒注册（R6-10）",
  getTheme("bad-undef-light") === undefined &&
    errorLog.some((m) => m.includes("bad-undef-light") && /light/.test(m)),
  `捕获到: ${JSON.stringify(errorLog)}`,
);

activateTheme("bad-empty-light");
activateTheme("bad-undef-light");
check(
  "被拒的空集包无法激活（active 仍为 null，不装作成功）",
  getActiveThemeId() === null,
  `实得 ${JSON.stringify(getActiveThemeId())}`,
);

registerTheme({ ...emptyLightPkg, id: "ok-light" });
check(
  "同 id 空集包不能顶掉已注册的合法包（R6-10）",
  getTheme("ok-light") === lightPkg,
);

registerTheme({ ...undefLightPkg, id: "ok-light" });
check(
  "同 id 全 undefined 包不能顶掉已注册的合法包（R6-10）",
  getTheme("ok-light") === lightPkg,
);

registerTheme(partialUndefPkg);
check(
  "light 部分 undefined 的合法部分集仍可注册（不误伤）",
  getTheme("ok-partial-undef") === partialUndefPkg,
);

// ─── ⑫ R7-12：空串/纯空白与类型外形态（G-C5-01＋G-C5-02 登记组） ─────────

const emptyStrLightPkg = {
  ...base,
  id: "bad-empty-str-light",
  tokens: { light: { "--primary": "" } },
};
const blankStrLightPkg = {
  ...base,
  id: "bad-blank-str-light",
  tokens: { light: { "--primary": "   " } },
};
const nullValLightPkg = {
  ...base,
  id: "bad-null-val-light",
  tokens: { light: { "--primary": null } },
};
const zeroValLightPkg = {
  ...base,
  id: "bad-zero-val-light",
  tokens: { light: { "--primary": 0 } },
};
const falseValLightPkg = {
  ...base,
  id: "bad-false-val-light",
  tokens: { light: { "--primary": false } },
};
const objValLightPkg = {
  ...base,
  id: "bad-obj-val-light",
  tokens: { light: { "--primary": { hex: "#123456" } } },
};
const arrayLightPkg = {
  ...base,
  id: "bad-array-light",
  tokens: { light: ["#ffffff"] },
};
const mixedBlankPkg = {
  ...base,
  id: "ok-mixed-blank",
  tokens: { light: { "--primary": "", "--secondary": "#abcdef" } },
};
const blankValPkgs = [
  ["空串", emptyStrLightPkg],
  ["纯空白", blankStrLightPkg],
  ["null 值（类型外）", nullValLightPkg],
  ["数字 0 值（类型外）", zeroValLightPkg],
  ["布尔 false 值（类型外）", falseValLightPkg],
  ["对象值（类型外）", objValLightPkg],
  ["数组（类型外）", arrayLightPkg],
];

if (typeof validateThemePackage === "function") {
  for (const [label, pkg] of blankValPkgs) {
    check(
      `validateThemePackage：light 仅${label} → 点名缺 light（R7-12）`,
      /light/.test(validateThemePackage(pkg) ?? ""),
      `实得 ${JSON.stringify(validateThemePackage(pkg))}`,
    );
  }
  check(
    "validateThemePackage：空串＋实值混合 → 通过（合法部分集）",
    validateThemePackage(mixedBlankPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(mixedBlankPkg))}`,
  );
}

for (const [label, pkg] of blankValPkgs) {
  registerTheme(pkg);
  check(
    `light 仅${label}的主题包被拒注册（R7-12）`,
    getTheme(pkg.id) === undefined &&
      errorLog.some((m) => m.includes(pkg.id) && /light/.test(m)),
    `捕获到: ${JSON.stringify(errorLog)}`,
  );
}

activateTheme("bad-empty-str-light");
activateTheme("bad-blank-str-light");
check(
  "被拒的空串/纯空白包无法激活（active 仍为 null，不装作成功）",
  getActiveThemeId() === null,
  `实得 ${JSON.stringify(getActiveThemeId())}`,
);

registerTheme({ ...emptyStrLightPkg, id: "ok-light" });
check(
  "同 id 空串包不能顶掉已注册的合法包（R7-12）",
  getTheme("ok-light") === lightPkg,
);

registerTheme({ ...blankStrLightPkg, id: "ok-light" });
check(
  "同 id 纯空白包不能顶掉已注册的合法包（R7-12）",
  getTheme("ok-light") === lightPkg,
);

registerTheme(mixedBlankPkg);
check(
  "空串与实值混合的部分集仍可注册（不误伤）",
  getTheme("ok-mixed-blank") === mixedBlankPkg,
);

registerTheme(noTokensPkg);
check("无 tokens 的包（仅组件覆盖面）仍可注册", getTheme("ok-no-tokens") === noTokensPkg);

console.error = origError;

// ─── ⑨ 默认包色板与 globals.css :root 对齐（R4-02 永久回归门） ────────────

// globals.css 的 :root token 按级联求值取生效值（R7-10）：先剥
// CSS 注释（块内注释存档的旧值声明不参与，R6-C4-10），再跟踪
// at-rule 上下文——仅无条件 :root（顶层、或仅被 @layer 包裹）
// 的声明参与胜出竞争，@media/@supports/@container 等条件块
// （含未知块级 at-rule）整棵子树排除；胜出顺序：!important 级
// 高于普通级，普通级无层高于 @layer 内（层间按首次提及顺序后
// 者胜）、!important 级反之，同级同层按源码序后者胜。与
// check-contrast.mjs 的 resolveRootTokens 同口径（各门自含、
// 不跨文件引用）。
const resolveRootTokens = (code) => {
  const layerOrder = new Map();
  let layerSeq = 0;
  const noteLayers = (namesText) => {
    for (const part of namesText.split(",")) {
      const name = part.trim();
      if (name && !layerOrder.has(name)) layerOrder.set(name, layerSeq++);
    }
  };
  const winners = new Map();
  let srcIdx = 0;
  const rankCmp = (a, b) => {
    for (let i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return a[i] - b[i];
    }
    return 0;
  };
  const consider = (name, value, important, layerName) => {
    const layered = layerName != null;
    const lo = layered ? (layerOrder.get(layerName) ?? 0) : 0;
    const rank = important
      ? [1, layered ? 1 : 0, -lo, srcIdx]
      : [0, layered ? 0 : 1, lo, srcIdx];
    srcIdx++;
    const cur = winners.get(name);
    if (!cur || rankCmp(rank, cur.rank) > 0) winners.set(name, { rank, value });
  };
  const parseDecls = (body, layerName) => {
    let i = 0;
    const n = body.length;
    while (i < n) {
      let depth = 0;
      let q = null;
      const start = i;
      while (i < n) {
        const c = body[i];
        if (q) {
          if (c === "\\") i++;
          else if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; i++; continue; }
        if (c === "(") depth++;
        else if (c === ")") depth--;
        else if (c === ";" && depth === 0) break;
        i++;
      }
      const decl = body.slice(start, i).trim();
      i++;
      const m = decl.match(/^(--[a-zA-Z0-9-]+)\s*:\s*([\s\S]+)$/);
      if (!m) continue;
      let value = m[2].trim();
      let important = false;
      const im = value.match(/!\s*important\s*$/i);
      if (im) {
        important = true;
        value = value.slice(0, im.index).trim();
      }
      consider(m[1], value, important, layerName);
    }
  };
  const walk = (text, frames) => {
    let i = 0;
    const n = text.length;
    while (i < n) {
      let pre = "";
      let depth = 0;
      let q = null;
      let term = null;
      while (i < n) {
        const c = text[i];
        if (q) {
          pre += c;
          if (c === "\\") { pre += text[i + 1] ?? ""; i += 2; continue; }
          if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; pre += c; i++; continue; }
        if (c === "(") depth++;
        else if (c === ")") depth--;
        if (depth === 0 && (c === "{" || c === ";" || c === "}")) {
          term = c;
          i++;
          break;
        }
        pre += c;
        i++;
      }
      const prelude = pre.trim();
      if (term === null || term === "}") break;
      if (term === ";") {
        if (/^@layer\b/i.test(prelude)) {
          noteLayers(prelude.replace(/^@layer\b/i, ""));
        }
        continue;
      }
      const open = i - 1;
      let d = 0;
      let end = -1;
      q = null;
      for (let j = open; j < n; j++) {
        const c = text[j];
        if (q) {
          if (c === "\\") j++;
          else if (c === q) q = null;
          continue;
        }
        if (c === '"' || c === "'") { q = c; continue; }
        if (c === "{") d++;
        else if (c === "}") {
          d--;
          if (d === 0) { end = j; break; }
        }
      }
      if (end < 0) break;
      const body = text.slice(open + 1, end);
      i = end + 1;
      if (/^@layer\b/i.test(prelude)) {
        const names = prelude.replace(/^@layer\b/i, "").trim();
        noteLayers(names);
        const name = names.split(",")[0].trim() || null;
        walk(body, [...frames, { type: "layer", name }]);
      } else if (prelude.startsWith("@")) {
        continue;
      } else {
        if (frames.some((f) => f.type === "cond")) continue;
        const layerFrame = [...frames]
          .reverse()
          .find((f) => f.type === "layer");
        if (prelude.split(",").some((s) => s.trim() === ":root")) {
          parseDecls(body, layerFrame ? layerFrame.name : null);
        }
        walk(body, frames);
      }
    }
  };
  walk(code, []);
  const out = {};
  for (const [k, v] of winners) out[k] = v.value;
  return out;
};

const globalsSrc = stripCssComments(readSrc("src/styles/globals.css"));
const globalsTokens = {};
for (const [k, v] of Object.entries(resolveRootTokens(globalsSrc))) {
  globalsTokens[k] = v.trim().toLowerCase();
}

// 默认包只有 import type，转译后取 exports.default 的真实 light 集。
const { outputText: defaultOut } = ts.transpileModule(defaultSrc, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
});
const defaultMod = { exports: {} };

new Function("module", "exports", "require", defaultOut)(
  defaultMod,
  defaultMod.exports,
  require,
);
const defaultLight = defaultMod.exports.default?.tokens?.light ?? {};
const drift = Object.entries(defaultLight).filter(
  ([k, v]) => globalsTokens[k] !== String(v).trim().toLowerCase(),
);

check(
  "默认主题包 light 集与 globals.css :root 相异集＝∅（R4-02 对齐钉）",
  Object.keys(defaultLight).length > 0 && drift.length === 0,
  drift
    .map(([k, v]) => `${k}: 包=${v} globals=${globalsTokens[k] ?? "<缺>"}`)
    .join("; "),
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
