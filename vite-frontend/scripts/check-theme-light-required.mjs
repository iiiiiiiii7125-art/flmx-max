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
//   ⑬ 「有效计数＝有效注入」三轴收口（R8-01 / H-C5-01）：⑫的计
//      数只验值——词表外键（--not-a-token／大小写变体／裸键／尾
//      空格键／数字键）计数过关而注入恒 0；light 为字符串或
//      boxed String 时被 Object.values 逐字枚举成单字符「值」
//      过关；纯格式字符值（U+200B／U+2060 等 Unicode Cf 类：
//      JS trim 不剥、CSS 计算值阶段无效）计数过关——三轴均可同
//      id 顶掉合法包。本轮谓词改为三轴合取：键 ∈ ThemeTokens
//      词表 ∧ light 为非数组普通对象 ∧ 值剔除空白与 Cf 后非
//      空。本节逐轴钉死（键轴五形态、容器轴两形态、值轴异体两
//      格、逐轴同 id 顶替矩阵），并把注册表的 THEME_TOKEN_KEYS
//      与 types.ts 的 ThemeTokens 命名键集逐一对账（双向差集
//      ∅），防词表两处漂移。正对照：词表外键与词表内实值混合
//      的部分集仍合法（词表内实值计数）；含实义但夹带格式字
//      符的值剔除后非空、仍合法。
//   ⑭ 「计数＝注入」同一判据＋计算有效性（R9-12 / I-C5-01 ≡
//      I-F6-01；R9-28 / I-C4-13；R9-29 / I-C4-14）：⑬收口的是计
//      数侧——注入侧 injectTokens 仍只判 !== undefined，混合包
//      里词表内键下的不可用条目（纯空白/纯 Cf/非字符串）被原样
//      setProperty、顶掉 :root 出厂回退，与 validateThemePackage
//      头注「不可用条目会被跳过」的承诺不符；且值轴只及字符层
//      ——CSS 全域关键字（initial 等五词全集）与 var() 自引用/
//      环引用值字符非空、计算值阶段却 guaranteed-invalid，同
//      属「计数过关、实注为零」。本轮收口为单一可用性函数
//      computeUsableTokenKeys：计数与注入两消费者共用，值轴扩
//      至计算有效性（全域关键字整值命中、集内 var() 依赖环成
//      员计不可用）。本节钉：混合包激活后不可用条目逐键
//      setProperty 0 次、可用条目照常注入（activate 与
//      reapply 两面）；关键字五词＋大小写变体、自引用、两键/
//      三键环的整包形态逐一拒并点名＋代表格同 id 不顶替；正
//      对照钉 var() 链式引用（无环）、带回退引用不可用键、引
//      用集外键、依赖环成员但本身不在环上四格不误伤。
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
// 嵌套与匿名层口径（修复轮 8，R8-02/R8-03 / H-C4-01/02 机制处
// 置）：轮 7 求值器仍有两处与浏览器级联不一致——匿名 @layer 块
// 的层名落为 null、被按无层声明计权（与浏览器正相反），层内
// 存档旧值可骗过⑨与对比度门；规则嵌套不建模，永不匹配祖先内
// 的 :root 被当顶层计、:root 体内嵌套条件块第二位起的声明被
// 逐片当无条件声明计。现两门同改：匿名层分配合成层名按出现
// 位置参与层序；分发携带祖先选择器上下文（有规则祖先后内层
// :root 恒不匹配、不计数）；parseDecls 按花括号深度切分、嵌
// 套块整块交回分发。两门内嵌的 resolveRootTokens 逐字同构，
// 动其一须动另一。
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

// ─── ⑬ R8-01：有效计数三轴收口（H-C5-01） ────────────────────────────────

// 词表对账：注册表运行时的 THEME_TOKEN_KEYS 必须与 types.ts 中
// ThemeTokens 的命名键集逐一相等（双向差集 ∅）——谓词以此集判
// 键轴，集与类型两处定义不许漂移。命名键以 `"--名"?:` 形态抽取
// （索引签名的转义舱 [key: `--${string}`] 不在此形态内）。
const { THEME_TOKEN_KEYS } = mod.exports;
const typesNamedKeys = new Set(
  [...stripComments(typesSrc).matchAll(/"(--[a-zA-Z0-9-]+)"\?:/g)].map(
    (m) => m[1],
  ),
);

check(
  "THEME_TOKEN_KEYS 导出存在且为非空 Set（R8-01）",
  THEME_TOKEN_KEYS instanceof Set && THEME_TOKEN_KEYS.size > 0,
  `实得 ${Object.prototype.toString.call(THEME_TOKEN_KEYS)}`,
);
if (THEME_TOKEN_KEYS instanceof Set) {
  const onlyInRegistry = [...THEME_TOKEN_KEYS].filter(
    (k) => !typesNamedKeys.has(k),
  );
  const onlyInTypes = [...typesNamedKeys].filter(
    (k) => !THEME_TOKEN_KEYS.has(k),
  );

  check(
    "THEME_TOKEN_KEYS 与 types.ts ThemeTokens 命名键集双向差集＝∅（R8-01）",
    typesNamedKeys.size > 0 &&
      onlyInRegistry.length === 0 &&
      onlyInTypes.length === 0,
    `仅注册表: ${onlyInRegistry.join(",")}；仅类型: ${onlyInTypes.join(",")}`,
  );
}

// 三轴坏形态（[标签, light 形态]）：键轴五形态、容器轴两形态、
// 值轴异体两格。逐形态钉 validate 点名拒＋register 拒；逐轴再跑
// 同 id 顶替矩阵（先注册合法包，再以该轴全部形态同 id 喂入，
// 合法包必须逐一留存）。
const axisGroups = [
  [
    "键轴",
    [
      ["词表外键", { "--not-a-token": "#ffffff" }],
      ["大小写变体键", { "--Primary": "#ffffff" }],
      ["裸键", { primary: "#ffffff" }],
      ["尾空格键", { "--primary ": "#ffffff" }],
      ["数字键", { 0: "#ffffff" }],
    ],
  ],
  [
    "容器轴",
    [
      ["字符串 light", "#ffffff"],
      ["boxed String light", new String("#ffffff")],
    ],
  ],
  [
    "值轴异体",
    [
      ["零宽空格值 U+200B", { "--primary": "\u200B" }],
      ["词组连接符值 U+2060", { "--primary": "\u2060" }],
    ],
  ],
];

let axisSeq = 0;

