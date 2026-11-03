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
//      函数体内每条 return 必须恒返回 "light"（文本钉）；
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

// ── ① src 内 classList.add / toggle 不得带 'dark' ────────────────────
const walk = (dir) => {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else if (/\.(ts|tsx|js|jsx)$/.test(name)) out.push(p);
  }
  return out;
};

// 括号配平截取一个调用的实参区：text[openIdx] 必须是 '('。
// 返回 { literals, firstArgLiterals, end }：literals 为实参区内全部
// 字符串字面量的静态文本（模板字面量按 ${} 切段、表达式内字符串亦
// 收集）；firstArgLiterals 为第一顶层逗号之前的子集；end 为匹配
// ')' 之后的下标。
const extractCallLiterals = (text, openIdx) => {
  const literals = [];
  const firstArgLiterals = [];
  const n = text.length;
  let topCommaSeen = false;
  const pushLit = (s) => {
    literals.push(s);
    if (!topCommaSeen) firstArgLiterals.push(s);
  };
  // 扫描模板字面量 ${ ... } 内部：i 指向 '{' 后一字，返回匹配 '}' 后下标。
  const scanTemplateExpr = (i) => {
    let braceDepth = 1;
    while (i < n && braceDepth > 0) {
      const c = text[i];
      if (c === '"' || c === "'" || c === "`") {
        i = scanString(i, c);
        continue;
      }
      if (c === "/" && text[i + 1] === "/") {
        const e = text.indexOf("\n", i);
        i = e < 0 ? n : e + 1;
        continue;
      }
      if (c === "/" && text[i + 1] === "*") {
        const e = text.indexOf("*/", i + 2);
        i = e < 0 ? n : e + 2;
        continue;
      }
      if (c === "{") braceDepth++;
      else if (c === "}") {
        braceDepth--;
        if (braceDepth === 0) return i + 1;
      }
      i++;
    }
    return i;
  };
  // 扫描一个字符串字面量：i 指向开引号，返回闭引号后的下标。
  const scanString = (i, quote) => {
    let buf = "";
    i++;
    while (i < n) {
      const c = text[i];
      if (c === "\\") {
        buf += text[i + 1] ?? "";
        i += 2;
        continue;
      }
      if (quote === "`" && c === "$" && text[i + 1] === "{") {
        pushLit(buf);
        buf = "";
        i = scanTemplateExpr(i + 2);
        continue;
      }
      if (c === quote) {
        pushLit(buf);
        return i + 1;
      }
      buf += c;
      i++;
    }
    pushLit(buf);
    return i;
  };
  let depth = 0;
  let i = openIdx;
  while (i < n) {
    const c = text[i];
    if (c === '"' || c === "'" || c === "`") {
      i = scanString(i, c);
      continue;
    }
    if (c === "/" && text[i + 1] === "/") {
      const e = text.indexOf("\n", i);
      i = e < 0 ? n : e + 1;
      continue;
    }
    if (c === "/" && text[i + 1] === "*") {
      const e = text.indexOf("*/", i + 2);
      i = e < 0 ? n : e + 2;
      continue;
    }
    if (c === "(") depth++;
    else if (c === ")") {
      depth--;
      i++;
      if (depth === 0) break;
      continue;
    } else if (c === "," && depth === 1) {
      topCommaSeen = true;
    }
    i++;
  }
  return { literals, firstArgLiterals, end: i };
};

const darkOffenders = [];
let addCallCount = 0;
let toggleCallCount = 0;
for (const file of walk(srcDir)) {
  const text = readFileSync(file, "utf8");
  for (const m of text.matchAll(/classList\s*\??\.\s*(add|toggle)\s*\(/g)) {
    const method = m[1];
    if (method === "add") addCallCount++;
    else toggleCallCount++;
    const { literals, firstArgLiterals, end } = extractCallLiterals(
      text,
      m.index + m[0].length - 1,
    );
    // toggle 的类名只在第一实参；add 的每个实参都是类名。
    const pool = method === "toggle" ? firstArgLiterals : literals;
    // 字符串字面量按空白切 token，精确匹配 'dark'。
    const hasDark = pool.some((lit) => lit.split(/\s+/).includes("dark"));
    if (hasDark) {
      const callText = text
        .slice(m.index, end)
        .replace(/\s+/g, " ")
        .slice(0, 100);
      darkOffenders.push(
        `${file.slice(frontendDir.length + 1)}: ${callText}`,
      );
    }
  }
}
check(
  `① src 内 classList.add/toggle 不含 'dark'（实扫 add ${addCallCount} 处、toggle ${toggleCallCount} 处调用）`,
  darkOffenders.length === 0,
  darkOffenders.join("; "),
);

// ── ② registry 两函数恒返回 "light" ──────────────────────────────────
const registryPath = join(srcDir, "themes/registry.ts");
let registry = "";
try {
  registry = readFileSync(registryPath, "utf8");
} catch {
  check("② themes/registry.ts 可读", false, "文件不存在");
}

const fnBody = (src, name) => {
  const start = src.indexOf(`function ${name}`);
  if (start < 0) return null;
  const open = src.indexOf("{", start);
  if (open < 0) return null;
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") {
      depth--;
      if (depth === 0) return src.slice(open + 1, i);
    }
  }
  return null;
};

for (const fn of ["resolveSystemMode", "getEffectiveMode"]) {
  const body = registry ? fnBody(registry, fn) : null;
  if (body === null) {
    check(`② ${fn} 存在`, false, "未找到函数定义");
    continue;
  }
  const returns = [...body.matchAll(/return\s+([^;]+);/g)].map((m) =>
    m[1].trim(),
  );
  const allLight =
    returns.length > 0 &&
    returns.every((v) => v === '"light"' || v === "'light'");
  check(
    `② ${fn} 每条 return 恒为 "light"（实测 ${returns.length} 条）`,
    allLight,
    `return 值: ${returns.join(", ") || "（无）"}`,
  );
}

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
    /classList\.remove\(\s*['"]dark['"]\s*\)/.test(indexHtml),
    "引导脚本未清除 dark 类",
  );
}

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\n深色模式防复活门：三项断言全过");
