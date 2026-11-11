#!/usr/bin/env node
// check-connlog-copy.mjs — 入站连接日志「查看方向」文案守卫（FIX-216）。
//
// 2.1.5 缺陷：连接日志弹窗中转落地空状态写「中转入口的连接记录请在
// 对应的入口入站的连接日志中查看」，把用户指向不存在的视图。真值：
// 经中转进入的连接由入口节点采集、按拓扑归属到落地入站、显示在落地
// 入站自己的连接日志弹窗里。本守卫钉死两件事：
//   1. 旧错句在用户面全集（vite-frontend/src、doc/、仓库根 README.md）
//      零命中——含同族句「中转入口的连接记在入口机」（只说记在入口机、
//      不给归属展示落点，同为误导）。
//   2. 新口径关键句在三处修复点在位（弹窗空状态、入站表单、nodes 指南）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-connlog-copy.mjs（run-checks 按目录自动入册）
// 退出码 0 = 通过，非 0 = 失败（打印命中的文件与行）。
import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const repoDir = join(frontendDir, "..");

const FORBIDDEN = [
  "请在对应的入口入站的连接日志中查看",
  "中转入口的连接记在入口机",
];

const REQUIRED = [
  {
    file: join(frontendDir, "src/pages/inbound/connlog-modal.tsx"),
    needle: "经中转进入的连接由入口节点采集、归属到本入站，连接记录会显示在这里",
    label: "弹窗中转落地空状态新口径",
  },
  {
    file: join(frontendDir, "src/pages/inbound/inbound-form-modal.tsx"),
    needle: "归属到本入站，在本入站的「连接日志」中查看",
    label: "入站表单仅中转提示新口径",
  },
  {
    file: join(repoDir, "doc/guide/nodes.md"),
    needle: "记到被中转的入站名下，在该入站的连接日志中查看",
    label: "nodes 指南新口径",
  },
];

const SCAN_ROOTS = [
  { dir: join(frontendDir, "src"), exts: [".ts", ".tsx"] },
  { dir: join(repoDir, "doc"), exts: [".md"] },
];
const SCAN_FILES = [join(repoDir, "README.md")];

function* walk(dir, exts) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "node_modules" || entry.name === ".git") continue;
      yield* walk(p, exts);
    } else if (exts.some((e) => entry.name.endsWith(e))) {
      yield p;
    }
  }
}

let failed = 0;

// 1. 旧错句零命中
const files = [];
for (const { dir, exts } of SCAN_ROOTS) files.push(...walk(dir, exts));
for (const f of SCAN_FILES) if (statSync(f, { throwIfNoEntry: false })) files.push(f);

for (const file of files) {
  const text = readFileSync(file, "utf8");
  const lines = text.split("\n");
  for (const bad of FORBIDDEN) {
    lines.forEach((line, i) => {
      if (line.includes(bad)) {
        console.log(`FAIL  旧错句残留：${relative(repoDir, file)}:${i + 1}  「${bad}」`);
        failed++;
      }
    });
  }
}

// 2. 新口径在位
for (const { file, needle, label } of REQUIRED) {
  const text = readFileSync(file, "utf8");
  if (!text.includes(needle)) {
    console.log(`FAIL  新口径缺失（${label}）：${relative(repoDir, file)} 未含「${needle}」`);
    failed++;
  }
}

if (failed > 0) {
  console.log(`\n连接日志文案守卫：失败 ${failed} 项`);
  process.exit(1);
}
console.log(
  `连接日志文案守卫：旧错句 ${FORBIDDEN.length} 句在 ${files.length} 个用户面文件零命中，新口径 ${REQUIRED.length} 处在位`,
);
console.log("--- check-connlog-copy.mjs: PASS");