for (const [axisLabel, forms] of axisGroups) {
  for (const [formLabel, light] of forms) {
    axisSeq++;
    const badPkg = { ...base, id: `bad-axis-${axisSeq}`, tokens: { light } };

    if (typeof validateThemePackage === "function") {
      check(
        `validateThemePackage：${axisLabel}${formLabel} → 点名缺 light（R8-01）`,
        /light/.test(validateThemePackage(badPkg) ?? ""),
        `实得 ${JSON.stringify(validateThemePackage(badPkg))}`,
      );
    }
    registerTheme(badPkg);
    check(
      `${axisLabel}${formLabel}的主题包被拒注册（R8-01）`,
      getTheme(badPkg.id) === undefined &&
        errorLog.some((m) => m.includes(badPkg.id) && /light/.test(m)),
      `捕获到: ${JSON.stringify(errorLog)}`,
    );
  }
  const anchorPkg = {
    ...base,
    id: `ok-axis-anchor-${axisLabel}`,
    tokens: { light: { "--primary": "#123456" } },
  };

  registerTheme(anchorPkg);
  check(
    `${axisLabel}顶替矩阵锚点合法包注册成功（R8-01）`,
    getTheme(anchorPkg.id) === anchorPkg,
  );
  for (const [formLabel, light] of forms) {
    registerTheme({ ...base, id: anchorPkg.id, tokens: { light } });
    check(
      `同 id ${axisLabel}${formLabel}包不能顶掉合法包（R8-01）`,
      getTheme(anchorPkg.id) === anchorPkg,
    );
  }
}

// 正对照（不误伤）：词表外键与词表内实值混合 → 词表内实值计数、
// 合法；值含实义但夹带格式字符 → 剔除空白与 Cf 后非空、合法。
const mixedVocabPkg = {
  ...base,
  id: "ok-mixed-vocab",
  tokens: { light: { "--not-a-token": "#000000", "--primary": "#123456" } },
};
const cfMixedPkg = {
  ...base,
  id: "ok-cf-mixed",
  tokens: { light: { "--primary": "#123456\u200B" } },
};

if (typeof validateThemePackage === "function") {
  check(
    "validateThemePackage：词表外键＋词表内实值混合 → 通过（合法部分集）",
    validateThemePackage(mixedVocabPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(mixedVocabPkg))}`,
  );
  check(
    "validateThemePackage：实义值夹带格式字符 → 通过（剔除后非空）",
    validateThemePackage(cfMixedPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(cfMixedPkg))}`,
  );
}
registerTheme(mixedVocabPkg);
check(
  "词表外键＋实值混合的部分集仍可注册（不误伤）",
  getTheme("ok-mixed-vocab") === mixedVocabPkg,
);
registerTheme(cfMixedPkg);
check(
  "实义值夹带格式字符的包仍可注册（不误伤）",
  getTheme("ok-cf-mixed") === cfMixedPkg,
);

// ─── ⑭ R9-12/R9-28/R9-29：注入同一判据＋计算有效性 ──────────────────────

// 换上记录型 CSSOM 桩：此前各节的桩吞掉 setProperty，本节逐条
// 核注入实录（哪把键被设、值是什么、哪把键零调用）。
const setCalls = [];

globalThis.document.documentElement.style.setProperty = (name, value) => {
  setCalls.push([name, value]);
};
globalThis.document.documentElement.style.removeProperty = () => {};
const { reapplyActiveTheme, deactivateTheme } = mod.exports;
const callsFor = (name) => setCalls.filter(([n]) => n === name);

// 混合包（R9-12）：1 个可用条目过校验，其余逐形态不可用——激
// 活后不可用条目必须逐键 setProperty 0 次（被跳过、不顶掉回
// 退），可用条目照常注入；词表外键同在跳过之列（计数与注入
// 同一可用性集）。
const mixedInjectPkg = {
  ...base,
  id: "ok-mixed-inject",
  tokens: {
    light: {
      "--primary": "#111111",
      "--background": "   ",
      "--foreground": "\u200B",
      "--border": null,
      "--input": 42,
      "--ring": false,
      "--content1": {},
      "--divider": new String("#333333"),
      "--not-a-token": "#000000",
    },
  },
};
const mixedUnusableKeys = [
  ["纯空白", "--background"],
  ["纯格式字符", "--foreground"],
  ["null（类型外）", "--border"],
  ["数字 42（类型外）", "--input"],
  ["布尔 false（类型外）", "--ring"],
  ["对象（类型外）", "--content1"],
  ["boxed String（类型外）", "--divider"],
  ["词表外键", "--not-a-token"],
];

