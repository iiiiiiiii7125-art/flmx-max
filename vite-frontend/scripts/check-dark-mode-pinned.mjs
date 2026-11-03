#!/usr/bin/env node
// check-dark-mode-pinned.mjs — 深色模式删除防复活门（R2-025 / B-ui-02）。
//
// 全站统一亮色、深色模式已删是用户定案级 invariant，但此前只靠代码
// 约定维持：src 里残留 138 处 dark: 工具类、index.html 里 4 块
// html.dark 纯 CSS（含 html.dark body 黑底，一旦触发整页黑屏）、主题
// 包 tokens.dark 回退——任何一处重新引入「给根元素加 .dark」的路径
// 都会静默把它们全部重新武装。本门把惰性证据链里最关键的三环钉死：
//   ① src 内任何 classList.add(...) 的参数不得含 'dark' 类名 token；
//   ② themes/registry.ts 的 getEffectiveMode / resolveSystemMode
//      恒返回 "light"（行为断言：转译执行、三种环境变体实测）；
//   ③ index.html 引导脚本必须含 classList.remove('dark')。
// 断言范围只此三项：dark: 工具类与 html.dark CSS 的存量清理是另一条
// 线（R2-027），本门不据此判红。
//
// C4-01 拓宽（修复轮 3）：断言①的旧实现以正则 [^)]* 截取实参串——首
// 实在参为函数调用时其后的 'dark' 落在截取之外不可见，且等价再武装
// 形态 classList.toggle('dark', ...) 整体不在扫描面（第三轮审计变异
// 探针 M2/M3 实证双双 exit 0 绕过）。现实现：
//   - 扫描面扩为 classList.add 与 classList.toggle 两方法；
//   - 实参截取改括号配平扫描：跟踪括号深度、字符串（含模板字面量及
//     其 ${} 表达式，内部字符串同样收集）与注释，嵌套实参不再截断；
//   - toggle 只判第一实参（第二实参是 force 布尔、不是类名）；
//   - 口径宁严勿漏：实参区内（含嵌套表达式）出现的 'dark' token 一律
//     判红，与仓内文本钉族 fail-closed 取舍一致。
// 变异钉死形态：add("dark")、add(f(), "dark")、add(`x ${"dark"}`)、
// toggle("dark", true)、classList 断行 .add("dark")、
// classList?.add("dark") 必须红；toggle("has-...", cond) 等现行形态与
// 'darkness' 等非精确 token 必须绿。
//
// 已知边界（D-C4-01，第四轮审计登记接受，修复轮 4 落字）：断言①的
// 扫描器只看得见字面量形态——非字面量构造一族（变量间接传入、字面
// 量拼接、Unicode 转义、括号记法、给 classList 起别名）均可绕过本
// 门，第四轮 5 形态探针实证 exit 0。此族全为有意规避型构造（不是
// 顺手写出的普通形态），且其复活载荷已被 R2-027 大幅解除（html.dark
// CSS 与 dark: 工具类存量已清零、主题注册表恒 light、引导脚本恒
// remove('dark')，后两环由断言②③钉住）——单靠绕过本门无法静默复
// 活深色。故此边界登记为已知取舍，本门不扩扫描面去追有意规避；本
// 门的职责是钉死无意/顺手形态的重新引入。
//
// R5-06 收口（第五轮审计 E-C4-02，修复轮 5）：上段登记的是非字面量
// 构造一族；其后发现的「调用点模式中断」子族——classList 与方法名
// 之间被空白/换行或 ?. 隔开（classList 断行 .add、classList?.add）
// ——'dark' 字面量完整在场、属顺手写出的普通形态而非构造规避，旧
// 调用点正则要求两端相邻而整体漏扫（探针实证 exit 0）。现正则放宽
// 为 classList\s*\??\.\s*(add|toggle)\s*\( 收口，两形态已并入上方
// 变异钉死形态；非字面量构造一族维持上段登记接受不变。
//
// R6 收口（第六轮审计 R6-C4-02/03/04，修复轮 6）：中断子族冒出第六
// 形态——注释中断（classList/**/.add、classList /*c*/ .add），放宽
// 正则的分隔符类不含注释、实扫仍报 0；断言②的 fnBody 朴素大括号
// 配平会被注释内的 } 截断、假 return 蒙混；断言③可被 index.html
// 的 HTML 注释拷贝满足。三处共同根因是提取面无注释意识，本轮按
// 机制迁移：断言①②的扫描对象先经词法剥注释（字符串保留、行号
// 不变），注释中断在剥离后还原为普通分隔、注释拷贝整体消失；
// 断言②的函数体截取改字符串/模板感知的配平扫描；断言③先剥
// HTML 注释再判定。变异钉死形态新增：classList/**/.add("dark")、
// classList /*c*/ .toggle("dark")、getEffectiveMode 体内含 } 的
// 示例注释＋真身 return "dark"、index.html 引导脚本改坏＋HTML
// 注释存档原文——四者必须红（修复轮 6 探针实证，见
// fix6-reports/gatesmech.md）。
//
// R7 收口（第七轮审计 R7-23/24，修复轮 7）：轮 6 的注释剥离器不
// 识正则字面量——任一被扫文件里出现 /[/*]/ 形正则，剥离器即误入
// 块注释态、吞掉其后的真实 add 调用（PROBE3a 实证「实扫 add 0
// 处」全绿）；断言②的 fnBody 以 indexOf("function "+名) 在保留
// 字符串内容的剥离文本上定位，注册表前的模板常量里放一份假签
// 名即可劫持锚点、真身 return "dark" 蒙混（PROBE3b 实证）。两
// 处的共同根因仍是「发现/定位跑在词法近似文本上」。本轮按机制
// 迁移：断言①改 TS 编译器 API 的 AST 调用点发现（callee 为
// *.classList 的 add/toggle 调用表达式，实参字面量按 AST 已求
// 值文本收集——正则字面量只是一个普通节点、再不能污染发现，
// 字符串/注释里的调用样式文本也不再是调用点）；断言②改行为断
// 言（转译 registry 后在存储为空/存储 dark/存储 system＋系统
// 偏好深色三种环境变体下实测两函数返回值恒 "light"，与主题门
// 同执行面）。词法剥离器与 fnBody 截取随之退役；D-C4-01 登记
// 的非字面量构造一族（变量间接、拼接、括号记法、别名）维持已
// 知取舍不变——但 Unicode 转义写出的字面量因 AST 取已求值文本
// 自本轮起落网，属该族内的局部收紧。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-dark-mode-pinned.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
// 本脚本由 run-checks.mjs 按目录自动枚举接线，无需另行登记。
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const srcDir = join(frontendDir, "src");

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

