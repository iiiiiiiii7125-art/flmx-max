#!/usr/bin/env node
// check-split-referrer-filter.mjs — 删除确认弹窗引用清单口径钉（R2-039）。
//
// node.tsx 的 computeBrokenReferrers 曾对任何带 outboundId 的 splitRules
// 计数，而后端权威口径 outboundTargets（kernel_chain.go）只计
// Action == SplitProxy 的规则：direct/block 规则残留的非零 outboundId
// 不是有效引用，前端弹窗会多报「将被打断的引用」。node.tsx 是页面
// 组件、无前端测试框架可直接执行，故以结构钉固定谓词：
// computeBrokenReferrers 函数体内必须先按 action === "proxy" 过滤
// splitRules 再取 outboundId，且不得再出现未过滤的直取形态。
//
// 提取面（修复轮 6，R6-C4-15 机制处置）：旧实现把函数体切到下一
// 个顶层声明、对原文做链形正则——真实谓词翻成 "direct" 后，在函
// 数体内以块注释逐字引下被钉链形即可满足正向钉（三断言全绿；行
// 注释形态因 // 混入链节不满足，块注释可满足）。现提取先词法剥
// 注释（字符串保留），再按括号配平（跳过字符串/模板）截取函数
// 体，注释引链整体消失、谓词翻转随之点名。后端口径本身另有后端
// 测试钉住 outboundTargets，不在本门范围。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-split-referrer-filter.mjs
// 退出码 0 = 通过，非 0 = 失败。
// 本脚本由 run-checks.mjs 按目录自动枚举接线，无需另行登记。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");

// 词法剥注释：// 与 /* */ 逐字替换为空格（换行保留），字符串/模板
// 字面量内容原样保留（模板 ${} 内的代码照常扫描）。
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

// 括号配平截取函数体：自 start 处首个 { 起，扫描跳过字符串/模板，
// 返回匹配 } 之前的文本（提取对象已剥注释）。
const extractBody = (src, start) => {
  const open = src.indexOf("{", start);
  if (open < 0) return null;
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    const c = src[i];
    if (c === '"' || c === "'" || c === "`") {
      i++;
      while (i < src.length && src[i] !== c) {
        if (src[i] === "\\") i++;
        i++;
      }
      continue;
    }
    if (c === "{") depth++;
    else if (c === "}") {
      depth--;
      if (depth === 0) return src.slice(open + 1, i);
    }
  }
  return null;
};

const src = stripComments(
  readFileSync(join(frontendDir, "src/pages/node.tsx"), "utf8"),
);

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

// 截取 computeBrokenReferrers 函数体（剥注释后的源码、括号配平）。
const start = src.indexOf("function computeBrokenReferrers");
check("computeBrokenReferrers 存在", start >= 0);
const body = start >= 0 ? extractBody(src, start) : null;
check("computeBrokenReferrers 函数体可截取", body !== null);

if (body !== null) {
  check(
    "splitRules 先按 action === \"proxy\" 过滤再取 outboundId",
    /splitRules \|\| \[\]\)\s*\.filter\(\(rule\) => rule\.action === "proxy"\)\s*\.map\(\(rule\) => rule\.outboundId \?\? 0\)/.test(
      body,
    ),
    "未找到 SplitProxy 过滤谓词（口径可能已漂移回全集计数）",
  );

  check(
    "函数体内无未过滤的 splitRules 直取 outboundId 形态",
    !/splitRules \|\| \[\]\)\.map\(\(rule\) => rule\.outboundId/.test(body),
    "仍存在 (ib.splitRules || []).map(rule => rule.outboundId) 直取",
  );
}

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\n删除引用清单口径钉：与后端 outboundTargets 对齐");