if (typeof validateThemePackage === "function") {
  check(
    "validateThemePackage：混合包（1 可用＋8 不可用）→ 通过（合法部分集）",
    validateThemePackage(mixedInjectPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(mixedInjectPkg))}`,
  );
}
registerTheme(mixedInjectPkg);
check(
  "混合包注册成功（R9-12）",
  getTheme("ok-mixed-inject") === mixedInjectPkg,
);

setCalls.length = 0;
activateTheme("ok-mixed-inject");
check(
  "混合包激活成功（active 为该包）",
  getActiveThemeId() === "ok-mixed-inject",
  `实得 ${JSON.stringify(getActiveThemeId())}`,
);
check(
  "混合包可用条目照常注入（--primary = #111111，恰 1 次）",
  callsFor("--primary").length === 1 &&
    callsFor("--primary")[0][1] === "#111111",
  `实录 ${JSON.stringify(setCalls)}`,
);
for (const [label, key] of mixedUnusableKeys) {
  check(
    `混合包不可用条目被跳过：${label} ${key} setProperty 0 次（R9-12）`,
    callsFor(key).length === 0,
    `实录 ${JSON.stringify(setCalls)}`,
  );
}

setCalls.length = 0;
reapplyActiveTheme();
check(
  "reapplyActiveTheme 同一判据：可用条目重注入、不可用条目仍 0 次（R9-12）",
  callsFor("--primary").length === 1 &&
    mixedUnusableKeys.every(([, key]) => callsFor(key).length === 0),
  `实录 ${JSON.stringify(setCalls)}`,
);
deactivateTheme();

// 计算有效性整包形态（R9-28/R9-29）：整集无一可用条目 → 与
// ⑫⑬ 同形态拒并点名。
const computeBadForms = [
  ["CSS 全域关键字 initial", { "--primary": "initial" }],
  ["CSS 全域关键字 unset", { "--primary": "unset" }],
  ["CSS 全域关键字 inherit", { "--primary": "inherit" }],
  ["CSS 全域关键字 revert", { "--primary": "revert" }],
  ["CSS 全域关键字 revert-layer", { "--primary": "revert-layer" }],
  ["CSS 全域关键字大小写/空白变体", { "--primary": " INITIAL " }],
  ["var() 自引用", { "--primary": "var(--primary)" }],
  [
    "var() 两键互引用环",
    { "--primary": "var(--secondary)", "--secondary": "var(--primary)" },
  ],
  [
    "var() 三键环",
    {
      "--primary": "var(--secondary)",
      "--secondary": "var(--background)",
      "--background": "var(--primary)",
    },
  ],
];

let computeSeq = 0;

for (const [label, light] of computeBadForms) {
  computeSeq++;
  const badPkg = { ...base, id: `bad-compute-${computeSeq}`, tokens: { light } };

  if (typeof validateThemePackage === "function") {
    check(
      `validateThemePackage：${label} → 点名缺 light（R9-28/R9-29）`,
      /light/.test(validateThemePackage(badPkg) ?? ""),
      `实得 ${JSON.stringify(validateThemePackage(badPkg))}`,
    );
  }
  registerTheme(badPkg);
  check(
    `${label}的主题包被拒注册（R9-28/R9-29）`,
    getTheme(badPkg.id) === undefined &&
      errorLog.some((m) => m.includes(badPkg.id) && /light/.test(m)),
    `捕获到: ${JSON.stringify(errorLog)}`,
  );
}

registerTheme({ ...base, id: "ok-light", tokens: { light: { "--primary": "initial" } } });
check(
  "同 id 关键字包不能顶掉已注册的合法包（R9-28）",
  getTheme("ok-light") === lightPkg,
);
registerTheme({ ...base, id: "ok-light", tokens: { light: { "--primary": "var(--primary)" } } });
check(
  "同 id 自引用包不能顶掉已注册的合法包（R9-29）",
  getTheme("ok-light") === lightPkg,
);

// 拒绝文案枚举钉（集成 9 文案补列）：拒绝文案与 install.md 的
// 「被拒形态」枚举同为 9 形态——⑭新增的两类整包形态（CSS 全
// 域关键字值、var() 自引用/环引用值）须在文案中被点名，旧 7
// 形态的枚举措辞不得回退丢失；任一面漂移即红。
if (typeof validateThemePackage === "function") {
  const legacyReason =
    validateThemePackage({ ...base, id: "msg-legacy", tokens: {} }) ?? "";
  check(
    "拒绝文案保留旧形态枚举（all-undefined / non-object 等）",
    legacyReason.includes("all-undefined") &&
      legacyReason.includes("unknown-key-only") &&
      legacyReason.includes("non-object"),
    `实得 ${JSON.stringify(legacyReason)}`,
  );
  const keywordReason =
    validateThemePackage({
      ...base,
      id: "msg-keyword",
      tokens: { light: { "--primary": "initial" } },
    }) ?? "";
  check(
    "拒绝文案点名 CSS 全域关键字形态（R9-28 文案补列）",
    keywordReason.includes("css-wide-keyword"),
    `实得 ${JSON.stringify(keywordReason)}`,
  );
  const cycleReason =
    validateThemePackage({
      ...base,
      id: "msg-cycle",
      tokens: { light: { "--primary": "var(--primary)" } },
    }) ?? "";
  check(
    "拒绝文案点名 var() 引用环形态（R9-29 文案补列）",
    cycleReason.includes("var()-cycle"),
    `实得 ${JSON.stringify(cycleReason)}`,
  );
}

// 正对照（不误伤）：合法 var() 引用——指向他键、无环——照常
// 计数与注入。
const varChainPkg = {
  ...base,
  id: "ok-var-chain",
  tokens: {
    light: {
      "--primary": "var(--secondary)",
      "--secondary": "var(--background)",
      "--background": "#fafafa",
    },
  },
};
const varFallbackPkg = {
  ...base,
  id: "ok-var-fallback",
  tokens: {
    light: {
      "--primary": "var(--secondary, #654321)",
      "--secondary": "initial",
    },
  },
};
const varExternalPkg = {
  ...base,
  id: "ok-var-external",
  tokens: { light: { "--primary": "var(--background)" } },
};
const varDependentPkg = {
  ...base,
  id: "ok-var-dependent",
  tokens: {
    light: {
      "--primary": "var(--secondary)",
      "--secondary": "var(--secondary)",
      "--background": "#111111",
    },
  },
};

if (typeof validateThemePackage === "function") {
  check(
    "validateThemePackage：var() 链式引用（无环）→ 通过（不误伤）",
    validateThemePackage(varChainPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(varChainPkg))}`,
  );
  check(
    "validateThemePackage：带回退引用关键字键 → 通过（引用方可用）",
    validateThemePackage(varFallbackPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(varFallbackPkg))}`,
  );
  check(
    "validateThemePackage：var() 引用集外键 → 通过（走应用默认值）",
    validateThemePackage(varExternalPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(varExternalPkg))}`,
  );
  check(
    "validateThemePackage：依赖自引用键但本身不在环上 → 通过（不误伤）",
    validateThemePackage(varDependentPkg) === null,
    `实得 ${JSON.stringify(validateThemePackage(varDependentPkg))}`,
  );
}
registerTheme(varChainPkg);
registerTheme(varFallbackPkg);
registerTheme(varExternalPkg);
registerTheme(varDependentPkg);
check(
  "var() 正对照四包注册成功（不误伤）",
  getTheme("ok-var-chain") === varChainPkg &&
    getTheme("ok-var-fallback") === varFallbackPkg &&
    getTheme("ok-var-external") === varExternalPkg &&
    getTheme("ok-var-dependent") === varDependentPkg,
);

setCalls.length = 0;
activateTheme("ok-var-chain");
check(
  "var() 链式引用三键照常注入（原样值，环外引用不设限）",
  callsFor("--primary").length === 1 &&
    callsFor("--primary")[0][1] === "var(--secondary)" &&
    callsFor("--secondary").length === 1 &&
    callsFor("--secondary")[0][1] === "var(--background)" &&
    callsFor("--background").length === 1 &&
    callsFor("--background")[0][1] === "#fafafa",
  `实录 ${JSON.stringify(setCalls)}`,
);

setCalls.length = 0;
activateTheme("ok-var-fallback");
check(
  "带回退引用：引用方照常注入、关键字键被跳过 0 次（R9-28）",
  callsFor("--primary").length === 1 &&
    callsFor("--primary")[0][1] === "var(--secondary, #654321)" &&
    callsFor("--secondary").length === 0,
  `实录 ${JSON.stringify(setCalls)}`,
);

