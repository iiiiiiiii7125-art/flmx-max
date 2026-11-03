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
//
// 提取面（修复轮 6，R6-C4-10 机制处置）：断言⑨的 globals 侧旧实现
// 以正则合并 :root 块、无注释剥离——在 :root 块内给改过的 token
// 同行加注释存档旧值声明，注释声明后胜出、相异集报 ∅。现 globals
// 侧先词法剥除 CSS 注释再合并；断言⑧的源码文本钉同样先剥 TS 注
// 释（注释里的回退形态提及不算数、也不能靠注释拷贝满足/干扰判
// 定），行为断言 ①–⑦ 的转译执行面不变。
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

registerTheme(noTokensPkg);
check("无 tokens 的包（仅组件覆盖面）仍可注册", getTheme("ok-no-tokens") === noTokensPkg);

console.error = origError;

// ─── ⑨ 默认包色板与 globals.css :root 对齐（R4-02 永久回归门） ────────────

// 合并 globals.css 全部 :root 块（文件序、后声明胜出，与级联同义）。
// 先剥 CSS 注释：块内注释存档的旧值声明不再参与合并（R6-C4-10）。
const globalsSrc = stripCssComments(readSrc("src/styles/globals.css"));
const globalsTokens = {};

for (const block of globalsSrc.matchAll(/:root\s*\{([^}]*)\}/g)) {
  for (const decl of block[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    globalsTokens[decl[1]] = decl[2].trim().toLowerCase();
  }
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