import { createRequire } from "node:module";

const require = createRequire(import.meta.url);

let ts;
try {
  ts = require("typescript");
} catch {
  // worktree 尚未 pnpm install 时，退回主仓的 node_modules。
  ts = require(
    "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
  );
}

// ── ① src 内 classList.add / toggle 不得带 'dark'（AST 发现） ──────
const walk = (dir) => {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else if (/\.(ts|tsx|js|jsx)$/.test(name)) out.push(p);
  }
  return out;
};

const scriptKindFor = (path) =>
  path.endsWith(".tsx")
    ? ts.ScriptKind.TSX
    : path.endsWith(".ts")
      ? ts.ScriptKind.TS
      : path.endsWith(".jsx")
        ? ts.ScriptKind.JSX
        : ts.ScriptKind.JS;

// 收集一个表达式子树内的全部字面量静态文本：字符串、无插值模板、
// 模板的头/中/尾段（${} 内表达式递归收集），与旧实参区扫描同口
// 径。AST 字面量的 .text 是已求值文本——Unicode 转义写出的
// 'dark' 同样落网（旧文本扫描的盲区，方向为 fail-closed 收紧）。
const collectLiterals = (node, out) => {
  if (
    ts.isStringLiteral(node) ||
    ts.isNoSubstitutionTemplateLiteral(node) ||
    ts.isTemplateHead(node) ||
    ts.isTemplateMiddle(node) ||
    ts.isTemplateTail(node)
  ) {
    out.push(node.text);
    return;
  }
  ts.forEachChild(node, (c) => collectLiterals(c, out));
};