setCalls.length = 0;
activateTheme("ok-var-dependent");
check(
  "依赖环成员：环上键被跳过 0 次、环外键照常注入（R9-29）",
  callsFor("--secondary").length === 0 &&
    callsFor("--primary").length === 1 &&
    callsFor("--background").length === 1,
  `实录 ${JSON.stringify(setCalls)}`,
);
deactivateTheme();

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
// 不跨文件引用）。//
// 条件求值与选择器口径（修复轮 9，R9-16～R9-20 机制处置）：轮 8
// 后仍有五格与浏览器级联不一致——恒真条件块（@media all 等）
// 整棵排除、合成层名可被作者预登记非法同名劫持、:ROOT 大小写
// 伪类不计数、html:root 等复合根选择器整体不可见、嵌套 &:root
// 不展开。现：条件 at-rule 三值求值（恒真按无条件走查、恒假与
// 不可判维持整棵排除）；层名按自定义标识符合法性校验，非法层
// 名经 problems 通道由两门点名报红（fail-closed），合成层名与
// 作者层名空间就此隔离；选择器按复合解析，含正向 :root（伪类
// 名大小写不敏感）且其余组件仅类型 html/* 与伪类者命中根元
// 素、按特异度参与胜出（层序仍先于特异度）；嵌套 & 复合按展
// 开后选择器判定。求值器文末附合成 CSS 钉格逐格钉住五条。
const resolveRootTokens = (code) => {
  const problems = [];
  const layerOrder = new Map();
  let layerSeq = 0;
  // 层名合法性（R9-17 / I-C4-02）：层名是点分自定义标识符串，括
  // 号、空格等字符不可能出现在合法层名里。作者写出的非法层名在
  // 浏览器中使整条 @layer 声明/规则块被丢弃，求值器同口径处理
  // 并记为建模问题（两门据此点名报红，fail-closed）；合成层名
  // 沿用带括号形态——合法作者层名永远写不出该形态，两个名空间
  // 就此隔离，预登记同名劫持不再可能。
  const LAYER_WIDE_KEYWORDS = new Set([
    "initial",
    "inherit",
    "unset",
    "revert",
    "revert-layer",
    "default",
  ]);
  const IDENT_SEGMENT =
    /^-?(?:[_a-zA-Z]|[^\x00-\x7F])(?:[_a-zA-Z0-9-]|[^\x00-\x7F])*$/;
  const isValidLayerName = (name) =>
    name !== "" &&
    name
      .split(".")
      .every(
        (seg) =>
          IDENT_SEGMENT.test(seg) && !LAYER_WIDE_KEYWORDS.has(seg.toLowerCase()),
      );
  // 语句形 @layer a, b; 的层名登记：任一名字非法即整条不登记
  // （与浏览器整条丢弃同口径），问题逐个记录。
  const noteLayers = (namesText) => {
    const names = namesText
      .split(",")
      .map((part) => part.trim())
      .filter((name) => name !== "");
    let ok = true;
    for (const name of names) {
      if (!isValidLayerName(name)) {
        problems.push(
          `@layer 层名 "${name}" 不是合法层名，浏览器会整条丢弃该层声明`,
        );
        ok = false;
      }
    }
    if (!ok) return;
    for (const name of names) {
      if (!layerOrder.has(name)) layerOrder.set(name, layerSeq++);
    }
  };
  // 匿名 @layer 块：级联中每个匿名块是一个独立的新层、按出现位
  // 置参与层序，必须分配合成层名——落为 null 会被 consider 当作
  // 无层声明（普通级最高优先），与浏览器正相反（无层恒胜有
  // 层），层内存档旧值就此骗过求值（R8-02 / H-C4-01）。
  const freshAnonLayer = () => {
    const name = `(anon-layer-${layerSeq})`;
    layerOrder.set(name, layerSeq++);
    return name;
  };
  const winners = new Map();
  let srcIdx = 0;
  const rankCmp = (a, b) => {
    for (let i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return a[i] - b[i];
    }
    return 0;
  };
  const consider = (name, value, important, layerName, spec) => {
    const layered = layerName != null;
    const lo = layered ? (layerOrder.get(layerName) ?? 0) : 0;
    // 胜出顺序按 CSS 级联：!important 级高于普通级；层序先于特
    // 异度——普通级无层 > 有层（层间后提及者胜），!important 级
    // 反转（有层 > 无层、层间先提及者胜）；同层按特异度高者
    // 胜；再同按源码顺序后者胜。
    const rank = important
      ? [1, layered ? 1 : 0, -lo, spec[0], spec[1], spec[2], srcIdx]
      : [0, layered ? 0 : 1, lo, spec[0], spec[1], spec[2], srcIdx];
    srcIdx++;
    const cur = winners.get(name);
    if (!cur || rankCmp(rank, cur.rank) > 0) winners.set(name, { rank, value });
  };
  // 取从 openIdx（指向 "{"）起的花括号配平块：返回 { body, next }
  // （next 为配平 "}" 之后的位置），不配平返回 null。引号内的花
  // 括号不计深度。
  const extractBlock = (text, openIdx) => {
    let d = 0;
    let q = null;
    for (let j = openIdx; j < text.length; j++) {
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
        if (d === 0) return { body: text.slice(openIdx + 1, j), next: j + 1 };
      }
    }
    return null;
  };
  const applyDecl = (decl, layerName, spec) => {
    const m = decl.match(/^(--[a-zA-Z0-9-]+)\s*:\s*([\s\S]+)$/);
    if (!m) return;
    let value = m[2].trim();
    let important = false;
    const im = value.match(/!\s*important\s*$/i);
    if (im) {
      important = true;
      value = value.slice(0, im.index).trim();
    }
    consider(m[1], value, important, layerName, spec);
  };
  // ── 选择器建模（R9-18/R9-19）──────────────────────────────
  // 在指定分隔符的顶层（括号/方括号深度 0、引号外）切分文本。
  const splitTopLevel = (text, sep) => {
    const parts = [];
    let depth = 0;
    let bdepth = 0;
    let q = null;
    let cur = "";
    for (let i = 0; i < text.length; i++) {
      const c = text[i];
      if (q) {
        cur += c;
        if (c === "\\") {
          cur += text[i + 1] ?? "";
          i++;
        } else if (c === q) q = null;
        continue;
      }
      if (c === '"' || c === "'") { q = c; cur += c; continue; }
      if (c === "(") depth++;
      else if (c === ")") depth--;
      else if (c === "[") bdepth++;
      else if (c === "]") bdepth--;
      if (c === sep && depth === 0 && bdepth === 0) {
        parts.push(cur);
        cur = "";
        continue;
      }
      cur += c;
    }
    parts.push(cur);
    return parts;
  };
  const IDENT_AT = /^-?(?:[_a-zA-Z]|[^\x00-\x7F])(?:[_a-zA-Z0-9-]|[^\x00-\x7F])*/;
  // 解析单个复合选择器为简单选择器组件列；含组合子（空白/>/+/
  // ~）、无法解析的形态返回 null。函数型伪类的实参原文保留。
  const parseCompound = (sel) => {
    const comps = [];
    let i = 0;
    const n = sel.length;
    const readIdent = () => {
      const m = IDENT_AT.exec(sel.slice(i));
      if (!m) return null;
      i += m[0].length;
      return m[0];
    };
    const readParens = () => {
      // sel[i] === "("：返回配平内容并越过 ")"，不配平返回 null。
      let d = 0;
      let q = null;
      const start = i;
      while (i < n) {
        const c = sel[i];
        if (q) {
          if (c === "\\") i++;
          else if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; i++; continue; }
        if (c === "(") d++;
        else if (c === ")") {
          d--;
          if (d === 0) {
            const inner = sel.slice(start + 1, i);
            i++;
            return inner;
          }
        }
        i++;
      }
      return null;
    };
    while (i < n) {
      const c = sel[i];
      if (c === "*") {
        comps.push({ kind: "universal" });
        i++;
        continue;
      }
      if (c === ".") {
        i++;
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "class", name });
        continue;
      }
      if (c === "#") {
        i++;
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "id", name });
        continue;
      }
      if (c === "[") {
        let q = null;
        let closed = false;
        i++;
        while (i < n) {
          const ch = sel[i];
          if (q) {
            if (ch === "\\") i++;
            else if (ch === q) q = null;
          } else if (ch === '"' || ch === "'") q = ch;
          else if (ch === "]") {
            closed = true;
            i++;
            break;
          }
          i++;
        }
        if (!closed) return null;
        comps.push({ kind: "attr" });
        continue;
      }
      if (c === ":") {
        if (sel[i + 1] === ":") {
          i += 2;
          const name = readIdent();
          if (name === null) return null;
          if (sel[i] === "(") {
            if (readParens() === null) return null;
          }
          comps.push({ kind: "pseudo-el", name });
          continue;
        }
        i++;
        const name = readIdent();
        if (name === null) return null;
        let args = null;
        if (sel[i] === "(") {
          args = readParens();
          if (args === null) return null;
        }
        comps.push({ kind: "pseudo", name, args });
        continue;
      }
      if (/[_a-zA-Z-]|[^\x00-\x7F]/.test(c)) {
        if (comps.length > 0) return null; // 类型选择器只能居首
        const name = readIdent();
        if (name === null) return null;
        comps.push({ kind: "type", name });
        continue;
      }
      return null; // 空白/组合子/其余字符：非单个复合选择器
    }
    return comps;
  };
  const specCmp = rankCmp;
  const compoundSpec = (comps) => {
    let a = 0;
    let b = 0;
    let c = 0;
    for (const comp of comps) {
      if (comp.kind === "id") a++;
      else if (comp.kind === "class" || comp.kind === "attr") b++;
      else if (comp.kind === "type" || comp.kind === "pseudo-el") c++;
      else if (comp.kind === "pseudo") {
        const nm = comp.name.toLowerCase();
        if (nm === "where") continue;
        if (nm === "not" || nm === "is" || nm === "has") {
          // 函数型伪类自身不计、取实参中最高特异度（规范口径）。
          let best = null;
          for (const frag of splitTopLevel(comp.args ?? "", ",")) {
            const inner = parseCompound(frag.trim());
            if (!inner) continue;
            const s = compoundSpec(inner);
            if (!best || specCmp(s, best) > 0) best = s;
          }
          if (best) {
            a += best[0];
            b += best[1];
            c += best[2];
          }
        } else b++;
      }
    }
    return [a, b, c];
  };
  // 状态伪类（静止态求值不计，与 &:hover 先例同口径）。
  const STATE_PSEUDOS = new Set([
    "hover",
    "focus",
    "focus-visible",
    "focus-within",
    "active",
    "visited",
    "link",
    "target",
  ]);
  // 复合选择器是否可静态判定为「命中根元素」：须含正向 :root
  // 组件（伪类名 ASCII 大小写不敏感——:ROOT 与 :root 同效，
  // R9-18）；类型选择器只能缺省、* 或 html；.class/#id/[attr]
  // 组件依赖根元素的运行时属性集、伪元素命中的不是元素本
  // 体、状态伪类属非静止态——含此三类组件的分片静态不可判，
  // 不入集（与条件块不可判面同口径）。
  const isRootCompound = (comps) => {
    let positive = false;
    for (const comp of comps) {
      if (comp.kind === "universal") continue;
      if (comp.kind === "type") {
        if (comp.name.toLowerCase() !== "html") return false;
        continue;
      }
      if (comp.kind === "pseudo") {
        const nm = comp.name.toLowerCase();
        if (STATE_PSEUDOS.has(nm)) return false;
        if (nm === "root") {
          positive = true;
          continue;
        }
        if (nm === "is" || nm === "where") {
          const anyRoot = splitTopLevel(comp.args ?? "", ",")
            .some((frag) => {
              const inner = parseCompound(frag.trim());
              return inner !== null && isRootCompound(inner);
            });
          if (anyRoot) positive = true;
          continue;
        }
        continue;
      }
      return false; // class / id / attr / pseudo-el
    }
    return positive;
  };
  // 选择器列表中命中根元素的分片取最高特异度：返回 {spec,text}
  // 或 null（无分片命中）。html:root 的特异度 (0,1,1) 恒胜裸
  // :root 的 (0,1,0)、与源码位置无关（R9-19 / I-C4-04）。
  const rootMatch = (prelude) => {
    let best = null;
    for (const frag of splitTopLevel(prelude, ",")) {
      const comps = parseCompound(frag.trim());
      if (!comps || !isRootCompound(comps)) continue;
      const spec = compoundSpec(comps);
      if (!best || specCmp(spec, best.spec) > 0) {
        best = { spec, text: frag.trim() };
      }
    }
    return best;
  };
  // ── 恒真条件求值（R9-16 / I-C4-01）──────────────────────
  // 三值语义：true＝在面板渲染语境（屏幕介质、常规视口）下恒
  // 成立；false＝恒不成立；null＝取决于运行时环境、静态不可
  // 判。只有 true 的条件块按无条件走查；false 与 null 整棵排
  // 除（null 排除是既有口径——不可判面不入集，与选择器的不可
  // 判分片同例）。恒真形全集：@media 的 all/screen 介质、not
  // 对恒假式的否定、零下限特征（min-width: 0 等）、逗号列表任
  // 一恒真、and/or 组合；@supports 的恒真布尔式（白名单长青
  // 属性的合法声明原子及其 not/and/or 组合）。
  const zeroLength = (v) => {
    const m = /^(-?\d+(?:\.\d+)?)([a-z%]*)$/i.exec(v.trim());
    return m !== null && parseFloat(m[1]) === 0;
  };
  const posLength = (v) => {
    const m = /^(-?\d+(?:\.\d+)?)([a-z%]*)$/i.exec(v.trim());
    return m !== null && parseFloat(m[1]) > 0;
  };
  const evalMediaFeature = (raw) => {
    const content = raw.trim();
    if (content.startsWith("(") && content.endsWith(")")) {
      return evalMediaFeature(content.slice(1, -1));
    }
    const cmp = /^([a-z-]+)\s*(<=|>=|<|>|=)\s*(\S+)$/i.exec(content) ||
      /^(\S+)\s*(<=|>=|<|>|=)\s*([a-z-]+)$/i.exec(content);
    if (cmp) {
      // 区间语法（width >= 0px 等）：只对「与 0 比较」定值。
      const nameFirst = /^[a-z-]+$/i.test(cmp[1]);
      const name = (nameFirst ? cmp[1] : cmp[3]).toLowerCase();
      const op = nameFirst
        ? cmp[2]
        : { "<": ">", ">": "<", "<=": ">=", ">=": "<=", "=": "=" }[cmp[2]];
      const val = nameFirst ? cmp[3] : cmp[1];
      if (name !== "width" && name !== "height") return null;
      if (zeroLength(val)) {
        // 视口宽高恒 > 0（渲染语境）。
        return op === ">=" || op === ">" ? true : false;
      }
      return null;
    }
    const decl = /^([a-z-]+)\s*:\s*([\s\S]+)$/.exec(content);
    if (!decl) {
      // 布尔上下文特征：(width)/(height) 在真实视口恒非零。
      const name = content.toLowerCase();
      if (name === "width" || name === "height") return true;
      if (/^[a-z-]+$/.test(name)) return false; // 未知特征名恒假（规范）
      return null;
    }
    const name = decl[1].toLowerCase();
    const value = decl[2].trim();
    const base = name.replace(/^(min|max)-/, "");
    const isMin = name.startsWith("min-");
    const isMax = name.startsWith("max-");
    if (base === "width" || base === "height" || base === "resolution") {
      if (isMin) return zeroLength(value) ? true : posLength(value) ? null : false;
      if (isMax) return zeroLength(value) ? false : posLength(value) ? null : false;
      return zeroLength(value) ? false : posLength(value) ? null : false;
    }
    if (
      /^(hover|pointer|any-hover|any-pointer|orientation|prefers-|color|color-index|monochrome|grid|scan|update|overflow-|scripting|inverted-colors|forced-colors|display-mode|aspect-ratio|min-aspect-ratio|max-aspect-ratio|device-)/.test(
        name,
      )
    ) {
      return null; // 已知但取决于设备/用户环境
    }
    return false; // 未知特征名：按规范整条查询恒假
  };
  // 把 media 查询在顶层拆为 ( ) 组与骨架词，组以占位符回填。
  const evalMediaQuery = (query) => {
    const groups = [];
    let skeleton = "";
    let i = 0;
    const n = query.length;
    while (i < n) {
      if (query[i] === "(") {
        let d = 0;
        let q = null;
        const start = i;
        while (i < n) {
          const c = query[i];
          if (q) {
            if (c === "\\") i++;
            else if (c === q) q = null;
            i++;
            continue;
          }
          if (c === '"' || c === "'") { q = c; i++; continue; }
          if (c === "(") d++;
          else if (c === ")") {
            d--;
            if (d === 0) {
              i++;
              break;
            }
          }
          i++;
        }
        groups.push(query.slice(start + 1, i - 1));
        skeleton += ` \u0001${groups.length - 1}\u0002 `;
        continue;
      }
      skeleton += query[i];
      i++;
    }
    const words = skeleton.trim().split(/\s+/).filter(Boolean);
    let pos = 0;
    let negate = false;
    if (/^(only|not)$/i.test(words[0] ?? "")) {
      negate = words[0].toLowerCase() === "not";
      pos = 1;
    }
    // 原子序：[介质类型词?]（组占位符），以 and/or 连接。
    const atoms = [];
    const ops = [];
    let expectAtom = true;
    for (; pos < words.length; pos++) {
      const w = words[pos];
      const ph = /^\u0001(\d+)\u0002$/.exec(w);
      if (ph) {
        if (!expectAtom) return null;
        atoms.push(evalMediaFeature(groups[Number(ph[1])]));
        expectAtom = false;
        continue;
      }
      const lw = w.toLowerCase();
      if (lw === "and" || lw === "or") {
        if (expectAtom) return null;
        ops.push(lw);
        expectAtom = true;
        continue;
      }
      if (/^[a-z][a-z0-9-]*$/i.test(w)) {
        if (!expectAtom || atoms.length > 0) return null; // 类型词只能居首
        // 屏幕渲染语境：all/screen 恒真，print/speech 恒假，未
        // 知介质类型按规范恒不匹配。
        atoms.push(lw === "all" || lw === "screen" ? true : false);
        expectAtom = false;
        continue;
      }
      return null;
    }
    if (atoms.length === 0 || expectAtom) return null;
    // and 优先于 or：先折 and 链、再折 or。
    const orTerms = [];
    let cur = atoms[0];
    for (let k = 0; k < ops.length; k++) {
      const rhs = atoms[k + 1];
      if (ops[k] === "and") {
        cur = cur === false || rhs === false ? false : cur === null || rhs === null ? null : true;
      } else {
        orTerms.push(cur);
        cur = rhs;
      }
    }
    orTerms.push(cur);
    let v = orTerms.some((t) => t === true)
      ? true
      : orTerms.every((t) => t === false)
        ? false
        : null;
    if (negate && v !== null) v = !v;
    return v;
  };
  const evalMediaList = (text) => {
    const queries = splitTopLevel(text, ",").map((s) => s.trim());
    if (queries.length === 1 && queries[0] === "") return true; // 空列表＝all
    let anyUnknown = false;
    for (const q of queries) {
      const v = evalMediaQuery(q);
      if (v === true) return true;
      if (v === null) anyUnknown = true;
    }
    return anyUnknown ? null : false;
  };
  // @supports：白名单长青属性的声明原子（display 另验值在其关
  // 键字集内）、自定义属性原子恒真；未知属性与 selector() 等
  // 原子不可判（null）。原子以 not/and/or 与括号组合、and 优
  // 先于 or。
  const SUPPORTS_PROPS = new Set([
    "display", "position", "visibility", "color", "background",
    "background-color", "font-size", "font-family", "font-weight",
    "line-height", "width", "height", "min-width", "min-height",
    "max-width", "max-height", "margin", "padding", "border",
    "content", "float", "clear", "opacity", "transform",
    "transition", "overflow", "cursor", "text-align",
    "vertical-align", "box-sizing", "flex", "flex-direction",
    "justify-content", "align-items", "gap", "grid", "z-index",
    "top", "left", "right", "bottom",
  ]);
  const DISPLAY_VALUES = new Set([
    "block", "inline", "inline-block", "flex", "inline-flex",
    "grid", "inline-grid", "none", "contents", "flow-root",
    "table", "inline-table", "table-row", "table-cell",
    "list-item",
  ]);
  const evalSupportsAtom = (inner) => {
    const decl = /^(--[-_a-zA-Z0-9]+|[-_a-zA-Z][-_a-zA-Z0-9]*)\s*:\s*([\s\S]+)$/.exec(
      inner.trim(),
    );
    if (!decl) return null;
    const prop = decl[1].toLowerCase();
    const value = decl[2].trim().toLowerCase();
    if (prop.startsWith("--")) return true;
    if (!SUPPORTS_PROPS.has(prop)) return null;
    if (prop === "display") {
      return DISPLAY_VALUES.has(value.split(/\s+/)[0]) ? true : false;
    }
    return value !== "" ? true : false;
  };
  const evalSupports = (text) => {
    // 词法：括号组递归下降（声明原子＝不含顶层 and/or/not 的括
    // 号组）、and/or/not 关键字。
    const parseInner = (s) => {
      const toks = [];
      let i = 0;
      const n = s.length;
      while (i < n) {
        const c = s[i];
        if (/\s/.test(c)) { i++; continue; }
        if (c === "(") {
          let d = 0;
          let q = null;
          const start = i;
          let closed = false;
          while (i < n) {
            const ch = s[i];
            if (q) {
              if (ch === "\\") i++;
              else if (ch === q) q = null;
              i++;
              continue;
            }
            if (ch === '"' || ch === "'") { q = ch; i++; continue; }
            if (ch === "(") d++;
            else if (ch === ")") {
              d--;
              if (d === 0) {
                i++;
                closed = true;
                break;
              }
            }
            i++;
          }
          if (!closed) return null;
          toks.push({ group: s.slice(start + 1, i - 1) });
          continue;
        }
        const m = /^[a-zA-Z-]+/.exec(s.slice(i));
        if (m) {
          toks.push({ word: m[0].toLowerCase() });
          i += m[0].length;
          continue;
        }
        return null;
      }
      return toks;
    };
    const evalTokens = (toks) => {
      let pos = 0;
      const parseUnary = () => {
        const t = toks[pos];
        if (!t) return null;
        if (t.word === "not") {
          pos++;
          const v = parseUnary();
          return v === null ? null : !v;
        }
        if (t.group !== undefined) {
          pos++;
          const inner = t.group;
          // 声明原子（含冒号且无顶层布尔词）先判，否则递归。
          if (!/\b(and|or|not)\b/.test(inner)) {
            const atom = evalSupportsAtom(inner);
            if (atom !== null) return atom;
            if (/:/.test(inner)) return null; // 声明形但属性不可判
          }
          const sub = parseInner(inner);
          if (!sub) return null;
          return evalTokens(sub);
        }
        return null;
      };
      const parseAnd = () => {
        let v = parseUnary();
        while (toks[pos] && toks[pos].word === "and") {
          pos++;
          const rhs = parseUnary();
          v = v === false || rhs === false ? false : v === null || rhs === null ? null : true;
        }
        return v;
      };
      let v = parseAnd();
      while (toks[pos] && toks[pos].word === "or") {
        pos++;
        const rhs = parseAnd();
        v = v === true || rhs === true ? true : v === null || rhs === null ? null : false;
      }
      return pos === toks.length ? v : null;
    };
    const toks = parseInner(text.trim());
    if (!toks || toks.length === 0) return null;
    return evalTokens(toks);
  };
  // 条件 at-rule 的处置：@media/@supports 恒真才走查（上下文不
  // 变——条件不改变选择器/层语境）；其余块级 at-rule（@keyframes
  // /@font-face/@container/未知）整棵排除（既有口径）。
  const condRuleWalks = (prelude) => {
    const m = /^@([a-zA-Z-]+)/.exec(prelude);
    if (!m) return false;
    const name = m[1].toLowerCase();
    const rest = prelude.slice(m[0].length).trim();
    if (name === "media") return evalMediaList(rest) === true;
    if (name === "supports") return evalSupports(rest) === true;
    return false;
  };
  // 单条规则按上下文分发。frames 记 at-rule 与祖先选择器上下文：
  // 存在 type:"rule" 祖先时，内层 :root 的有效选择器是「祖先
  // :root」——根元素不可能是任何元素的后代，恒不匹配，不计数，
  // 仅继续遍历（R8-03 / H-C4-02）。
  const dispatchRule = (prelude, body, frames) => {
    if (/^@layer\b/i.test(prelude)) {
      const names = prelude.replace(/^@layer\b/i, "").trim();
      if (names === "") {
        // 匿名层分配合成层名参与层序（R8-02 / H-C4-01）。
        walk(body, [...frames, { type: "layer", name: freshAnonLayer() }]);
        return;
      }
      const parts = splitTopLevel(names, ",")
        .map((s) => s.trim())
        .filter((s) => s !== "");
      if (parts.length !== 1 || !isValidLayerName(parts[0])) {
        // 具名块只许单名；名单/非法名在浏览器中整块丢弃。
        problems.push(
          `@layer 规则块层名 "${names}" 不是合法单层名，浏览器会整条丢弃该规则块`,
        );
        return;
      }
      noteLayers(parts[0]);
      walk(body, [...frames, { type: "layer", name: parts[0] }]);
    } else if (prelude.startsWith("@")) {
      // 恒真条件块走查、余者整棵排除（R9-16 / I-C4-01）。
      if (condRuleWalks(prelude)) walk(body, frames);
      return;
    } else {
      if (frames.some((f) => f.type === "cond")) return;
      const inner = [...frames, { type: "rule" }];
      if (frames.some((f) => f.type === "rule")) {
        walk(body, inner);
        return;
      }
      const layerFrame = [...frames]
        .reverse()
        .find((f) => f.type === "layer");
      const match = rootMatch(prelude);
      if (match) {
        parseDecls(body, layerFrame ? layerFrame.name : null, frames, match);
      } else {
        walk(body, inner);
      }
    }
  };
  // 解析一个根规则体（R8-03 / H-C4-02 起按花括号深度切分）：
  // 声明以 ";" 收尾；若先遇 "{"，此前分片是嵌套规则的 prelude，
  // 整块交回分发按上下文处置——嵌套条件块内第二位起的声明不再
  // 被当作无条件声明逐片计数。match 是本规则命中根元素的
  // {spec, text}：嵌套 "&" 即父选择器本身、"&:root" 展开为
  // 「父选择器＋:root」的复合（:root:root 特异度 (0,2,0) 恒
  // 胜，R9-20 / I-C4-05），展开后按同一复合判定处置。
  const parseDecls = (body, layerName, frames, match) => {
    let i = 0;
    const n = body.length;
    while (i < n) {
      let paren = 0;
      let q = null;
      const start = i;
      let kind = null;
      while (i < n) {
        const c = body[i];
        if (q) {
          if (c === "\\") i++;
          else if (c === q) q = null;
          i++;
          continue;
        }
        if (c === '"' || c === "'") { q = c; i++; continue; }
        if (c === "(") paren++;
        else if (c === ")") paren--;
        else if (paren === 0 && c === ";") { kind = "decl"; break; }
        else if (paren === 0 && c === "{") { kind = "rule"; break; }
        i++;
      }
      if (kind === null) {
        applyDecl(body.slice(start).trim(), layerName, match.spec);
        break;
      }
      if (kind === "decl") {
        applyDecl(body.slice(start, i).trim(), layerName, match.spec);
        i++;
        continue;
      }
      const prelude = body.slice(start, i).trim();
      const blk = extractBlock(body, i);
      if (!blk) break;
      i = blk.next;
      if (prelude === "&") {
        parseDecls(blk.body, layerName, frames, match);
      } else if (prelude.startsWith("&")) {
        const expanded = match.text + prelude.slice(1);
        const comps = parseCompound(expanded);
        if (comps && isRootCompound(comps)) {
          parseDecls(blk.body, layerName, frames, {
            spec: compoundSpec(comps),
            text: expanded,
          });
        } else {
          dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
        }
      } else if (prelude.startsWith("@")) {
        if (/^@layer\b/i.test(prelude)) {
          dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
        } else if (condRuleWalks(prelude)) {
          // 恒真条件不改变选择器语境：仍按本规则体解析。
          parseDecls(blk.body, layerName, frames, match);
        }
        // 余者（恒假/不可判条件、其他 at-rule）：整块跳过。
      } else {
        dispatchRule(prelude, blk.body, [...frames, { type: "rule" }]);
      }
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
      const blk = extractBlock(text, i - 1);
      if (!blk) break;
      i = blk.next;
      dispatchRule(prelude, blk.body, frames);
    }
  };
  walk(code, []);
  const out = {};
  for (const [k, v] of winners) out[k] = v.value;
  return { tokens: out, problems };
};

