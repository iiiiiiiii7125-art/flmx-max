#!/usr/bin/env node
// check-system-info-counters.mjs — buildNodeSystemInfo 计数器回填校验
// （R2-050 / B-ui-04，无测试框架版）。
//
// info 路径的构建器此前对可选字段缺帧保持 undefined，却把计数器
// uploadTraffic/downloadTraffic/uptime 经 ?? 0 归零：一帧过了 ≥3 键
// 门槛却不含计数字段（如仅带 cpu/memory/disk）时，节点卡的累计流量
// 与 uptime 会被清零显示，与 node.tsx metric 合并路径的「本帧 ??
// 上次 ?? 0」口径不一致。修复后三字段缺帧回填 previous。本脚本把
// system-info.ts 用 typescript.transpileModule 转译后在 Node 里
// 执行并断言（与 check-topayload-reality-guard.mjs 同型做法；该
// 模块无任何 import，转译产物无运行时依赖）。
//
// 运行方法（工作目录 vite-frontend）：
//   node scripts/check-system-info-counters.mjs
// 退出码 0 = 通过，非 0 = 失败。
// 本脚本由 run-checks.mjs 按目录自动枚举接线，无需另行登记。
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const here = dirname(fileURLToPath(import.meta.url));
const frontendDir = join(here, "..");

let ts;
try {
  ts = require("typescript");
} catch {
  // worktree 尚未 pnpm install 时，退回主仓的 node_modules。
  ts = require(
    "/home/hatch/workspace/flvx-max/vite-frontend/node_modules/typescript",
  );
}

const srcPath = join(frontendDir, "src/pages/node/system-info.ts");
const source = readFileSync(srcPath, "utf8");

const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
  reportDiagnostics: true,
});

const runtimeRequires = [
  ...outputText.matchAll(/require\(["']([^"']+)["']\)/g),
].map((m) => m[1]);
const failures = [];
const check = (label, ok, detail = "") => {
  console.log(`${ok ? "PASS" : "FAIL"}  ${label}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(label);
};

check(
  "转译产物无运行时 require",
  runtimeRequires.length === 0,
  `发现 require: ${runtimeRequires.join(", ")}`,
);

const mod = { exports: {} };
new Function("module", "exports", "require", outputText)(
  mod,
  mod.exports,
  require,
);
const { buildNodeSystemInfo } = mod.exports;
check(
  "导出 buildNodeSystemInfo",
  typeof buildNodeSystemInfo === "function",
  `typeof = ${typeof buildNodeSystemInfo}`,
);

if (typeof buildNodeSystemInfo === "function") {
  const prev = {
    uploadTraffic: 1000,
    downloadTraffic: 2000,
    uploadSpeed: 0,
    downloadSpeed: 0,
    uptime: 500,
  };

  // ① 帧过 ≥3 键门槛但不含任何计数字段：三计数器保持上次值（本条
  //    修复的核心用例；修前三者全为 0）。
  const r1 = buildNodeSystemInfo(
    { cpu_usage: 12.5, memory_usage: 40, disk_usage: 70 },
    prev,
  );
  check(
    "① 无计数帧回填 previous（1000/2000/500）",
    r1 !== null &&
      r1.uploadTraffic === 1000 &&
      r1.downloadTraffic === 2000 &&
      r1.uptime === 500,
    JSON.stringify(r1),
  );
  check(
    "① 同帧可选字段照常取帧值（cpuUsage 12.5）",
    r1 !== null && r1.cpuUsage === 12.5,
    JSON.stringify(r1),
  );

  // ② 部分计数帧：帧内有的取帧值、缺的逐个回填。
  const r2 = buildNodeSystemInfo(
    { cpu_usage: 1, memory_usage: 2, bytes_transmitted: 7000 },
    prev,
  );
  check(
    "② 部分计数帧逐字段回填（7000/2000/500）",
    r2 !== null &&
      r2.uploadTraffic === 7000 &&
      r2.downloadTraffic === 2000 &&
      r2.uptime === 500,
    JSON.stringify(r2),
  );

  // ③ 全量计数帧：全取帧值；uptime 差 60s 超出速率窗，速率仍为 0
  //    （既有语义不变）。
  const r3 = buildNodeSystemInfo(
    {
      bytes_transmitted: 5000,
      bytes_received: 6000,
      uptime: 560,
      cpu_usage: 3,
    },
    prev,
  );
  check(
    "③ 全量帧取帧值（5000/6000/560，速率 0）",
    r3 !== null &&
      r3.uploadTraffic === 5000 &&
      r3.downloadTraffic === 6000 &&
      r3.uptime === 560 &&
      r3.uploadSpeed === 0 &&
      r3.downloadSpeed === 0,
    JSON.stringify(r3),
  );

  // ④ 窗内差分速率推导不变：uptime +5s、双向各 +500 → 100/s。
  const r4 = buildNodeSystemInfo(
    {
      bytes_transmitted: 1500,
      bytes_received: 2500,
      uptime: 505,
      cpu_usage: 9,
    },
    prev,
  );
  check(
    "④ 窗内速率推导（100/100）",
    r4 !== null && r4.uploadSpeed === 100 && r4.downloadSpeed === 100,
    JSON.stringify(r4),
  );

  // ⑤ 无 previous 且帧无计数：回落初值 0（首帧语义不变）。
  const r5 = buildNodeSystemInfo(
    { load1: 0.5, load5: 0.6, load15: 0.7 },
    undefined,
  );
  check(
    "⑤ 无 previous 时计数器初值 0",
    r5 !== null &&
      r5.uploadTraffic === 0 &&
      r5.downloadTraffic === 0 &&
      r5.uptime === 0,
    JSON.stringify(r5),
  );

  // ⑥ 未过 ≥3 键门槛的帧仍返回 null（门槛语义不变）。
  const r6 = buildNodeSystemInfo({ cpu_usage: 1 }, prev);
  check("⑥ 未过门槛帧返回 null", r6 === null, JSON.stringify(r6));
}

if (failures.length > 0) {
  console.log(`\n失败 ${failures.length} 项：${failures.join("；")}`);
  process.exit(1);
}
console.log("\nbuildNodeSystemInfo 计数器口径：与 metric 合并路径对齐");
