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

// ── ① src 内 classList.add 不得带 'dark' ─────────────────────────────
const walk = (dir) => {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...walk(p));
    else if (/\.(ts|tsx|js|jsx)$/.test(name)) out.push(p);
  }
  return out;
};

const addOffenders = [];
let addCallCount = 0;
for (const file of walk(srcDir)) {
  const text = readFileSync(file, "utf8");
  for (const m of text.matchAll(/classList\.add\s*\(([^)]*)\)/g)) {
    addCallCount++;
    const args = m[1];
    // 取出每个字符串字面量，按空白切 token，精确匹配 'dark'。
    const literals = [...args.matchAll(/["'`]([^"'`]*)["'`]/g)].map(
      (x) => x[1],
    );
    const hasDark = literals.some((lit) =>
      lit.split(/\s+/).includes("dark"),
    );
    if (hasDark) {
      addOffenders.push(
        `${file.slice(frontendDir.length + 1)}: classList.add(${args.trim()})`,
      );
    }
  }
}
check(
  `① src 内 classList.add 不含 'dark'（实扫 ${addCallCount} 处调用）`,
  addOffenders.length === 0,
  addOffenders.join("; "),
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
