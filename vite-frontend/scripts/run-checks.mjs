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
// 运行方法（工作目录 vite-frontend）：
//   node scripts/run-checks.mjs   （或 pnpm run check:all）
// 退出码 0 = 全部通过，非 0 = 至少一个 check 失败。
import { readdirSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");
const scripts = readdirSync(here)
  .filter((f) => /^check-.*\.mjs$/.test(f))
  .sort();

if (scripts.length === 0) {
  console.log("FAIL  scripts/ 下没有 check-*.mjs（清单为空即异常）");
  process.exit(1);
}

const failed = [];

for (const name of scripts) {
  console.log(`\n=== ${name} ===`);
  const res = spawnSync(process.execPath, [join(here, name)], {
    cwd: frontendDir,
    stdio: "inherit",
  });

  if (res.status === 0) {
    console.log(`--- ${name}: PASS`);
  } else {
    console.log(`--- ${name}: FAIL (exit ${res.status})`);
    failed.push(name);
  }
}

console.log(
  `\n前端 check 全集：${scripts.length} 个，失败 ${failed.length} 个` +
    (failed.length ? `：${failed.join(", ")}` : ""),
);
process.exit(failed.length > 0 ? 1 : 0);