const globalsSrc = stripCssComments(readSrc("src/styles/globals.css"));
const { tokens: globalsResolved, problems: evalProblems } =
  resolveRootTokens(globalsSrc);
const globalsTokens = {};
for (const [k, v] of Object.entries(globalsResolved)) {
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


// 级联求值建模问题（R9-17 的 fail-closed 通道）：求值中记录的
// 非法层名等问题在两门逐个点名报红，不并入 token 胜出判定。
for (const p of evalProblems) {
  check("globals.css 级联求值建模问题", false, p);
}

// ── 求值器钉格（修复轮 9，R9-16～R9-20）────────────────────
// 以合成 CSS 直接实测 resolveRootTokens 的级联语义，逐格对应
// 五条立案的浏览器真值（构造原文：I-C4 §2、VERIFY8-A §2）。
// 任一格回归即点名报红；对照格钉住排除方向（不可判面不入集、
// 静止态不计状态伪类、祖先内 :root 恒不匹配）防过度入集回归。
const fxResolve = (cssText) => resolveRootTokens(stripCssComments(cssText));
const FX_BASE = ":root { --fx-probe: #111111; }";
const fxVal = (cssText) => fxResolve(cssText).tokens["--fx-probe"];

check(
  "求值器钉格：@media all 尾置坏值按恒真走查胜出（R9-16）",
  fxVal(`${FX_BASE}\n@media all { :root { --fx-probe: #222222; } }`) ===
    "#222222",
  `实得 ${fxVal(`${FX_BASE}\n@media all { :root { --fx-probe: #222222; } }`)}`,
);
check(
  "求值器钉格：@media all 存档好值胜出、顶层坏值不假红（R9-16）",
  fxVal(
    ":root { --fx-probe: #222222; }\n@media all { :root { --fx-probe: #111111; } }",
  ) === "#111111",
);
check(
  "求值器钉格：@media screen 与 (min-width: 0px) 恒真走查（R9-16）",
  fxVal(`${FX_BASE}\n@media screen { :root { --fx-probe: #222222; } }`) ===
    "#222222" &&
    fxVal(
      `${FX_BASE}\n@media (min-width: 0px) { :root { --fx-probe: #222222; } }`,
    ) === "#222222",
);
check(
  "求值器钉格：@media print 恒假排除、视口条件不可判维持排除（R9-16 对照）",
  fxVal(`${FX_BASE}\n@media print { :root { --fx-probe: #222222; } }`) ===
    "#111111" &&
    fxVal(
      `${FX_BASE}\n@media (max-width: 768px) { :root { --fx-probe: #222222; } }`,
    ) === "#111111",
);
check(
  "求值器钉格：@supports (display: block) 恒真走查、未知属性不可判排除（R9-16）",
  fxVal(
    `${FX_BASE}\n@supports (display: block) { :root { --fx-probe: #222222; } }`,
  ) === "#222222" &&
    fxVal(
      `${FX_BASE}\n@supports (made-up-prop: 1) { :root { --fx-probe: #222222; } }`,
    ) === "#111111",
);
check(
  "求值器钉格：:root 内嵌 @media all 仍按本规则体走查（R9-16）",
  fxVal(`${FX_BASE}\n:root { @media all { --fx-probe: #222222; } }`) ===
    "#222222",
);
const fxCollision = fxResolve(`@layer (anon-layer-1);\n${FX_BASE}`);
check(
  "求值器钉格：预登记非法层名记为建模问题且不入层序（R9-17）",
  fxCollision.problems.some((p) => p.includes("(anon-layer-1)")) &&
    fxCollision.tokens["--fx-probe"] === "#111111",
  `problems=${JSON.stringify(fxCollision.problems)}`,
);
check(
  "求值器钉格：匿名层存档不胜无层出厂值（R8-02 对照）",
  fxVal(`${FX_BASE}\n@layer { :root { --fx-probe: #222222; } }`) === "#111111",
);
check(
  "求值器钉格：:ROOT 大写伪类按大小写不敏感计数（R9-18）",
  fxVal(`${FX_BASE}\n:ROOT { --fx-probe: #222222; }`) === "#222222",
);
check(
  "求值器钉格：html:root 特异度恒胜裸 :root、与位置无关（R9-19）",
  fxVal(`html:root { --fx-probe: #222222; }\n${FX_BASE}`) === "#222222" &&
    fxVal(`:root:not(.fx-off) { --fx-probe: #222222; }\n${FX_BASE}`) ===
      "#222222",
);
check(
  "求值器钉格：嵌套 &:root 展开 :root:root 恒胜（R9-20）",
  fxVal(`${FX_BASE}\n:root { &:root { --fx-probe: #222222; } }`) === "#222222",
);
check(
  "求值器钉格：&:hover 静止态不计、祖先内 :root 恒不匹配（对照）",
  fxVal(
    ":root { --fx-probe: #222222; }\n:root { &:hover { --fx-probe: #111111; } }",
  ) === "#222222" &&
    fxVal(`${FX_BASE}\n.fx-wrap { :root { --fx-probe: #222222; } }`) ===
      "#111111",
);

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
