#!/usr/bin/env node
// run-checks.mjs — 前端 check-*.mjs 全集运行器（A-F-01 门禁落地）。
//
// 此前 vite-frontend/scripts/ 的 check-*.mjs 没有统一入口：两个挂在
// package.json、三个无名无姓，集成/发版门禁清单只跑仓库根 scripts/，
// 于是 check-pure-enc-preset 在集成树上红了也没人看见。本运行器以
// 「目录即清单」为口径：机械枚举同目录 check-*.mjs 全集逐个以 node
// 执行，任一失败整体失败并点名——新增 check 脚本自动入册，无需在
// 任何清单里再登记一遍。根门禁 scripts/check-doc-consistency.sh
// 第 14 项转跑本运行器，CI 前端 job 因此恒跑全集。
//
// BG-01（R2-026）：枚举口径扩为递归含子目录——scripts/demo/ 的
// check-fixtures-content.mjs 与 check-shape-drift.mjs 此前只在 demo
// README 的手工命令里出现、无任何自动执行点，守护的 fixtures 白名单
// 逐格一致与形状漂移在脚本退化后无信号。递归枚举后两者与顶层同册，
// 子目录内新增 check-*.mjs 同样自动入册（demo/check.mjs 是浏览器
// 走查、不匹配 check-* 命名口径，仍由 CI 的 demo 步单独执行）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/run-checks.mjs   （或 pnpm run check:all）
// 退出码 0 = 全部通过，非 0 = 至少一个 check 失败。
import { readdirSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");

// 递归枚举 scripts/ 本层与全部子目录的 check-*.mjs（目录即清单）。
// 返回 [{ label, path }]：label 为相对 scripts/ 的路径，顶层脚本与
// 既往一致只显示文件名，子目录脚本带子目录前缀以便点名定位。
function collectChecks(dir, prefix) {
  const out = [];

  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      out.push(...collectChecks(join(dir, entry.name), `${prefix}${entry.name}/`));
    } else if (/^check-.*\.mjs$/.test(entry.name)) {
      out.push({ label: `${prefix}${entry.name}`, path: join(dir, entry.name) });
    }
  }

  return out;
}

const scripts = collectChecks(here, "").sort((a, b) =>
  a.label < b.label ? -1 : a.label > b.label ? 1 : 0,
);

if (scripts.length === 0) {
  console.log("FAIL  scripts/ 下没有 check-*.mjs（清单为空即异常）");
  process.exit(1);
}

const failed = [];

for (const { label, path } of scripts) {
  console.log(`\n=== ${label} ===`);
  const res = spawnSync(process.execPath, [path], {
    cwd: frontendDir,
    stdio: "inherit",
  });

  if (res.status === 0) {
    console.log(`--- ${label}: PASS`);
  } else {
    console.log(`--- ${label}: FAIL (exit ${res.status})`);
    failed.push(label);
  }
}

console.log(
  `\n前端 check 全集：${scripts.length} 个，失败 ${failed.length} 个` +
    (failed.length ? `：${failed.join(", ")}` : ""),
);
process.exit(failed.length > 0 ? 1 : 0);
