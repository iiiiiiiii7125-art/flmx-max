// check-contrast.mjs — 文字对比度回归校验（无测试框架版）。
//
// 定案纪律：全站素面亮色单模，正文文字用所属色族最深档（-700 起），
// 禁灰字，对比度目标 7、底线 4.5（WCAG AA 正文）。此前该自检只是
// 审计时的一次性临时脚本、未落仓库（A-front 复核时独立复算 25 对
// 全过），本脚本把它沉淀为常设门：解析 src/styles/globals.css 的
// :root token 实值，按 WCAG 相对亮度公式复算 25 对正文/托盘文字
// 对，逐对断言 ① 不低于 4.5 底线；② 与钉值偏差 ≤ 0.1——token 被
// 改浅（或改深到改变设计值）都会点名，改值须同步改钉值（有意变更
// 留痕）。主题包（themes/）是管理员主动启用的资产，不在本门范围；
// 本门守的是出厂默认 token。
//
// 提取面（修复轮 6，R6-C4-09 机制处置）：旧实现以整文件正则收集
// 声明、后声明胜出且不剥注释——把某 token 改浅后在文件尾以注释
// 存档旧值声明，门会按旧值算出全过（渲染实值对比度可已跌破
// 4.5）。现提取先词法剥除 CSS 注释，再只在 :root { } 块内按块内
// 顺序合并声明（后声明胜出，与级联同义），与头注「解析 :root
// token 实值」的口径一致；注释存档不再构成满足点。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-contrast.mjs
// 退出码 0 = 通过，非 0 = 失败（打印具体不一致的断言）。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(
  join(here, "..", "src/styles/globals.css"),
  "utf8",
);

// 词法剥除 CSS 注释：/* */ 逐字替换为空格（换行保留），字符串
// 内容原样保留。注释里的旧值存档声明在剥离后不复存在。
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

// 只取 :root { } 块（括号配平）内的声明，按文件顺序合并、后声明
// 胜出（与 CSS 级联同义）；块外声明与注释均不入集。
const tokens = {};
const code = stripCssComments(css);
for (const m of code.matchAll(/:root\s*\{/g)) {
  const open = m.index + m[0].length - 1;
  let depth = 0;
  let end = -1;
  for (let i = open; i < code.length; i++) {
    if (code[i] === "{") depth++;
    else if (code[i] === "}") {
      depth--;
      if (depth === 0) {
        end = i;
        break;
      }
    }
  }
  if (end < 0) continue;
  const block = code.slice(open + 1, end);
  for (const d of block.matchAll(/(--[a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    tokens[d[1]] = d[2].toLowerCase();
  }
}

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

const luminance = (hex) => {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;

    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  });

  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

const contrast = (fgHex, bgHex) => {
  const [hi, lo] = [luminance(fgHex), luminance(bgHex)].sort((a, b) => b - a);

  return (hi + 0.05) / (lo + 0.05);
};

// [前景 token, 背景 token, 钉值]——钉值由本脚本首跑实测并与 A-front
// 审计的独立复算逐项对齐（正文 17.72/16.97、次级 -700 10.44/10.01、
// primary-700 7.90、success/warning-700 5.02/4.81、danger-700
// 6.47/6.20、托盘六对 8.15–9.50）。
const PAIRS = [
  ["--foreground", "--content1", 17.72],
  ["--foreground", "--background", 16.97],
  ["--default-700", "--content1", 10.44],
  ["--default-700", "--background", 10.01],
  ["--default-800", "--content1", 14.89],
  ["--default-900", "--background", 16.97],
  ["--primary-700", "--content1", 7.9],
  ["--primary-700", "--background", 7.57],
  ["--primary-800", "--content1", 9.93],
  ["--secondary-700", "--content1", 7.1],
  ["--success-700", "--content1", 5.02],
  ["--success-700", "--background", 4.81],
  ["--warning-700", "--content1", 5.02],
  ["--warning-700", "--background", 4.81],
  ["--danger-700", "--content1", 6.47],
  ["--danger-700", "--background", 6.2],
  ["--tray-primary-foreground", "--tray-primary", 9.27],
  ["--tray-secondary-foreground", "--tray-secondary", 9.23],
  ["--tray-success-foreground", "--tray-success", 8.3],
  ["--tray-warning-foreground", "--tray-warning", 8.15],
  ["--tray-danger-foreground", "--tray-danger", 8.2],
  ["--tray-default-foreground", "--tray-default", 9.5],
  ["#ffffff", "--primary-800", 9.93],
  ["#ffffff", "--success-800", 7.13],
  ["#ffffff", "--danger-800", 8.31],
];

check("对比度基线恰 25 对", PAIRS.length === 25, `实际 ${PAIRS.length} 对`);

for (const [fg, bg, pinned] of PAIRS) {
  const fgHex = fg.startsWith("#") ? fg : tokens[fg];
  const bgHex = bg.startsWith("#") ? bg : tokens[bg];

  if (!fgHex || !bgHex) {
    check(`${fg} on ${bg}`, false, "token 未在 globals.css 中找到");

    continue;
  }
  const ratio = contrast(fgHex, bgHex);

  check(
    `${fg} on ${bg} = ${ratio.toFixed(2)}（钉值 ${pinned}、底线 4.5）`,
    ratio >= 4.5 && Math.abs(ratio - pinned) <= 0.1,
    `实测 ${ratio.toFixed(2)}`,
  );
}

if (failures.length > 0) {
  console.log(`\n${failures.length} 项断言失败`);
  process.exit(1);
}
console.log("\n全部断言通过");