const darkOffenders = [];
let addCallCount = 0;
let toggleCallCount = 0;
for (const file of walk(srcDir)) {
  const text = readFileSync(file, "utf8");
  const sf = ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.ES2020,
    true,
    scriptKindFor(file),
  );
  const visit = (node) => {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression)
    ) {
      const method = node.expression.name.text;
      if (method === "add" || method === "toggle") {
        const objText = node.expression.expression.getText();
        if (/(^|\.)classList$/.test(objText)) {
          if (method === "add") addCallCount++;
          else toggleCallCount++;
          // toggle 的类名只在第一实参；add 的每个实参都是类名。
          const args =
            method === "toggle"
              ? node.arguments.slice(0, 1)
              : [...node.arguments];
          const lits = [];
          for (const a of args) collectLiterals(a, lits);
          if (lits.some((lit) => lit.split(/\s+/).includes("dark"))) {
            darkOffenders.push(
              `${file.slice(frontendDir.length + 1)}: ${node
                .getText()
                .replace(/\s+/g, " ")
                .slice(0, 100)}`,
            );
          }
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}
check(
  `① src 内 classList.add/toggle 不含 'dark'（实扫 add ${addCallCount} 处、toggle ${toggleCallCount} 处调用）`,
  darkOffenders.length === 0,
  darkOffenders.join("; "),
);

// ── ② registry 两函数恒返回 "light"（行为断言，R7-24） ────────────
const registryPath = join(srcDir, "themes/registry.ts");
let registrySrc = "";
try {
  registrySrc = readFileSync(registryPath, "utf8");
} catch {
  check("② themes/registry.ts 可读", false, "文件不存在");
}
if (registrySrc) {
  const { outputText } = ts.transpileModule(registrySrc, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2020,
    },
  });
  const runtimeRequires = [
    ...outputText.matchAll(/require\(["']([^"']+)["']\)/g),
  ].map((m) => m[1]);
  check(
    "② registry 转译产物无运行时 require",
    runtimeRequires.length === 0,
    `发现 require: ${runtimeRequires.join(", ")}`,
  );
  if (runtimeRequires.length === 0) {
    // 环境变体：存储为空 / 存储遗留 "dark" / 存储 "system" 且系统
    // 偏好深色——三种环境下两函数都必须实测返回 "light"（存量
    // dark 偏好强制亮色是定案口径）。每变体以全新模块实例执行，
    // 模块级缓存不跨变体。
    const variants = [
      ["存储为空", () => null, false],
      ["存储为 dark", () => "dark", false],
      ["存储为 system 且系统偏好深色", () => "system", true],
    ];
    const results = { resolveSystemMode: [], getEffectiveMode: [] };
    let execError = "";
    for (const [, getItem, prefersDark] of variants) {
      globalThis.localStorage = {
        getItem,
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
      const matchMedia = () => ({ matches: prefersDark });
      globalThis.window = {
        matchMedia,
        localStorage: globalThis.localStorage,
      };
      globalThis.matchMedia = matchMedia;
      const mod = { exports: {} };
      try {
        new Function("module", "exports", "require", outputText)(
          mod,
          mod.exports,
          require,
        );
        for (const fn of Object.keys(results)) {
          results[fn].push(
            typeof mod.exports[fn] === "function"
              ? mod.exports[fn]()
              : "<导出缺失>",
          );
        }
      } catch (e) {
        execError = String(e);
        break;
      }
    }
    for (const fn of ["resolveSystemMode", "getEffectiveMode"]) {
      const vals = results[fn];
      check(
        `② ${fn} 行为实测恒为 "light"（3 种环境变体）`,
        !execError && vals.length === 3 && vals.every((v) => v === "light"),
        execError || `返回值: ${vals.join(", ") || "（无）"}`,
      );
    }
  }
}

// 剥 HTML 注释（换行保留），注释内的脚本拷贝不再构成满足点。
const stripHtmlComments = (src) =>
  src.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, " "));


// ── ③ index.html 引导脚本含 remove('dark') ───────────────────────────
let indexHtml = "";
try {
  indexHtml = readFileSync(join(frontendDir, "index.html"), "utf8");
} catch {
  check("③ index.html 可读", false, "文件不存在");
}
if (indexHtml) {
  check(
    "③ index.html 引导脚本含 classList.remove('dark')",
    /classList\.remove\(\s*['"]dark['"]\s*\)/.test(
      stripHtmlComments(indexHtml),
    ),
    "引导脚本未清除 dark 类",
  );
}

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\n深色模式防复活门：三项断言全过");