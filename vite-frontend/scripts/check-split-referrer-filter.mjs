#!/usr/bin/env node
// check-split-referrer-filter.mjs — 删除确认弹窗引用清单口径钉（R2-039）。
//
// node.tsx 的 computeBrokenReferrers 曾对任何带 outboundId 的 splitRules
// 计数，而后端权威口径 outboundTargets（kernel_chain.go）只计
// Action == SplitProxy 的规则：direct/block 规则残留的非零 outboundId
// 不是有效引用，前端弹窗会多报「将被打断的引用」。node.tsx 是页面
// 组件、无前端测试框架可直接执行，故按仓内既有做法以文本钉固定谓词：
// computeBrokenReferrers 函数体内必须先按 action === "proxy" 过滤
// splitRules 再取 outboundId，且不得再出现未过滤的直取形态。
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
const src = readFileSync(join(frontendDir, "src/pages/node.tsx"), "utf8");

const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

// 截取 computeBrokenReferrers 函数体（定义到下一个顶层 function/声明）。
const start = src.indexOf("function computeBrokenReferrers");
check("computeBrokenReferrers 存在", start >= 0);
let body = "";
if (start >= 0) {
  const rest = src.slice(start);
  const next = rest.indexOf("\nexport default function");
  body = next > 0 ? rest.slice(0, next) : rest;
}

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

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\n删除引用清单口径钉：与后端 outboundTargets 对齐");
