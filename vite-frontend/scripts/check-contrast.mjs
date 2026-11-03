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
// 级联条件口径（修复轮 7，R7-09 / G-C4-03 机制处置）：轮 6 的块合
// 并对块的上下文无条件——把改浅的新值写进无条件 :root、再在文件
// 尾追加一个永假 @media 块存档旧值 :root，后声明胜出仍按旧值算
// 全过，而真实渲染用的是新值。现提取改为级联求值：解析规则时追
// 踪 at-rule 上下文，仅无条件 :root（顶层、或仅被 @layer 包裹）
// 的声明入集；@media/@supports/@container 等条件块（含未知块级
// at-rule）整棵子树排除。胜出顺序按 CSS 级联：!important 层级
// 高于普通级；普通级中无层声明高于 @layer 内声明、层间按首次提
// 及顺序后者胜；同级按源码顺序后者胜。token 只存在于条件块时
// 视为未定义（fail-closed，断言点名缺失）。
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

// 级联求值（R7-09）：跟踪 at-rule 上下文，只让无条件 :root 的
// 声明参与胜出竞争；条件块子树整棵排除。返回 token → 生效值
// （原文值，含非 hex 形态——调用方按需过滤）。
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
    // 普通级：无层 > 有层（层间后提及者胜）；!important 级反转：
    // 有层 > 无层（层间先提及者胜）。同级同层按源码顺序后者胜。
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
        // 条件 at-rule（@media/@supports/@container/@document/未知
        // 块级 at-rule）：子树整棵排除，不入 token 集。
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

const resolved = resolveRootTokens(stripCssComments(css));
const tokens = {};
for (const [k, v] of Object.entries(resolved)) {
  if (/^#[0-9a-fA-F]{6}$/.test(v)) tokens[k] = v.toLowerCase();
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
    check(
      `${fg} on ${bg}`,
      false,
      "token 未在 globals.css 的无条件 :root 中解析到 #rrggbb 生效值",
    );

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
